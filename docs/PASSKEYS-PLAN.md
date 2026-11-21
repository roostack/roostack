# Passkeys (plan, 2026-11-19)

**Asked by the maintainer (2026-11-19):** begin building passkey support, for Roostack's own pages and the sign-in page at `my.<domain>`. SPEC §5 says "Passkeys later", and §5.2's "Signing in" names `go-webauthn` as the module to agree when they come. Nothing is built yet: this is the design, the choice of dependency, and a mock-up, for the maintainer to approve before any code. The questions are at the end.

The mock-up is in [docs/mockups/passkeys](mockups/passkeys/README.md).

**Decided (2026-11-20), the maintainer's answers to §4, and built the same day** (SPEC.md §5, "Passkeys", is now the record of what was built):

1. **go-webauthn** v0.18.2, not our own implementation: §1's recommendation and §2.6's DER columns are superseded (the store keeps go-webauthn's record of each passkey as JSON).
2. A passkey **alone** signs someone in.
3. It **meets two-factor** for Owners and Admins by itself.
4. **No** "passkeys only" setting.
5. **Members too.**
6. Adding one **needs a fresh confirmation**.
7. The mock-ups were approved as they are. As built, People's "Turn off two-factor (lost phone)" removes passkeys too rather than having a button of its own, and a passkey is offered after a password only to someone who must use two-factor (for anyone else, the password alone still signs in).

## Why

- **A passkey can't be phished.** The browser only uses it at the address it was made for, so a fake sign-in page gets nothing. A code from an authenticator app can be typed into a fake page and used within its 30 seconds.
- **It's quicker.** Touch ID, Face ID, Windows Hello, or a phone's screen lock instead of a password and a six-digit code. For the family members of a lab, that's the difference between using the sign-in page and asking for help.
- **It's the second factor too.** A passkey is something you have (the device, or the account it syncs through) that only opens with something you are or know (a fingerprint, the device's PIN). Roostack will ask the device to check that ("user verification"), so a passkey on its own is as strong as a password and a code together.

## 1. The dependency: go-webauthn, or our own

### go-webauthn/webauthn

