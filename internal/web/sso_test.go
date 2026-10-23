package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/store"
)

const signInHost = "login.lab.example.com"

// ssoPackages adds the fixtures with single sign-on to the fake manager.
type ssoPackages struct{ *fakeManager }

func (m ssoPackages) Package(name string) (*pkgspec.Package, error) {
	if name == "mealie" || name == "stirling-pdf" {
		return pkgspec.Load(pkgtest.File(name))
	}
	if name == "photos" {
		return pkgspec.Parse([]byte(photosPackage))
	}
	return m.fakeManager.Package(name)
}
func (m ssoPackages) ServicePackage(name string) (*pkgspec.Package, error) { return m.Package(name) }
func (m ssoPackages) PackageIcon(name string) ([]byte, error) {
	if name == "mealie" || name == "stirling-pdf" {
		return os.ReadFile(filepath.Join(filepath.Dir(pkgtest.File(name)), "icon.png"))
	}
	return m.fakeManager.PackageIcon(name)
}

// withSignIn gives a harness's server the sign-in page, and returns a
// client of its listener that doesn't follow redirects.
func withSignIn(t *testing.T, h *harness, fs *fakeSSO) (*httptest.Server, *http.Client) {
	t.Helper()
	key, err := sso.LoadKey(filepath.Join(t.TempDir(), "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	s := h.server
	s.SSO, s.SignInHost, s.SSODir, s.UIHost = fs, signInHost, t.TempDir(), "roost.lab.example.com"
	s.SignInServed = func(context.Context) bool { return true } // Caddy serves it
	s.SignIn = &sso.Provider{Store: h.store, Key: key, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Issuer: func() string { return "https://" + signInHost }, Clients: fs.SSOClients,
		ClientSecret: func(context.Context, string) ([]byte, error) { return []byte("app-secret"), nil }}
	if s.Manager != nil {
		if fm, ok := s.Manager.(*fakeManager); ok {
			s.Manager = ssoPackages{fm}
		}
	}
	srv := httptest.NewServer(s.SignInHandler())
	t.Cleanup(srv.Close)
	return srv, &http.Client{Jar: &plainJar{}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// plainJar keeps cookies by name, whatever the host and Secure: the
// sign-in listener is asked over plain HTTP here, as Caddy asks it, with
// X-Forwarded-Proto saying the browser's side was HTTPS.
type plainJar struct{ cookies map[string]*http.Cookie }

func (j *plainJar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	if j.cookies == nil {
		j.cookies = map[string]*http.Cookie{}
	}
	for _, c := range cs {
		if c.MaxAge < 0 {
			delete(j.cookies, c.Name)
		} else {
			j.cookies[c.Name] = c
		}
	}
}

func (j *plainJar) Cookies(*url.URL) []*http.Cookie {
	var out []*http.Cookie
	for _, c := range j.cookies {
		out = append(out, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	return out
}

// call asks the sign-in listener as if at host.
func call(t *testing.T, srv *httptest.Server, c *http.Client, method, host, path string, form url.Values) (*http.Response, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, srv.URL+path, body)
	req.Host = host
	req.Header.Set("X-Forwarded-Proto", "https")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://"+host)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func addPerson(t *testing.T, st *store.Store, name, role string) int64 {
	t.Helper()
	hash, _ := authkit.HashPassword("a long password")
	id, err := st.CreateUser(context.Background(), store.User{Username: name, PasswordHash: hash, Role: role, Email: name + "@smith.family"})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// An app sends someone to sign in: the sign-in page in the lab's look,
// then back to the app with a code; someone without access is told who to
// ask instead.
func TestSignInOIDC(t *testing.T) {
	h := newHarness(t, "")
	h.server.LabName = "Smiths' lab"
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	addPerson(t, h.store, "mum", "member")

	authorize := sso.PathAuthorize + "?" + url.Values{"client_id": {"mealie"}, "redirect_uri": {"https://recipes.lab.example.com/login"},
		"response_type": {"code"}, "scope": {"openid email"}, "state": {"xyz"}}.Encode()
	resp, _ := call(t, srv, c, "GET", signInHost, authorize, nil)
	next := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(next, "/sign-in?next=") {
		t.Fatalf("not signed in: %d %q", resp.StatusCode, next)
	}
	_, body := call(t, srv, c, "GET", signInHost, next, nil)
	for _, want := range []string{"Smiths&#39; lab", "to continue to", "Mealie", `name="remember" checked`, `class="brand-mark`} {
		if !strings.Contains(body, want) {
			t.Errorf("sign-in page missing %q", want)
		}
	}
	u, _ := url.Parse(next)
	if resp, body := call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"mum"}, "password": {"wrong"}, "next": {u.Query().Get("next")}}); resp.StatusCode != 401 ||
		!strings.Contains(body, "Wrong username or password") {
		t.Errorf("wrong password: %d", resp.StatusCode)
	}
	// By email, too.
	resp, _ = call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"mum@smith.family"}, "password": {"a long password"},
		"next": {u.Query().Get("next")}, "remember": {"on"}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != authorize {
		t.Fatalf("signed in: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == signInCookie {
			session = ck
		}
	}
	if session == nil || !session.HttpOnly || !session.Secure || session.Domain != "" || session.MaxAge != int(signInRemember.Seconds()) {
		t.Fatalf("sign-in cookie %+v", session)
	}
	resp, _ = call(t, srv, c, "GET", signInHost, authorize, nil)
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "https://recipes.lab.example.com/login?code=") ||
		!strings.Contains(loc, "state=xyz") {
		t.Fatalf("back to the app: %d %q", resp.StatusCode, loc)
	}

	// The sign-in page itself lists what she can use.
	if _, body := call(t, srv, c, "GET", signInHost, "/", nil); !strings.Contains(body, "Hello, mum") || !strings.Contains(body, "recipes.lab.example.com") {
		t.Errorf("home:\n%s", body)
	}

	// Without access: who to ask.
	h.store.SetServiceAccess(context.Background(), "mealie", store.Access{Chosen: true})
	resp, body = call(t, srv, c, "GET", signInHost, authorize, nil)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "You can't use Mealie yet") {
		t.Errorf("no access: %d", resp.StatusCode)
	}
	if _, body := call(t, srv, c, "GET", signInHost, "/", nil); strings.Contains(body, "recipes.lab.example.com") {
		t.Error("home lists a service she can't use")
	}

	// Signing out.
	resp, body = call(t, srv, c, "POST", signInHost, sso.PathSignOut, url.Values{})
	if resp.StatusCode != 200 || !strings.Contains(body, "You've signed out") {
		t.Errorf("sign out: %d", resp.StatusCode)
	}
	if resp, _ := call(t, srv, c, "GET", signInHost, authorize, nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("after signing out: %d", resp.StatusCode)
	}
}

