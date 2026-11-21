package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/fxamacker/cbor/v2"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/store"
)

// softKey is a passkey authenticator in software, as a phone or security
// key answers: an ES256 key, its counter, and the person's handle once it
// has made a passkey. It always says the person was verified.
type softKey struct {
	key     *ecdsa.PrivateKey
	id      []byte
	handle  []byte
	counter uint32
}

func newSoftKey(t *testing.T) *softKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	rand.Read(id)
	return &softKey{key: k, id: id}
}

var b64 = base64.RawURLEncoding

// options reads the publicKey options Roostack sent.
func passkeyOptions(t *testing.T, body string) map[string]any {
	t.Helper()
	var o struct {
		PublicKey map[string]any `json:"publicKey"`
	}
	if err := json.Unmarshal([]byte(body), &o); err != nil || o.PublicKey == nil {
		t.Fatalf("passkey options %q: %v", body, err)
	}
	return o.PublicKey
}

func clientData(typ, challenge, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": typ, "challenge": challenge, "origin": origin, "crossOrigin": false})
	return b
}

// authData is the authenticator's data: the address's hash, flags (user
// present and verified, and synced), the counter, and with attested the
// new passkey's ID and public key.
func (k *softKey) authData(rpID string, attested bool) []byte {
	h := sha256.Sum256([]byte(rpID))
	flags := byte(0x01 | 0x04 | 0x08 | 0x10)
	if attested {
		flags |= 0x40
	}
	out := append(h[:], flags)
	out = binary.BigEndian.AppendUint32(out, k.counter)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID: none
		out = binary.BigEndian.AppendUint16(out, uint16(len(k.id)))
		out = append(out, k.id...)
		raw, _ := k.key.PublicKey.Bytes() // 0x04, then X and Y
		x, y := raw[1:33], raw[33:]
		cose, _ := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x, -3: y})
		out = append(out, cose...)
	}
	return out
}

// create makes a passkey from Roostack's creation options, as seen at
// origin, and returns the browser's JSON for it.
func (k *softKey) create(t *testing.T, body, origin string) string {
	t.Helper()
	o := passkeyOptions(t, body)
	rp := o["rp"].(map[string]any)["id"].(string)
	k.handle, _ = b64.DecodeString(o["user"].(map[string]any)["id"].(string))
	cd := clientData("webauthn.create", o["challenge"].(string), origin)
	att, _ := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": k.authData(rp, true)})
	b, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"clientExtensionResults": map[string]any{},
		"response":               map[string]any{"clientDataJSON": b64.EncodeToString(cd), "attestationObject": b64.EncodeToString(att), "transports": []string{"internal"}}})
	return string(b)
}

// get signs Roostack's request options, as seen at origin.
func (k *softKey) get(t *testing.T, body, origin string) string {
	t.Helper()
	o := passkeyOptions(t, body)
	rp, _ := o["rpId"].(string)
	k.counter++
	ad := k.authData(rp, false)
	cd := clientData("webauthn.get", o["challenge"].(string), origin)
	sum := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"clientExtensionResults": map[string]any{},
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad),
			"signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(k.handle)}})
	return string(b)
}