- **Version:** v0.18.2 (19 September 2026). Releases are frequent: v0.14.0 in September 2025, v0.17.0 in April 2026, v0.18.0 in August 2026. It's still before 1.0, and minor versions change its API.
- **Licence:** BSD 3-Clause.
- **Who uses it:** it began as Duo's `duo-labs/webauthn`, and Authelia, Gitea and Forgejo use it. It's the usual choice in Go.
- **What it brings in** (its `go.mod` at v0.18.2; `testify` and `go.uber.org/mock` are only for its tests and aren't built in):

  | Module | What for |
  |---|---|
  | `github.com/fxamacker/cbor/v2` v2.9.4 | reading the authenticator's CBOR |
  | `github.com/x448/float16` v0.8.4 | needed by the CBOR module |
  | `github.com/go-webauthn/x` v0.3.1 | its own helpers |
  | `github.com/go-viper/mapstructure/v2` v2.5.0 | its configuration |
  | `github.com/golang-jwt/jwt/v5` v5.3.1 | the FIDO Metadata Service's signed list |
  | `github.com/google/go-tpm` v0.9.8 | checking Windows TPM attestation |
  | `github.com/tinylib/msgp` v1.6.4 | serialising its metadata |
  | `github.com/philhofer/fwd` v1.2.0 | needed by msgp |
  | `github.com/google/uuid` v1.6.0 | already in Roostack's `go.mod` |
  | `golang.org/x/crypto`, `golang.org/x/sys` | already in Roostack's `go.mod` |

  That's **eight new modules** in a build that has three direct dependencies today (`yaml`, `x/crypto`, `sqlite`). Most of the code they bring is for **attestation**: proving which make of authenticator made a passkey, which an enterprise needs and a home lab doesn't.

### Our own, of the part we need

What Roostack needs is small, and every piece of it is in Go's standard library:

- **No attestation** (`attestation: "none"`): Roostack won't ask which make of device made a passkey, so there are no certificate chains, no TPM, and no metadata service.
- **Three algorithms:** ES256 (`crypto/ecdsa` on P-256), EdDSA (`crypto/ed25519`), and RS256 (`crypto/rsa`, for Windows Hello). Those cover every passkey provider and security key in use.
- **No CBOR at all.** When a passkey is made, the browser already hands over the public key as a standard DER key (`response.getPublicKey()`, which `crypto/x509` reads), the algorithm (`getPublicKeyAlgorithm()`), and the authenticator data (`getAuthenticatorData()`). That has been in Chrome since 85, Safari since 16, and Firefox since 119. The authenticator data is a fixed binary layout (the address's hash, flags, and a counter, then the credential's ID), which a few lines read. Signing in returns only JSON, the authenticator data, and a signature.
- **About 350 lines of Go and 80 of JavaScript** (a `passkey.js` among the static files, since the pages' Content-Security-Policy allows only scripts of Roostack's own), with tests built from real passkeys recorded once from an iPhone, an Android phone, Windows Hello, a YubiKey, 1Password, and Bitwarden, and fuzz tests on everything that reads what the browser sends, as `authkit` has.

What's checked, which is what the WebAuthn standard requires of a server that doesn't use attestation:

- **Making a passkey:** the client data's `type` is `webauthn.create`, its challenge is the one Roostack gave (one use, five minutes), and its `origin` is exactly `https://my.<domain>` or `https://roost.<domain>`; the authenticator data's address hash is SHA-256 of the lab's domain, with "user present" and "user verified" set; the credential ID in it matches the one sent, is at most 1,023 bytes, and isn't anyone's already; the key is one of the three algorithms (P-256 only for ES256, at least 2,048 bits for RSA).
- **Signing in with one:** the same for `webauthn.get`; the credential is one Roostack knows, belonging to the person it says (the user handle); the signature over the authenticator data and the client data's hash checks with its key; and its counter, when the device keeps one, has gone up (a counter that went backwards is refused and audited, as it means a copied key).
- **One thing is trusted rather than checked:** when the passkey is made, Roostack takes the public key the browser read from the authenticator rather than reading the CBOR itself. Without attestation that's no weaker: the authenticator's answer isn't signed by anyone Roostack trusts, so whatever sends it could send any key in either case. It's sent from a session that has just confirmed it's them (§2.4).

**Recommendation: our own**, in `authkit` (a new `authkit/passkey` package, standard library only), so that Roostack Cloud can use the same code later, as it does for passwords and codes (SPEC §5, "Shared with Roostack Cloud"). It keeps the build at its three direct dependencies, leaves out the attestation code we'd never use but would have to keep updated, and avoids following a library whose API still changes before 1.0. The cost is security-sensitive code of our own, which is why the list of checks above is short and fixed, and why the tests use recordings from real devices. If attestation ever matters (an Owner wanting only security keys of one make, say), go-webauthn is the way to get it, and the stored passkeys would carry over, as both keep the same facts.

## 2. Design

### 2.1 What a passkey does

- **Signs in on its own,** instead of a password and a code: on the sign-in page at `my.<domain>`, and on Roostack's own login when it's reached at its address (`roost.<domain>`) over HTTPS. Roostack will always ask the device to verify the person (`userVerification: "required"`), so a passkey counts as both factors.
- **Stands in for the code** wherever Roostack asks someone to confirm it's them:
  - the confirm step on the sign-in page after Roostack's idle limit (SPEC §5.2, "How long Roostack's pages stay open");
  - showing the recovery key (§6.9);
  - adding a VPN device (§10.11);
  - and adding another passkey (§2.4).
- **Meets "who must use two-factor"** (SPEC §5): an Owner, an Admin while Roostack's pages are public, or anyone when the Owner asks everyone, has met it with an authenticator app **or** a passkey. A passkey doesn't need a password typed with it.
- **The password and the code stay.** Passkeys only work at the lab's address over HTTPS (§2.2), and Roostack's own login through the SSH tunnel, at a local address, or on the setup pages is the way in when that address doesn't work. So a passkey is added to an account; it never replaces the password. Turning the password off ("passkeys only") is question 4.

### 2.2 Where passkeys work: the lab's domain

- **One passkey for both addresses.** A passkey belongs to a domain (its "relying party ID"), and works on that domain and the addresses under it. Roostack will make passkeys for the **lab's domain** (`lab.grahamhq.com`, or `smiths.roost.ac` for a name from Roostack Cloud), so a passkey made on `my.` works on `roost.` too, and nothing else on the internet can use it. Roostack will accept only those two exact addresses, never another subdomain, so an app at `photos.<domain>` can't ask for the lab's passkeys even though the browser would allow it.
- **Where they can't work,** the browser refuses before Roostack sees anything, and the page will say so instead of showing the button:
  - a lab without a domain yet, reached by its IP address (WebAuthn doesn't work on IP addresses);
  - the SSH tunnel (`localhost`), and the setup pages;
  - plain HTTP.
- **When the lab's domain changes,** its passkeys stop working, since the browser ties each to the old domain. Roostack keeps the domain each passkey was made for, and when it differs from the lab's, Security will say "Made for old.example.com, so it won't work since the lab's address changed: remove it and add a new one." Signing in then needs the password and code, and a person whose only second factor was a passkey will be asked to set up two-factor again at their next sign-in, as they would be today without it. The page that changes the domain will warn that everyone's passkeys will stop working, and how many there are.
- **Restoring the lab onto a new server** with the same domain keeps its passkeys: they're in Roostack's database, which is in the lab's backup.

### 2.3 Signing in

- **The sign-in page** will offer a passkey in two ways:
  - **in the username box,** as the browser's autofill suggests it (`autocomplete="username webauthn"`, "conditional mediation"), so someone who has one just picks it;
  - **as a button,** "Sign in with a passkey", for browsers that don't show passkeys in autofill.
  - Typing a username and password carries on working as now.
- **After a password,** someone with both a passkey and an authenticator app will be asked for either: "Use your passkey", or the code box beneath it.
- **The confirm step** after the idle limit offers the passkey first, with the code beneath it.
- **It fails safely:** when the person cancels, or the device refuses, the page will say "The passkey didn't work, or you cancelled it. Try again, or sign in with your password." A passkey Roostack doesn't know (removed, or for another lab) gets the same sentence, so the page doesn't say which passkeys exist.
- **No guesses to count:** unlike codes, a passkey can't be guessed, so it isn't counted towards the lock on wrong codes (SPEC §6.10 item 6). Failed passkey sign-ins are logged and audited without naming an account.

### 2.4 Adding and removing passkeys

- **Where:** Your account → Security (`my.<domain>/account/security`), in a new **Passkeys** card between Password and Two-factor, for everyone, Members included.
- **Adding one** needs the person to have confirmed it's them in the last five minutes: their password and code, or a passkey they already have. A session alone isn't enough, because an added passkey would let whoever had the session back in for good. After the device has made it, Roostack will ask for a name, suggesting the browser and system ("Safari on iPhone", as for sessions).
- **Each passkey shows** its name, when it was added, when it was last used, and whether it's **synced** (copied to the person's Apple, Google, or password manager account, so it'll be there on a new phone) or **on this device only** (a security key, or a device that doesn't sync), from the flags the device reports.
- **Removing one** asks to confirm, but not for the password: it can only lock the person out of a way in, never let anyone else in. The last way to meet a required two-factor (an Owner's only passkey, with no authenticator app) can't be removed until they've added another.
- **The Owner, on People,** can remove someone's passkeys, beside "Turn off two-factor", for a person who's lost their phone.
- **A nudge, not a rule:** with one passkey and no authenticator app, the card will say "If you lose this device, you'll need your password and the Owner to get back in. Add a second passkey or an authenticator app."

### 2.5 Recovery

- **A lost passkey** is like a lost phone today: sign in with the password and code, or ask the Owner, who can remove the person's passkeys on People.
- **An Owner who's lost every way in** uses the command line, as now: `sudo roostack user reset-2fa <name>` will remove their passkeys as well as their authenticator app, and say how many of each it removed. The recovery key (§6.9) is for the lab's backups and isn't involved: passkeys don't change it.
- **No recovery codes,** as decided for two-factor (SPEC §5, "Password recovery").

### 2.6 What's stored

A new step at the end of `store.migrations` (43):

```sql
-- 43 (2026-11-..): passkeys (SPEC.md §5, "Passkeys"). Each person's
-- passkeys go with them; the public key only, as the device keeps the
-- private one.
ALTER TABLE users ADD COLUMN passkey_handle BLOB;
CREATE TABLE passkeys (
    id             INTEGER PRIMARY KEY,
    user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id  BLOB NOT NULL UNIQUE,
    public_key     BLOB NOT NULL,          -- DER (SubjectPublicKeyInfo)
    algorithm      INTEGER NOT NULL,       -- COSE: -7 ES256, -8 EdDSA, -257 RS256
    rp_id          TEXT NOT NULL,          -- the lab's domain when it was made
    name           TEXT NOT NULL,
    sign_count     INTEGER NOT NULL DEFAULT 0,
    synced         INTEGER NOT NULL DEFAULT 0,  -- backup state (BS)
    transports     TEXT NOT NULL DEFAULT '',    -- e.g. "internal,hybrid"
    created_at     INTEGER NOT NULL,
    last_used_at   INTEGER NOT NULL DEFAULT 0
);
```

- **The user handle** (`passkey_handle`) is 32 random bytes made with someone's first passkey, never their username or number, since the device stores it and shows it to whatever asks.
- **Challenges** stay in memory, like the logins waiting for a code (`expiring`), for five minutes and one use, tied to the browser by a cookie.

### 2.7 The audit log and emails

- **Audited:** "passkey added: Safari on iPhone"; "passkey removed: …" (by the person, or by the Owner, naming them); "login with a passkey" (and "through the sign-in page (confirmed with a passkey)" for the confirm step); "passkey refused: its counter went backwards, so it may have been copied"; and failed passkey sign-ins, without an account.
- **Security emails** (Your account → Notifications, which always come): a passkey added, and a passkey removed by the Owner, join the password and two-factor changes.

### 2.8 Proposed SPEC.md text (§5, replacing "Passkeys later")

> - **Passkeys — Proposed (2026-11-19):** anyone can add passkeys on Your account → Security, which sign them in at `my.<domain>` and on Roostack's own pages at `roost.<domain>` without a password or code, and stand in for the code wherever Roostack asks someone to confirm it's them. Roostack always asks the device to verify the person, so a passkey meets "who must use two-factor" by itself. They're made for the lab's domain, so one works at both addresses, and Roostack accepts only those two; a lab reached by its IP address, through the SSH tunnel, or on its setup pages keeps the password and code, which is why a passkey is added to an account rather than replacing its password. When the domain changes, the passkeys made for the old one stop working and Security says so. Adding one needs the person to have confirmed it's them in the last five minutes; removing one doesn't, except that the last way to meet a required two-factor can't be removed. The Owner can remove someone's passkeys on People, and `roostack user reset-2fa` removes them too. No attestation: Roostack doesn't ask which make of device made a passkey. The code is our own (`authkit/passkey`, standard library only: ES256, EdDSA, and RS256), shared with Roostack Cloud. Considered: `go-webauthn` (eight more modules, most of them for attestation, and an API still changing before 1.0); passkeys only as a second step after the password (safer against a stolen device without a screen lock, but a device that verifies the person already is two factors, and it gives up the speed); passkeys replacing passwords (they can't work through the SSH tunnel or before the lab has a domain).

## 3. Building it, in steps

1. `authkit/passkey`: the checks in §1, with recorded real-device tests and fuzz tests.
2. The store step and `store` functions; `reset-2fa` and People's removal.
3. Your account → Security: the Passkeys card, adding, naming, and removing; the emails and the audit log.
4. Signing in: the sign-in page (autofill and the button), the step after a password, and Roostack's own login at its address.
5. The confirm steps: the idle limit, the recovery key, VPN devices, and adding a passkey.
6. The domain change: the warning, and Security's note on old passkeys.
7. On a VM, with a real domain: an iPhone, an Android phone, a Mac, Windows Hello, a YubiKey, 1Password, and Bitwarden; phone and desktop widths.

## 4. Questions for the maintainer

1. **The dependency:** our own `authkit/passkey` with the standard library (recommended), or `go-webauthn` v0.18.2 and its eight modules?
2. **Passkeys alone sign in** (recommended), or only as a second step after the password?
3. **A passkey meets the two-factor requirement** for Owners and Admins by itself (recommended), or must an Owner keep an authenticator app as well, as a fallback?
4. **"Passkeys only":** let a person turn off signing in with their password and code at `my.` and `roost.` (still allowed through the SSH tunnel)? Recommended: not yet, as it's one more way to lock yourself out; later, once passkeys have been used for a while.
5. **Members too** (recommended), or Owners and Admins first?
6. **Adding a passkey needs confirming in the last five minutes** with the password and code, or an existing passkey (recommended), or is the session enough?
7. **The mock-up:** is the Passkeys card and the sign-in page as you'd want them? In particular, the card between Password and Two-factor, and the passkey button above the password on the sign-in page.
