# Passkeys (mock-up, 2026-11-19)

For [docs/PASSKEYS-PLAN.md](../../PASSKEYS-PLAN.md), so the maintainer can approve the pages before they're built.

**Open them** through a web server at the repo root, so the real CSS loads:

```sh
python3 -m http.server 8791 --bind 127.0.0.1
```

then <http://127.0.0.1:8791/docs/mockups/passkeys/security.html>. The bar at the bottom right switches between the pages, each page's states, and light or dark. What's new is outlined with a dashed line. The pages use the real stylesheet, the Your account and single sign-on mock-ups' CSS, and a little of their own (`mock.css`).

- **Security** (Your account → Security, at `my.<domain>/account/security`): a new **Passkeys** card between Password and Two-factor. Its states:
  - two passkeys, one synced and one on a security key;
  - none yet;
  - adding one: confirming it's you first, with a passkey you already have, or your password and code;
  - adding one: the device's own window, then a name;
  - one passkey and no authenticator app, which can't be removed and comes with a nudge to add another;
  - after the lab's domain changed, with a passkey made for the old one.
- **Sign-in page** (`my.<domain>`): "Sign in with a passkey" above the username and password, and the passkey offered in the username box's autofill; the step after a password, with the passkey or a code; the confirm step after Roostack's idle limit; a passkey that didn't work; and Roostack's own login through the SSH tunnel, where passkeys can't work.

Not mocked: People's "Remove their passkeys" beside "Turn off two-factor", and the warning on the page that changes the lab's domain (both are a sentence and a button, in the plan's §2.2 and §2.4).
