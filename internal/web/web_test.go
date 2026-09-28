package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	form := url.Values{"token": {"the-setup-code"}, "lab_name": {"The Smiths' lab"}, "username": {"tyris"},
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

	form := url.Values{"token": {"wrong"}, "username": {"tyris"},
		"password": {"a long password"}, "confirm": {"a long password"}}
	if code, body := h.post("/setup", form, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "setup code is wrong") {
		t.Fatalf("wrong token: %d", code)
	}
	if n, _ := h.store.CountUsers(context.Background()); n != 0 {
		t.Fatal("no user may be created with a wrong token")
	}

	form.Set("token", "the-setup-code")
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
	if err != nil || u.Role != auth.RoleOwner {
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
	used, _ := auth.TOTPCode(secret, enrolledAt)
	if code, _ := h.post("/login/totp", url.Values{"code": {used}}, h.srv.URL); code != http.StatusUnauthorized {
		t.Errorf("reusing the enrollment code: status %d, want 401", code)
	}
	next, _ := auth.TOTPCode(secret, enrolledAt.Add(30*time.Second))
	if _, body := h.post("/login/totp", url.Values{"code": {next}}, h.srv.URL); !strings.Contains(body, "My Services") {
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
	_, rest, ok := strings.Cut(page, `name="secret" value="`)
	if !ok {
		h.t.Fatal("no secret on the 2FA page")
	}
	secret, _, _ := strings.Cut(rest, `"`)
	now := time.Now()
	code, err := auth.TOTPCode(secret, now)
	if err != nil {
		h.t.Fatal(err)
	}
	if status, _ := h.post("/account/2fa", url.Values{"secret": {secret}, "code": {"000000"}}, h.srv.URL); status != http.StatusBadRequest {
		h.t.Errorf("a wrong enrollment code: status %d, want 400", status)
	}
	if status, _ := h.post("/account/2fa", url.Values{"secret": {secret}, "code": {code}}, h.srv.URL); status != http.StatusOK {
		h.t.Fatalf("enrollment failed: %d", status)
	}
	return secret, now
}

func TestTooManyCodes(t *testing.T) {
	h := newHarness(t, "")
	hash, _ := auth.HashPassword("a long password")
	id, _ := h.store.CreateUser(context.Background(), store.User{Username: "tyris", PasswordHash: hash, Role: auth.RoleOwner})
	secret, _ := auth.NewTOTPSecret()
	h.store.SetTOTP(context.Background(), id, secret, 0)

	h.post("/login", url.Values{"username": {"tyris"}, "password": {"a long password"}}, h.srv.URL)
	var body string
	for i := 0; i < maxTOTPAttempts; i++ {
		_, body = h.post("/login/totp", url.Values{"code": {"000000"}}, h.srv.URL)
	}
	if !strings.Contains(body, "Too many wrong codes") {
		t.Fatal("expected lockout after too many codes")
	}
	good, _ := auth.TOTPCode(secret, time.Now())
	if _, body := h.post("/login/totp", url.Values{"code": {good}}, h.srv.URL); strings.Contains(body, "My Services") {
		t.Error("after lockout the password must be entered again")
	}
}

func TestLoginLogout(t *testing.T) {
	h := newHarness(t, "")
	hash, _ := auth.HashPassword("a long password")
	h.store.CreateUser(context.Background(), store.User{Username: "mum", PasswordHash: hash, Role: auth.RoleAdmin})

	if _, body := h.get("/"); !strings.Contains(body, "Log in") {
		t.Fatal("expected a redirect to the login page")
	}
	code, body := h.post("/login", url.Values{"username": {"mum"}, "password": {"wrong password!"}}, h.srv.URL)
	if code != http.StatusUnauthorized || !strings.Contains(body, "Wrong username or password") {
		t.Errorf("bad login: %d", code)
	}
	code, body = h.post("/login", url.Values{"username": {"mum"}, "password": {"a long password"}}, h.srv.URL)
	if code != http.StatusOK || !strings.Contains(body, "My Services") {
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
	hash, _ := auth.HashPassword("old password here")
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
	hash, _ := auth.HashPassword("a long password")
	h.store.CreateUser(context.Background(), store.User{Username: "mum", PasswordHash: hash, Role: auth.RoleMember})
	h.post("/login", url.Values{"username": {"mum"}, "password": {"a long password"}}, h.srv.URL)
	code, body := h.get("/")
	if code != http.StatusInternalServerError || !strings.Contains(body, "Something went wrong") {
		t.Errorf("a crash should give an error page, got %d %q", code, body)
	}
}