// askOptions is the page's script asking for a ceremony's options.
func askOptions(t *testing.T, srv *httptest.Server, c *http.Client, host, path string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+path, nil)
	req.Host = host
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://"+host)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// Adding a passkey on Your account, signing in with it alone at my., the
// checks a passkey must pass, and removing one (SPEC.md §5, "Passkeys").
func TestPasskeys(t *testing.T) {
	h := newHarness(t, "")
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	h.server.Domain = "lab.example.com"
	ctx := context.Background()
	id := addPerson(t, h.store, "sam", "owner")
	secret, _ := authkit.NewTOTPSecret()
	h.store.SetTOTP(ctx, id, secret, 0)
	origin := "https://" + signInHost

	// Signed in with a password and code.
	call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"sam"}, "password": {"a long password"}, "next": {"/"}})
	code, _ := authkit.TOTPCode(secret, nowFunc())
	if resp, _ := call(t, srv, c, "POST", signInHost, "/sign-in/code", url.Values{"code": {code}, "next": {"/"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("signing in: %d", resp.StatusCode)
	}
	if _, body := call(t, srv, c, "GET", signInHost, "/account/security", nil); !strings.Contains(body, "Add a passkey") || !strings.Contains(body, `<span class="pill">None</span>`) {
		t.Fatalf("Security without passkeys:\n%s", body)
	}

	// Adding one asks it's them first; the device can't be asked before.
	if _, body := call(t, srv, c, "GET", signInHost, "/account/passkeys/add", nil); !strings.Contains(body, "Confirm it's you") {
		t.Fatalf("adding a passkey doesn't ask it's them:\n%s", body)
	}
	if resp, _ := askOptions(t, srv, c, signInHost, "/account/passkeys/options/add"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("options before confirming: %d", resp.StatusCode)
	}
	if resp, _ := call(t, srv, c, "POST", signInHost, "/account/passkeys/confirm", url.Values{"current": {"wrong password!"}, "code": {code}}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a wrong password: %d", resp.StatusCode)
	}
	next, _ := authkit.TOTPCode(secret, nowFunc().Add(30e9))
	if resp, _ := call(t, srv, c, "POST", signInHost, "/account/passkeys/confirm", url.Values{"current": {"a long password"}, "code": {next}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("confirming: %d", resp.StatusCode)
	}
	if _, body := call(t, srv, c, "GET", signInHost, "/account/passkeys/add", nil); !strings.Contains(body, "Save passkey") {
		t.Fatalf("the naming step:\n%s", body)
	}
	phone := newSoftKey(t)
	_, opts := askOptions(t, srv, c, signInHost, "/account/passkeys/options/add")
	if !strings.Contains(opts, `"rp":{"name":"`) || !strings.Contains(opts, `"id":"lab.example.com"`) || !strings.Contains(opts, `"residentKey":"required"`) ||
		!strings.Contains(opts, `"userVerification":"required"`) {
		t.Errorf("creation options: %s", opts)
	}
	resp, _ := call(t, srv, c, "POST", signInHost, "/account/passkeys/add", url.Values{"name": {"Sam's phone"}, passkeyField: {phone.create(t, opts, origin)}})
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "passkey-added") {
		t.Fatalf("adding: %d %q", resp.StatusCode, loc)
	}
	u, _ := h.store.UserByID(ctx, id)
	if u.Passkeys != 1 {
		t.Fatalf("passkeys after adding: %d", u.Passkeys)
	}
	if _, body := call(t, srv, c, "GET", signInHost, "/account/security", nil); !strings.Contains(body, "Sam&#39;s phone") || !strings.Contains(body, "synced across your devices") {
		t.Errorf("Security with a passkey:\n%s", body)
	}
	// The confirmation was used up.
	if resp, _ := askOptions(t, srv, c, signInHost, "/account/passkeys/options/add"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("a second passkey on the same confirmation: %d", resp.StatusCode)
	}

	// Signed out, a passkey alone signs in.
	call(t, srv, c, "POST", signInHost, "/sign-out", url.Values{})
	if _, body := call(t, srv, c, "GET", signInHost, "/sign-in", nil); !strings.Contains(body, "Sign in with a passkey") || !strings.Contains(body, "username webauthn") ||
		!strings.Contains(body, "/static/passkey.js") {
		t.Errorf("the sign-in page doesn't offer passkeys")
	}
	signIn := func(key *softKey, origin string) *http.Response {
		_, opts := askOptions(t, srv, c, signInHost, "/sign-in/passkey/options")
		resp, _ := call(t, srv, c, "POST", signInHost, "/sign-in/passkey", url.Values{"next": {"/"}, "remember": {"on"}, passkeyField: {key.get(t, opts, origin)}})
		return resp
	}
	if resp := signIn(phone, origin); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("signing in with a passkey: %d", resp.StatusCode)
	}
	if _, ok := c.Jar.(*plainJar).cookies[signInCookie]; !ok {
		t.Fatal("not signed in after the passkey")
	}

	// Refused: a key Roostack doesn't know, one used at an app's address
	// (the browser would let photos.<domain> ask), and a copy whose
	// counter went backwards.
	call(t, srv, c, "POST", signInHost, "/sign-out", url.Values{})
	stranger := newSoftKey(t)
	stranger.handle = phone.handle
	if resp := signIn(stranger, origin); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unknown passkey: %d", resp.StatusCode)
	}
	if resp := signIn(phone, "https://photos.lab.example.com"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a passkey used at an app's address: %d", resp.StatusCode)
	}
	copied := *phone
	copied.counter = 0
	if resp := signIn(&copied, origin); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a copied passkey: %d", resp.StatusCode)
	}
	entries, _ := h.store.RecentAudit(ctx, 30)
	var got []string
	for _, e := range entries {
		got = append(got, e.Action+": "+e.Detail)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{"passkey added: Sam's phone", "signed in: on the sign-in page for services with a passkey", "passkey refused: Sam's phone", "failed passkey"} {
		if !strings.Contains(all, want) {
			t.Errorf("audit log missing %q:\n%s", want, all)
		}
	}

	// An Owner whose only way to meet two-factor is one passkey can't
	// remove it; with an authenticator app as well, they can.
	if resp := signIn(phone, origin); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("signing in again: %d", resp.StatusCode)
	}
	pk, _ := h.store.UserPasskeys(ctx, id)
	h.store.SetTOTP(ctx, id, "", 0)
	if resp, body := call(t, srv, c, "POST", signInHost, "/account/passkeys/"+itoa(pk[0].ID)+"/remove", url.Values{}); resp.StatusCode != http.StatusConflict ||
		!strings.Contains(body, "only way") {
		t.Errorf("removing the only way in: %d", resp.StatusCode)
	}
	h.store.SetTOTP(ctx, id, secret, 0)
	if resp, _ := call(t, srv, c, "POST", signInHost, "/account/passkeys/"+itoa(pk[0].ID)+"/remove", url.Values{}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("removing: %d", resp.StatusCode)
	}
	if u, _ := h.store.UserByID(ctx, id); u.Passkeys != 0 {
		t.Errorf("passkeys after removing: %d", u.Passkeys)
	}
}