// An app may post its request (OIDC Core §3.1.2.1), from its own page, and
// ask for the password again (prompt=login, max_age): the same sign-in
// goes on, with a new time. Someone else signing in on the browser ends
// the last person's sign-in.
func TestSignInFresh(t *testing.T) {
	h := newHarness(t, "")
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	ctx := context.Background()
	addPerson(t, h.store, "mum", "member")
	addPerson(t, h.store, "dad", "member")
	request := url.Values{"client_id": {"mealie"}, "redirect_uri": {"https://recipes.lab.example.com/login"},
		"response_type": {"code"}, "scope": {"openid"}, "state": {"xyz"}}
	signIn := func(who, next string) *http.Response {
		t.Helper()
		resp, _ := call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {who}, "password": {"a long password"}, "next": {next}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("signing in %s: %d", who, resp.StatusCode)
		}
		return resp
	}
	signIn("mum", "/")
	first := c.Jar.(*plainJar).cookies[signInCookie].Value
	hash := authkit.HashToken(first)
	h.store.SignedInAgain(ctx, hash, time.Now().Add(-time.Hour))

	// Posted from the app's page, which isn't the sign-in page's origin.
	req, _ := http.NewRequest("POST", srv.URL+sso.PathAuthorize, strings.NewReader(request.Encode()))
	req.Host = signInHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://recipes.lab.example.com")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.Contains(loc, "code=") {
		t.Errorf("posted request: %d %q", resp.StatusCode, loc)
	}

	for _, ask := range [][2]string{{"prompt", "login"}, {"max_age", "60"}} {
		q := url.Values{ask[0]: {ask[1]}}
		for k, v := range request {
			q[k] = v
		}
		resp, _ := call(t, srv, c, "GET", signInHost, sso.PathAuthorize+"?"+q.Encode(), nil)
		loc, _ := url.Parse(resp.Header.Get("Location"))
		next := loc.Query().Get("next")
		if resp.StatusCode != http.StatusSeeOther || loc.Path != "/sign-in" || strings.Contains(next, ask[0]) || !strings.Contains(next, "client_id=mealie") {
			t.Fatalf("%s=%s: %d %q", ask[0], ask[1], resp.StatusCode, loc)
		}
		// prompt=none can't be answered then.
		q.Set("prompt", "none")
		if ask[0] == "max_age" {
			resp, _ := call(t, srv, c, "GET", signInHost, sso.PathAuthorize+"?"+q.Encode(), nil)
			if loc := resp.Header.Get("Location"); !strings.Contains(loc, "error=login_required") {
				t.Errorf("max_age with prompt=none: %q", loc)
			}
		}
		resp = signIn("mum", next)
		if len(resp.Cookies()) != 0 || c.Jar.(*plainJar).cookies[signInCookie].Value != first {
			t.Errorf("%s: signing in again made a new sign-in", ask[0])
		}
		si, err := h.store.SignInSession(ctx, hash)
		if err != nil || time.Since(si.At) > time.Minute {
			t.Errorf("%s: sign-in time %v, %v", ask[0], si, err)
		}
		resp, _ = call(t, srv, c, "GET", signInHost, resp.Header.Get("Location"), nil)
		if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusFound || !strings.Contains(loc, "code=") {
			t.Errorf("%s: back to the app: %d %q", ask[0], resp.StatusCode, loc)
		}
		h.store.SignedInAgain(ctx, hash, time.Now().Add(-time.Hour))
	}

	signIn("dad", "/")
	if _, err := h.store.SignInSession(ctx, hash); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("mum's sign-in after dad signed in on her browser: %v", err)
	}
}

