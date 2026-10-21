package web

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/store"
)

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
	store  *store.Store
	server *Server
	setup  bool // OnSetupComplete called
}

var nowFunc = time.Now

// wrongCode is a two-factor code that isn't valid for secret now, nor in
// the windows either side ("000000" is a real code about once in a
// million runs, which made tests flaky).
func wrongCode(secret string) string {
	valid := map[string]bool{}
	for step := -3; step <= 3; step++ {
		c, _ := authkit.TOTPCode(secret, nowFunc().Add(time.Duration(step)*30*time.Second))
		valid[c] = true
	}
	for _, c := range []string{"000000", "111111", "222222", "333333", "444444", "555555", "666666", "777777"} {
		if !valid[c] {
			return c
		}
	}
	panic("no wrong code")
}

// userSecret is a test user's two-factor secret.
func userSecret(h *harness, username string) string {
	u, _ := h.store.UserByUsername(context.Background(), username)
	return u.TOTPSecret
}

func newHarness(t *testing.T, setupToken string) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	services := func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "vaultwarden", Title: "Vaultwarden", Status: "running",
			URLs: []string{"https://passwords.lab.example.com"}}}, nil
	}
	s, err := NewServer(st, services, setupToken, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, store: st, server: s}
	s.OnSetupComplete = func() { h.setup = true }
	h.srv = httptest.NewServer(s.Handler())
	t.Cleanup(h.srv.Close)
	jar, _ := cookiejar.New(nil)
	h.client = &http.Client{Jar: jar}
	return h
}