// A passkey made for the lab's domain before it changed won't work, and
// Security says so; a reset of two-factor removes passkeys too.
func TestPasskeysDomainAndReset(t *testing.T) {
	h := newHarness(t, "")
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	h.server.Domain = "lab.example.com"
	ctx := context.Background()
	id := addPerson(t, h.store, "mum", "member")
	handle, _ := h.store.PasskeyHandle(ctx, id)
	if len(handle) != 32 {
		t.Fatalf("handle %x", handle)
	}
	if again, _ := h.store.PasskeyHandle(ctx, id); string(again) != string(handle) {
		t.Error("the handle changed")
	}
	h.store.AddPasskey(ctx, storePasskey(id, "old.example.com", "Firefox on Linux"))
	call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"mum"}, "password": {"a long password"}, "next": {"/"}})
	if _, body := call(t, srv, c, "GET", signInHost, "/account/security", nil); !strings.Contains(body, "Made for old.example.com") {
		t.Errorf("an old domain's passkey isn't explained:\n%s", body)
	}
	// It isn't offered to the device either.
	if resp, _ := askOptions(t, srv, c, signInHost, "/account/passkeys/options/confirm"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("confirming with only an old domain's passkey: %d", resp.StatusCode)
	}
	n, err := h.store.ResetTOTP(ctx, id)
	if err != nil || n != 1 {
		t.Errorf("reset: %d, %v", n, err)
	}
	if u, _ := h.store.UserByID(ctx, id); u.Passkeys != 0 {
		t.Errorf("passkeys after a reset: %d", u.Passkeys)
	}
}