// Two-factor: someone with it enters a code; an Owner without it is sent
// to Roostack's pages first; a temporary password too.
func TestSignInTwoFactor(t *testing.T) {
	h := newHarness(t, "")
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	ctx := context.Background()
	id := addPerson(t, h.store, "dad", "member")
	secret, _ := authkit.NewTOTPSecret()
	h.store.SetTOTP(ctx, id, secret, 0)
	resp, body := call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"dad"}, "password": {"a long password"}})
	if resp.StatusCode != 200 || !strings.Contains(body, "One more step") {
		t.Fatalf("code step: %d", resp.StatusCode)
	}
	if resp, body := call(t, srv, c, "POST", signInHost, "/sign-in/code", url.Values{"code": {wrongCode(secret)}}); resp.StatusCode != 401 ||
		!strings.Contains(body, "That code didn") {
		t.Errorf("wrong code: %d", resp.StatusCode)
	}
	code, _ := authkit.TOTPCode(secret, nowFunc())
	if resp, _ := call(t, srv, c, "POST", signInHost, "/sign-in/code", url.Values{"code": {code}}); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("right code: %d", resp.StatusCode)
	}

	addPerson(t, h.store, "sam", "owner")
	if resp, body := call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"sam"}, "password": {"a long password"}}); resp.StatusCode != 403 ||
		!strings.Contains(body, "needs two-factor authentication") || !strings.Contains(body, "https://roost.lab.example.com") {
		t.Errorf("Owner without two-factor: %d", resp.StatusCode)
	}
	jo := addPerson(t, h.store, "jo", "member")
	u, _ := h.store.UserByID(ctx, jo)
	h.store.SetPassword(ctx, u.Username, u.PasswordHash, true)
	if resp, body := call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"jo"}, "password": {"a long password"}}); resp.StatusCode != 403 ||
		!strings.Contains(body, "temporary password") {
		t.Errorf("temporary password: %d", resp.StatusCode)
	}
	// Everyone needs it, if the Owner chose so.
	h.store.SetSetting(ctx, settingSignInTwoFactor, "everyone")
	addPerson(t, h.store, "ella", "member")
	if resp, _ := call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"ella"}, "password": {"a long password"}}); resp.StatusCode != 403 {
		t.Errorf("everyone needs two-factor: %d", resp.StatusCode)
	}
}