func (h *harness) get(path string) (int, string) {
	h.t.Helper()
	resp, err := h.client.Get(h.srv.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (h *harness) post(path string, form url.Values, origin string) (int, string) {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestFirstRunNamesTheLab(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	fs := &fakeSetup{}
	h.server.Setup = fs
	form := url.Values{"token": {"the-setup-code"}, "lab_name": {"The Smiths' lab"}, "username": {"tyris"}, "email": {"tyris@example.com"},
		"password": {"a long password"}, "confirm": {"a long password"}}
	h.post("/setup", form, h.srv.URL)
	if fs.labName != "The Smiths' lab" || fs.restarted || h.server.LabName != "The Smiths' lab" {
		t.Errorf("first run: %q restart=%v %q", fs.labName, fs.restarted, h.server.LabName)
	}
}

func TestFirstRunSetup(t *testing.T) {
	h := newHarness(t, "the-setup-code")

	// Everything redirects to setup until an Owner exists.
	if _, body := h.get("/"); !strings.Contains(body, "Welcome to Roostack") {
		t.Fatal("expected the setup page")
	}

	// The setup code comes first; the choices only after it.
	if _, body := h.get("/setup"); !strings.Contains(body, `action="/setup/code"`) || strings.Contains(body, "this server for?") {
		t.Fatal("the setup code first")
	}
	if code, body := h.post("/setup/code", url.Values{"token": {"wrong"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "setup code isn&#39;t right") {
		t.Fatalf("wrong code: %d", code)
	}
	form := url.Values{"token": {"wrong"}, "username": {"tyris"},
		"password": {"a long password"}, "confirm": {"a long password"}}
	if h.post("/setup", form, h.srv.URL); func() bool { n, _ := h.store.CountUsers(context.Background()); return n != 0 }() {
		t.Fatal("no user may be created with a wrong token")
	}
	h.post("/setup/code", url.Values{"token": {"the-setup-code"}}, h.srv.URL)
	if _, body := h.get("/setup"); !strings.Contains(body, "this server for?") || strings.Contains(body, `value="new" x-model="choice" checked`) {
		t.Fatal("then the choices, nothing chosen")
	}

	form.Set("token", "the-setup-code")
	// The Owner gives an email address (ownerNeedsEmail).
	for _, bad := range []string{"", "not an address", "Tyris <tyris@example.com>"} {
		form.Set("email", bad)
		if code, body := h.post("/setup", form, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "Enter an email address") {
			t.Errorf("email %q: %d, want it refused", bad, code)
		}
	}
	form.Set("email", "tyris@example.com")
	form.Set("confirm", "different password")
	if _, body := h.post("/setup", form, h.srv.URL); !strings.Contains(body, "don&#39;t match") {
		t.Error("mismatched passwords should be rejected")
	}

	form.Set("confirm", "a long password")
	code, body := h.post("/setup", form, h.srv.URL)
	// Owners must turn on 2FA before anything else.
	if code != http.StatusOK || !strings.Contains(body, "need to turn on two-factor") {
		t.Fatalf("after setup expected 2FA enrollment, got %d", code)
	}
	if _, body := h.get("/"); !strings.Contains(body, "need to turn on two-factor") {
		t.Fatal("the dashboard must be blocked until the Owner enrolls")
	}
	if !h.setup {
		t.Error("OnSetupComplete not called")
	}
	u, err := h.store.UserByUsername(context.Background(), "tyris")
	if err != nil || u.Role != auth.RoleOwner || u.Email != "tyris@example.com" {
		t.Errorf("owner not created: %+v %v", u, err)
	}

	secret, enrolledAt := h.enrollTOTP(body)
	if _, body := h.get("/"); !strings.Contains(body, "Vaultwarden") {
		t.Fatal("after enrolling, the Owner should see the dashboard")
	}

	// Logging in again needs the password and a code; a used code is refused.
	h.post("/logout", nil, h.srv.URL)
	_, body = h.post("/login", url.Values{"username": {"tyris"}, "password": {"a long password"}}, h.srv.URL)
	if !strings.Contains(body, "Enter your code") {
		t.Fatal("expected the 2FA step")
	}
	used, _ := authkit.TOTPCode(secret, enrolledAt)
	if code, _ := h.post("/login/totp", url.Values{"code": {used}}, h.srv.URL); code != http.StatusUnauthorized {
		t.Errorf("reusing the enrollment code: status %d, want 401", code)
	}
	next, _ := authkit.TOTPCode(secret, enrolledAt.Add(30*time.Second))
	if _, body := h.post("/login/totp", url.Values{"code": {next}}, h.srv.URL); !strings.Contains(body, "<title>Home · Roostack</title>") {
		t.Error("a fresh code should complete the login")
	}

	// Setup can't be repeated.
	if _, body := h.get("/setup"); strings.Contains(body, "Welcome to Roostack") {
		t.Error("setup page must not be available after setup")
	}
}

// enrollTOTP reads the key from the 2FA page and submits a valid code. It
// returns the secret and the time the code was generated for.
func (h *harness) enrollTOTP(page string) (string, time.Time) {
	h.t.Helper()
	secret := totpSecretOf(h.t, page)
	now := time.Now()
	code, err := authkit.TOTPCode(secret, now)
	if err != nil {
		h.t.Fatal(err)
	}
	if status, _ := h.post("/account/2fa", url.Values{"secret": {secret}, "code": {wrongCode(secret)}}, h.srv.URL); status != http.StatusBadRequest {
		h.t.Errorf("a wrong enrollment code: status %d, want 400", status)
	}
	if status, _ := h.post("/account/2fa", url.Values{"secret": {secret}, "code": {code}}, h.srv.URL); status != http.StatusOK {
		h.t.Fatalf("enrollment failed: %d", status)
	}
	return secret, now
}

// totpSecretOf reads the key being set up from a two-factor page.
func totpSecretOf(t *testing.T, page string) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, `name="secret" value="`)
	if !ok {
		t.Fatal("no secret on the 2FA page")
	}
	secret, _, _ := strings.Cut(rest, `"`)
	return secret
}

// Wrong two-factor codes count against the account, however many logins
// they're spread over (SPEC.md §6.10): the fifth locks codes, even the
// right one, and the password step says so; after the lock, the right code
// works and starts the count again.
func TestTooManyCodes(t *testing.T) {
	h := newHarness(t, "")
	ctx := context.Background()
	hash, _ := authkit.HashPassword("a long password")
	id, _ := h.store.CreateUser(ctx, store.User{Username: "tyris", PasswordHash: hash, Role: auth.RoleOwner, Email: "tyris@example.com"})
	secret, _ := authkit.NewTOTPSecret()
	h.store.SetTOTP(ctx, id, secret, 0)
	login := func() (int, string) {
		return h.post("/login", url.Values{"username": {"tyris"}, "password": {"a long password"}}, h.srv.URL)
	}

	var status int
	var body string
	for _, tries := range []int{3, 2} { // a new login doesn't start again
		login()
		for range tries {
			status, body = h.post("/login/totp", url.Values{"code": {wrongCode(secret)}}, h.srv.URL)
		}
	}
	if status != http.StatusTooManyRequests || !strings.Contains(body, "no code will work until") {
		t.Fatalf("fifth wrong code: %d, want the lock", status)
	}
	if status, body := login(); status != http.StatusTooManyRequests || strings.Contains(body, "Enter your code") {
		t.Errorf("logging in while locked: %d, want the lock message instead of the code step", status)
	}
	logs, _ := h.store.RecentAudit(ctx, 10)
	if len(logs) == 0 || logs[0].Action != "2fa locked" {
		t.Errorf("the lock isn't in the audit log: %+v", logs)
	}

	// The lock is over: five wrong codes, the last 15 minutes ago.
	h.store.ResetTOTP(ctx, id)
	h.store.SetTOTP(ctx, id, secret, 0)
	for range authkit.TOTPFailuresPerLock {
		h.store.AddTOTPFailure(ctx, id, time.Now().Add(-15*time.Minute))
	}
	login()
	good, _ := authkit.TOTPCode(secret, time.Now())
	if _, body := h.post("/login/totp", url.Values{"code": {good}}, h.srv.URL); !strings.Contains(body, "<title>Home · Roostack</title>") {
		t.Fatal("the right code should work once the lock is over")
	}
	if u, _ := h.store.UserByID(ctx, id); u.TOTPFailures != 0 {
		t.Errorf("a right code should start the count again: %d", u.TOTPFailures)
	}
}

func TestLoginLogout(t *testing.T) {
	h := newHarness(t, "")
	hash, _ := authkit.HashPassword("a long password")
	h.store.CreateUser(context.Background(), store.User{Username: "mum", PasswordHash: hash, Role: auth.RoleAdmin})

	if _, body := h.get("/"); !strings.Contains(body, "Log in") {
		t.Fatal("expected a redirect to the login page")
	}
	code, body := h.post("/login", url.Values{"username": {"mum"}, "password": {"wrong password!"}}, h.srv.URL)
	if code != http.StatusUnauthorized || !strings.Contains(body, "Wrong username or password") {
		t.Errorf("bad login: %d", code)
	}
	code, body = h.post("/login", url.Values{"username": {"mum"}, "password": {"a long password"}}, h.srv.URL)
	if code != http.StatusOK || !strings.Contains(body, "<title>Home · Roostack</title>") {
		t.Fatalf("login failed: %d", code)
	}
	if !strings.Contains(body, "Protect your account with two-factor authentication") {
		t.Error("admins without 2FA should see a reminder, not be forced")
	}
	if _, body := h.get("/partials/services"); !strings.Contains(body, "status-running") {
		t.Error("services partial should render")
	}
	h.post("/logout", nil, h.srv.URL)
	if _, body := h.get("/"); !strings.Contains(body, "Log in") {
		t.Error("should be logged out")
	}
}

// Logging in comes back to the page someone asked for (?next=), through a
// two-factor code and choosing their own password, and never goes to
// another site.
func TestLoginComesBack(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	h.post("/logout", nil, h.srv.URL)
	ctx := context.Background()
	hash, _ := authkit.HashPassword("temporary password")
	h.store.CreateUser(ctx, store.User{Username: "dad", PasswordHash: hash, Role: auth.RoleAdmin, MustChangePassword: true})

	_, body := h.get("/services")
	if !strings.Contains(body, `<input type="hidden" name="next" value="/services">`) {
		t.Fatal("the login page should remember the page asked for")
	}
	_, body = h.post("/login", url.Values{"username": {"dad"}, "password": {"temporary password"}, "next": {"/services"}}, h.srv.URL)
	if !strings.Contains(body, "using a temporary password") || !strings.Contains(strings.ToLower(body), `action="/account/password?next=%2fservices"`) {
		t.Fatal("choosing a password first should carry on to the page asked for")
	}
	_, body = h.post("/account/password?next=%2Fservices", url.Values{"current": {"temporary password"},
		"password": {"my own long password"}, "confirm": {"my own long password"}}, h.srv.URL)
	if !strings.Contains(body, "<title>Services · Roostack</title>") {
		t.Error("after choosing a password: not the page asked for")
	}

	// With a two-factor code.
	h.post("/logout", nil, h.srv.URL)
	secret, _ := authkit.NewTOTPSecret()
	u, _ := h.store.UserByUsername(ctx, "dad")
	h.store.SetTOTP(ctx, u.ID, secret, 0)
	h.post("/login", url.Values{"username": {"dad"}, "password": {"my own long password"}, "next": {"/services"}}, h.srv.URL)
	code, _ := authkit.TOTPCode(secret, nowFunc())
	if _, body := h.post("/login/totp", url.Values{"code": {code}}, h.srv.URL); !strings.Contains(body, "<title>Services · Roostack</title>") {
		t.Error("after a two-factor code: not the page asked for")
	}

	for next, want := range map[string]string{
		"/immich-photos": "/immich-photos", "/services/immich?x=1#photos": "/services/immich?x=1#photos",
		"//evil.example/": "/", "https://evil.example/": "/", "/\\evil.example": "/", "services": "/",
		"/login?next=/x": "/", "/login/totp": "/", "": "/", "/a\nb": "/",
	} {
		if got := localNext(next); got != want {
			t.Errorf("localNext(%q) = %q, want %q", next, got, want)
		}
	}
}

// An unknown username gets the same answer as a wrong password; a broken
// database is a server error, not a wrong password.
func TestLoginUnknownUserAndBrokenStore(t *testing.T) {
	h := newHarness(t, "")
	code, body := h.post("/login", url.Values{"username": {"nobody"}, "password": {"a long password"}}, h.srv.URL)
	if code != http.StatusUnauthorized || !strings.Contains(body, "Wrong username or password") {
		t.Errorf("unknown user: %d", code)
	}
	h.store.Close()
	if code, _ := h.post("/login", url.Values{"username": {"nobody"}, "password": {"x"}}, h.srv.URL); code != http.StatusInternalServerError {
		t.Errorf("broken store: status %d, want 500", code)
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	h := newHarness(t, "")
	for _, origin := range []string{"", "https://evil.example.com"} {
		code, _ := h.post("/login", url.Values{"username": {"x"}, "password": {"y"}}, origin)
		if code != http.StatusForbidden {
			t.Errorf("origin %q: status %d, want 403", origin, code)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness(t, "")
	resp, err := h.client.Get(h.srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP missing or incomplete: %q", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff header missing")
	}
}

func TestStaticAssets(t *testing.T) {
	h := newHarness(t, "")
	for _, path := range []string{"/static/css/tokens.css", "/static/css/themes/ocean.css", "/static/css/app.css", "/static/htmx-2.0.11.min.js", "/static/alpine-3.17.4.min.js"} {
		if code, _ := h.get(path); code != http.StatusOK {
			t.Errorf("%s: %d", path, code)
		}
	}
}

// Background refreshes must never have another page swapped into them
// (the setup form appeared inside the dashboard and reset every 10s).
func TestHtmxRequestsGetFullPageRedirects(t *testing.T) {
	h := newHarness(t, "")
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/partials/services", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := resp.Header.Get("HX-Redirect"); got != "/login" {
		t.Errorf("HX-Redirect = %q, want /login", got)
	}
	if strings.Contains(string(body), "<form") {
		t.Error("the login page must not be returned into the partial")
	}
	// Normal requests still get a regular redirect.
	if _, body := h.get("/partials/services"); !strings.Contains(body, "Log in") {
		t.Error("plain requests should follow the redirect to the login page")
	}
}

func TestChangePassword(t *testing.T) {
	h := newHarness(t, "")
	hash, _ := authkit.HashPassword("old password here")
	h.store.CreateUser(context.Background(), store.User{Username: "mum", PasswordHash: hash, Role: auth.RoleMember})
	h.post("/login", url.Values{"username": {"mum"}, "password": {"old password here"}}, h.srv.URL)

	// A second device logged in too.
	other := newClientFor(h)
	other.post("/login", url.Values{"username": {"mum"}, "password": {"old password here"}}, h.srv.URL)

	for _, tc := range []struct{ current, pw, confirm, want string }{
		{"wrong", "new password here", "new password here", "current password is wrong"},
		{"old password here", "new password here", "different", "don&#39;t match"},
		{"old password here", "short", "short", "at least 12"},
	} {
		code, body := h.post("/account/password", url.Values{"current": {tc.current}, "password": {tc.pw}, "confirm": {tc.confirm}}, h.srv.URL)
		if code != http.StatusBadRequest || !strings.Contains(body, tc.want) {
			t.Errorf("%+v: %d", tc, code)
		}
	}
	code, body := h.post("/account/password", url.Values{"current": {"old password here"},
		"password": {"new password here"}, "confirm": {"new password here"}}, h.srv.URL)
	if code != http.StatusOK || !strings.Contains(body, "Password changed") {
		t.Fatalf("change: %d", code)
	}
	if _, body := h.get("/account"); !strings.Contains(body, "Your account") {
		t.Error("this device should stay logged in")
	}
	if _, body := other.get("/"); !strings.Contains(body, "Log in") {
		t.Error("other devices must be logged out")
	}
}

// newClientFor is a second browser (its own cookies) for the same server.
func newClientFor(h *harness) *harness {
	jar, _ := cookiejar.New(nil)
	return &harness{t: h.t, srv: h.srv, store: h.store, server: h.server, client: &http.Client{Jar: jar}}
}

func TestCrashBecomesErrorPage(t *testing.T) {
	h := newHarness(t, "")
	h.server.Services = func(context.Context) ([]ServiceView, error) { panic("boom") }
	hash, _ := authkit.HashPassword("a long password")
	h.store.CreateUser(context.Background(), store.User{Username: "mum", PasswordHash: hash, Role: auth.RoleMember})
	h.post("/login", url.Values{"username": {"mum"}, "password": {"a long password"}}, h.srv.URL)
	code, body := h.get("/")
	if code != http.StatusInternalServerError || !strings.Contains(body, "Something went wrong") {
		t.Errorf("a crash should give an error page, got %d %q", code, body)
	}
}

// Roostack's own files are revalidated, so an update's app.js is used at
// once: an ETag, "no-cache", and 304 while unchanged.
func TestStaticRevalidated(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	resp, err := http.Get(h.srv.URL + "/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	tag := resp.Header.Get("ETag")
	if tag == "" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("headers: %v", resp.Header)
	}
	req, _ := http.NewRequest("GET", h.srv.URL+"/static/app.js", nil)
	req.Header.Set("If-None-Match", tag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("unchanged: %d", resp.StatusCode)
	}
}

// The Content-Security-Policy (style-src 'self') blocks style attributes,
// so templates mustn't use them: a bar's width is data-width (app.js).
func TestNoStyleAttributes(t *testing.T) {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, _ := fs.ReadFile(templateFS, "templates/"+e.Name())
		if i := bytes.Index(b, []byte(` style="`)); i >= 0 {
			t.Errorf("%s uses a style attribute, which the browser blocks: %.60s", e.Name(), b[i:])
		}
	}
}

// Every page's footer says which Roostack this is.
func TestFooterVersion(t *testing.T) {
	h, _ := loggedIn(t, "owner")
	h.server.Version = "1.2.3"
	for _, page := range []string{"/", "/settings", "/about"} {
		if _, body := h.get(page); !strings.Contains(body, `<footer class="site">Roostack <span class="version" title="Roostack's version">1.2.3</span>`) {
			t.Errorf("%s: no version in the footer", page)
		}
	}
}