// Through the SSH tunnel (no domain to make passkeys for), nothing offers
// them and the options are refused.
func TestPasskeysWhereTheyCantWork(t *testing.T) {
	h := newHarness(t, "")
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	if resp, _ := askOptions(t, srv, c, signInHost, "/sign-in/passkey/options"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("options without a domain: %d", resp.StatusCode)
	}
	h.server.Domain = "lab.example.com"
	if _, body := call(t, h.srv, c, "GET", "localhost:18481", "/login", nil); strings.Contains(body, "with a passkey") || strings.Contains(body, "passkey.js") {
		t.Error("the login page through the tunnel offers a passkey")
	}
}

// addPhone adds a passkey for whoever is signed in on c at my., who has
// password "a long password" and no authenticator app.
func addPhone(t *testing.T, srv *httptest.Server, c *http.Client) *softKey {
	t.Helper()
	if resp, _ := call(t, srv, c, "POST", signInHost, "/account/passkeys/confirm", url.Values{"current": {"a long password"}}); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("confirming: %d", resp.StatusCode)
	}
	phone := newSoftKey(t)
	_, opts := askOptions(t, srv, c, signInHost, "/account/passkeys/options/add")
	resp, _ := call(t, srv, c, "POST", signInHost, "/account/passkeys/add", url.Values{"name": {"Phone"}, passkeyField: {phone.create(t, opts, "https://"+signInHost)}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("adding: %d", resp.StatusCode)
	}
	return phone
}

// For someone who must use two-factor, a password asks for their passkey
// next, at my. and on Roostack's own login at its address; there, a
// passkey alone signs in too.
func TestPasskeySecondStep(t *testing.T) {
	h := newHarness(t, "")
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	h.server.Domain = "lab.example.com"
	ctx := context.Background()
	addPerson(t, h.store, "ali", "member")
	// Ali signs in and adds a passkey, then the Owner asks everyone for
	// two-factor (Settings → Single sign-on).
	call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"ali"}, "password": {"a long password"}, "next": {"/"}})
	phone := addPhone(t, srv, c)
	call(t, srv, c, "POST", signInHost, "/sign-out", url.Values{})
	h.store.SetSetting(ctx, settingSignInTwoFactor, "everyone")

	// my.: the password, then the passkey.
	_, body := call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"ali"}, "password": {"a long password"}, "next": {"/"}})
	if !strings.Contains(body, "Use your passkey") || strings.Contains(body, `name="code"`) {
		t.Fatalf("the step after a password:\n%s", body)
	}
	_, opts := askOptions(t, srv, c, signInHost, "/sign-in/passkey/options/second")
	if !strings.Contains(opts, "allowCredentials") {
		t.Errorf("second-step options name no passkeys: %s", opts)
	}
	resp, _ := call(t, srv, c, "POST", signInHost, "/sign-in/code", url.Values{"next": {"/"}, passkeyField: {phone.get(t, opts, "https://"+signInHost)}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the passkey after a password: %d", resp.StatusCode)
	}

	// Roostack's own login at its address: a passkey alone. (A Member is
	// sent on to Your account.)
	pages := &http.Client{Jar: &plainJar{}, CheckRedirect: c.CheckRedirect}
	if _, body := call(t, h.srv, pages, "GET", pagesHost, "/login?here", nil); !strings.Contains(body, "Log in with a passkey") {
		t.Fatalf("Roostack's own login doesn't offer passkeys:\n%s", body)
	}
	_, opts = askOptions(t, h.srv, pages, pagesHost, "/login/passkey/options")
	resp, _ = call(t, h.srv, pages, "POST", pagesHost, "/login/passkey", url.Values{"next": {"/"}, passkeyField: {phone.get(t, opts, "https://"+pagesHost)}})
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("a passkey at Roostack's own login: %d", resp.StatusCode)
	}
	if _, ok := pages.Jar.(*plainJar).cookies[sessionCookie]; !ok {
		t.Error("no session after a passkey at Roostack's own login")
	}
}

// storePasskey is a passkey saved for a domain, without a usable key.
func storePasskey(userID int64, rpID, name string) store.Passkey {
	cid := make([]byte, 16)
	rand.Read(cid)
	return store.Passkey{UserID: userID, CredentialID: cid, Credential: "{}", RPID: rpID, Name: name}
}