// The sign-in listener answers only its own host, Caddy's checks and the
// walled services' callbacks; forms must come from its own page.
func TestSignInHosts(t *testing.T) {
	h := newHarness(t, "")
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"stirling-pdf": true}})
	if resp, _ := call(t, srv, c, "GET", "roost.lab.example.com", "/sign-in", nil); resp.StatusCode != 404 {
		t.Errorf("Roostack's own host: %d", resp.StatusCode)
	}
	if resp, _ := call(t, srv, c, "GET", "pdf.lab.example.com", "/sign-in", nil); resp.StatusCode != 404 {
		t.Errorf("a service's host: %d", resp.StatusCode)
	}
	// Caddy's check comes with the service's own host.
	req, _ := http.NewRequest("GET", srv.URL+sso.PathCheck+"?service=stirling-pdf", nil)
	req.Host = "pdf.lab.example.com"
	req.Header.Set("X-Forwarded-Host", "pdf.lab.example.com")
	req.Header.Set("X-Forwarded-Uri", "/")
	req.Header.Set("X-Forwarded-Method", "GET")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 302 || !strings.HasPrefix(resp.Header.Get("Location"), "https://"+signInHost+sso.PathStart) {
		t.Errorf("check: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, _ := call(t, srv, c, "GET", "pdf.lab.example.com", sso.PathCallback+"?code=nope", nil); resp.StatusCode != 400 {
		t.Errorf("a wrong callback: %d", resp.StatusCode)
	}
	// A form from elsewhere.
	req, _ = http.NewRequest("POST", srv.URL+"/sign-in", strings.NewReader("username=x&password=y"))
	req.Host = signInHost
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	resp, _ = c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("cross-site sign-in: %d", resp.StatusCode)
	}
	resp, body := call(t, srv, c, "GET", signInHost, "/sign-in", nil)
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP %q", csp)
	}
	if strings.Contains(body, "<script") {
		t.Error("the sign-in page has a script")
	}
	// Where to go after signing in stays on the sign-in page.
	for _, next := range []string{"https://evil.example/", "//evil.example/", "/authorize?\r\nx"} {
		if got := safeNext(next); got != "/" {
			t.Errorf("safeNext(%q) = %q", next, got)
		}
	}
}

// The card on a service's page: off with what turning it on does, on with
// who can sign in; turning it on saves who chose; Admins can turn it on
// but only Owners choose who.
func TestServiceSSOCard(t *testing.T) {
	h, _ := loggedIn(t, "owner")
	fs := &fakeSSO{on: map[string]bool{}}
	withSignIn(t, h, fs)
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "mealie", Title: "Mealie", Status: "running", Package: "mealie", URLs: []string{"https://recipes.lab.example.com"}}}, nil
	}
	ctx := context.Background()
	family, _ := h.store.CreateGroup(ctx, "Family")
	_, body := h.get("/services/mealie")
	for _, want := range []string{`id="sso"`, "Turn on single sign-on", "Mealie will restart", "Family", `name="who" value="all" checked`,
		"API tokens"} {
		if !strings.Contains(body, want) {
			t.Errorf("off card missing %q", want)
		}
	}
	code, _ := h.post("/services/mealie/sso", url.Values{"sso": {"on"}, "who": {"chosen"}, "group": {itoa64(family)}}, h.srv.URL)
	if code != http.StatusOK && code != http.StatusSeeOther || !fs.on["mealie"] {
		t.Fatalf("turn on: %d %v", code, fs.on)
	}
	if a, _ := h.store.ServiceAccess(ctx, "mealie"); !a.Chosen || len(a.Groups) != 1 || a.Groups[0] != family {
		t.Errorf("access %+v", a)
	}
	_, body = h.get("/services/mealie")
	for _, want := range []string{"status-running\">On", "Who can sign in", "Family", "https://recipes.lab.example.com/login", "OIDC_CLIENT_SECRET",
		"Turn off single sign-on"} {
		if !strings.Contains(body, want) {
			t.Errorf("on card missing %q", want)
		}
	}
	// Who can sign in changes in a dialog, which ?sso=who opens; the choice
	// is a segmented control.
	if !strings.Contains(body, `data-dialog="sso-who-dialog"`) || strings.Contains(body, `aria-labelledby="sso-who-title" open>`) ||
		!strings.Contains(body, `name="who" value="chosen" checked`) {
		t.Error("the who dialog: no button, open, or not the choice")
	}
	if _, body := h.get("/services/mealie?sso=who"); !strings.Contains(body, `aria-labelledby="sso-who-title" open>`) {
		t.Error("?sso=who doesn't open the dialog")
	}
	h.post("/services/mealie/sso", url.Values{"sso": {"off"}}, h.srv.URL)
	if fs.on["mealie"] {
		t.Error("still on")
	}
	// A refusal is shown on the card.
	fs.why = "Single sign-on needs your lab's address first."
	code, body = h.post("/services/mealie/sso", url.Values{"sso": {"on"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "needs your lab&#39;s address first") {
		t.Errorf("refused: %d", code)
	}

	// An Admin turns it on, but doesn't choose who.
	ha, _ := loggedIn(t, "admin")
	fa := &fakeSSO{on: map[string]bool{}}
	withSignIn(t, ha, fa)
	ha.server.Services = h.server.Services
	if _, body := ha.get("/services/mealie"); strings.Contains(body, `name="who"`) {
		t.Error("an Admin sees who can sign in")
	}
	ha.post("/services/mealie/sso", url.Values{"sso": {"on"}, "who": {"chosen"}}, ha.srv.URL)
	if a, _ := ha.store.ServiceAccess(ctx, "mealie"); a.Chosen || !fa.on["mealie"] {
		t.Errorf("an Admin's choice: %+v %v", a, fa.on)
	}
	if code, _ := ha.post("/services/mealie/sso/access", url.Values{"who": {"chosen"}}, ha.srv.URL); code != http.StatusForbidden {
		t.Errorf("an Admin choosing who: %d", code)
	}
}

// An app set by its sso hook (Forgejo): the card says while it runs and
// when it failed, with Try again, and shows its last run in Details; the
// sign-in page tells someone whose app keeps a button once it's off.
func TestServiceSSOHookCard(t *testing.T) {
	h, fm := loggedIn(t, "owner")
	fs := &fakeSSO{on: map[string]bool{"forgejo": true}}
	srv, c := withSignIn(t, h, fs)
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "forgejo", Title: "Forgejo", Status: "running", Package: "forgejo", URLs: []string{"https://git.lab.example.com"}}}, nil
	}
	fm.setup = map[string]manager.HookStatus{"forgejo": {SSO: manager.SSOHookStatus{Running: true, On: true}}}
	if _, body := h.get("/services/forgejo"); !strings.Contains(body, "Connecting Forgejo to the sign-in page") || !strings.Contains(body, `hx-select="#sso-hook"`) {
		t.Error("running: no polling notice")
	}
	fm.setup["forgejo"] = manager.HookStatus{SSO: manager.SSOHookStatus{On: false, Failed: "Forgejo didn't answer"}}
	_, body := h.get("/services/forgejo")
	for _, want := range []string{"Forgejo isn't connected to the sign-in page yet", "Forgejo didn&#39;t answer", `action="/services/forgejo/setup/retry"`} {
		if !strings.Contains(body, want) {
			t.Errorf("failed card missing %q", want)
		}
	}
	fm.setup["forgejo"] = manager.HookStatus{SSO: manager.SSOHookStatus{On: true, Output: "Added single sign-on to Forgejo.", At: time.Now()}}
	if _, body := h.get("/services/forgejo"); !strings.Contains(body, "Added single sign-on to Forgejo.") || strings.Contains(body, "isn't connected") {
		t.Error("done: no last run in Details, or still failed")
	}

	// Off, with Forgejo's button kept: the sign-in page says so.
	fs.on["forgejo"] = false
	q := url.Values{"client_id": {"forgejo"}, "redirect_uri": {"https://git.lab.example.com/user/oauth2/lab.example.com/callback"},
		"response_type": {"code"}, "scope": {"openid"}}
	if _, body := call(t, srv, c, http.MethodGet, signInHost, "/authorize?"+q.Encode(), nil); !strings.Contains(body, "Single sign-on is off for Forgejo") {
		t.Errorf("off: %s", body)
	}
}

// photosPackage is an app set through its own API, like Immich, whose sso
// hook needs a key it makes (SPEC.md §5.2).
const photosPackage = `
schema: roostack/v1alpha1
name: photos
title: Photos
version: 1.0.0-roostack.1
settings:
  - { key: settings_key, title: Roostack's key to Photos' settings, type: secret, from_hook: true }
containers:
  server:
    image: example.org/photos:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
routes:
  web: { container: server, port: 2283, subdomain: photos, exposure: private }
hooks:
  - on: action
    name: connect
    title: Connect Roostack to Photos
    container: server
    inputs: [{ key: password, title: Password, type: secret }]
    secrets: { ADMIN_PASSWORD: inputs.password }
    saves: settings_key
    command: [make-key]
  - on: sso
    container: server
    secrets: { API_KEY: settings_key, CLIENT_SECRET: sso.client_secret }
    command: [set-sign-in]
sso:
  oidc: { route: web, redirect_paths: [/auth/login] }
`

// Without the key its sso hook needs (installed before its package made
// one), the card asks to connect first, linking to the action, which the
// link opens.
func TestServiceSSOConnect(t *testing.T) {
	h, fm := loggedIn(t, "owner")
	withSignIn(t, h, &fakeSSO{on: map[string]bool{}})
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "photos", Title: "Photos", Status: "running", Package: "photos", URLs: []string{"https://photos.lab.example.com"}}}, nil
	}
	fm.setup = map[string]manager.HookStatus{"photos": {SSO: manager.SSOHookStatus{NeedsKey: "connect"}}}
	_, body := h.get("/services/photos")
	if !strings.Contains(body, "First connect Roostack to Photos") || !strings.Contains(body, `href="/services/photos?action=connect#action-connect"`) ||
		strings.Contains(body, "Turn on single sign-on</button>") {
		t.Error("no Connect prompt, or the switch is offered without the key")
	}
	if _, body := h.get("/services/photos?action=connect"); !strings.Contains(body, `id="action-connect" open`) {
		t.Error("?action=connect doesn't open the action")
	}
}

func itoa64(i int64) string { return strconv.FormatInt(i, 10) }

// People: groups, a person's details, the services a group can use, and
// the sign-in page's look.
func TestPeopleSSO(t *testing.T) {
	h, _ := loggedIn(t, "owner")
	fs := &fakeSSO{on: map[string]bool{"mealie": true}}
	withSignIn(t, h, fs)
	ctx := context.Background()
	mum := addPerson(t, h.store, "mum", "member")
	h.store.SetServiceAccess(ctx, "mealie", store.Access{Chosen: true})

	code, body := h.post("/settings/people/groups", url.Values{"name": {"Family"}}, h.srv.URL)
	if code != 200 {
		t.Fatalf("add group: %d", code)
	}
	// A new group's dialog opens, to choose its people and services.
	if !strings.Contains(body, `aria-labelledby="group-1-title" open>`) {
		t.Error("the new group's dialog isn't open")
	}
	if _, body := h.get("/settings/people?group=1"); !strings.Contains(body, `aria-labelledby="group-1-title" open>`) {
		t.Error("?group= doesn't open its dialog")
	}
	if _, body := h.get("/settings/people"); strings.Contains(body, `aria-labelledby="group-1-title" open>`) || !strings.Contains(body, `data-dialog="group-1"`) {
		t.Error("the group's dialog is open, or has no button")
	}
	if code, body := h.post("/settings/people/groups", url.Values{"name": {"family"}}, h.srv.URL); code != 400 || !strings.Contains(body, "already a group") {
		t.Errorf("a second Family: %d", code)
	}
	groups, _ := h.store.Groups(ctx)
	g := groups[0]
	h.post("/settings/people/groups/"+itoa64(g.ID), url.Values{"name": {"Family"}, "member": {itoa64(mum)}, "service": {"mealie"}}, h.srv.URL)
	if ok, _ := h.store.CanUse(ctx, "mealie", mum); !ok {
		t.Error("mum's group was given Mealie")
	}
	h.post("/users/"+itoa64(mum)+"/details", url.Values{"name": {"Margaret  Smith"}, "email": {"margaret@smith.family"}}, h.srv.URL)
	u, _ := h.store.UserByID(ctx, mum)
	if u.Name != "Margaret Smith" || u.Email != "margaret@smith.family" {
		t.Errorf("details %+v", u)
	}
	if groups, _ := h.store.Groups(ctx); len(groups[0].Members) != 0 {
		t.Error("details without groups ticked takes her out of them")
	}
	if code, body := h.post("/users/"+itoa64(mum)+"/details", url.Values{"email": {"not an address"}}, h.srv.URL); code != 400 || !strings.Contains(body, "Enter an email address, such as sam@example.com.") {
		t.Errorf("bad email: %d", code)
	}
	_, body = h.get("/settings/people")
	for _, want := range []string{`id="groups"`, `id="sign-in-page"`, "login.lab.example.com", `data-palette="nord"`, "Margaret Smith",
		"Ask everyone for two-factor"} {
		if !strings.Contains(body, want) {
			t.Errorf("People missing %q", want)
		}
	}
	// The sign-in page's two choices.
	h.post("/settings/signing-in", url.Values{"idle": {"30"}, "sso": {"1"}, "signin_two_factor": {"on"}}, h.srv.URL)
	if v, _ := h.store.Setting(ctx, settingSignInTwoFactor); v != "everyone" {
		t.Errorf("two-factor %q", v)
	}
	if v, _ := h.store.Setting(ctx, settingSignInRemember); v != "off" {
		t.Errorf("remember %q", v)
	}
}

// Logos: PNGs and clean SVGs only, of the right shape.
func TestCheckLogo(t *testing.T) {
	icon, _ := os.ReadFile(filepath.Join(filepath.Dir(pkgtest.File("mealie")), "icon.png"))
	if kind, err := checkLogo(icon, false); kind != "png" || err != nil {
		t.Errorf("a square PNG as a mark: %q %v", kind, err)
	}
	if _, err := checkLogo(icon, true); err == nil {
		t.Error("a square PNG as a wide logo")
	}
	for svg, ok := range map[string]bool{
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><rect width="10" height="10"/></svg>`: true,
		`<?xml version="1.0"?><svg><script>alert(1)</script></svg>`:                                        false,
		`<svg onload="alert(1)"></svg>`:                                                                    false,
		`<svg><a href="javascript:alert(1)">x</a></svg>`:                                                   false,
		`<svg><foreignObject><iframe/></foreignObject></svg>`:                                              false,
		`<svg><text>contone</text></svg>`:                                                                  true,
		`hello`:                                                                                            false,
	} {
		if _, err := checkLogo([]byte(svg), false); (err == nil) != ok {
			t.Errorf("%s: %v", svg, err)
		}
	}
	if _, err := checkLogo(make([]byte, maxLogo+1), false); err == nil {
		t.Error("too big")
	}
}

// Changing someone is a dialog: ?person= opens it, and refused details
// open it again with the mistake.
func TestPersonDialog(t *testing.T) {
	h, _ := loggedIn(t, "owner")
	mum := addPerson(t, h.store, "mum", "member")
	open := `id="person-` + itoa64(mum) + `" aria-labelledby="person-` + itoa64(mum) + `-title" open>`
	if _, body := h.get("/settings/people"); strings.Contains(body, open) || !strings.Contains(body, `data-dialog="person-`+itoa64(mum)+`"`) {
		t.Error("mum's dialog is open, or has no button")
	}
	if _, body := h.get("/settings/people?person=" + itoa64(mum)); !strings.Contains(body, open) {
		t.Error("?person= doesn't open the dialog")
	}
	if code, body := h.post("/users/"+itoa64(mum)+"/details", url.Values{"email": {"not an address"}}, h.srv.URL); code != 400 || !strings.Contains(body, open) {
		t.Errorf("a refused email: %d, or the dialog isn't open", code)
	}
}
