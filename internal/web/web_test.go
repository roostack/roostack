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

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/store"
)

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
	store  *store.Store
	setup  bool // OnSetupComplete called
}

func newHarness(t *testing.T, setupToken string) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "mylab.db"))
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
	h := &harness{t: t, store: st}
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

func TestFirstRunSetup(t *testing.T) {
	h := newHarness(t, "the-setup-code")

	// Everything redirects to setup until an Owner exists.
	if _, body := h.get("/"); !strings.Contains(body, "Welcome to MyLab") {
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
	if code != http.StatusOK || !strings.Contains(body, "Your services") || !strings.Contains(body, "Vaultwarden") {
		t.Fatalf("after setup expected the dashboard, got %d", code)
	}
	if !h.setup {
		t.Error("OnSetupComplete not called")
	}
	u, err := h.store.UserByUsername(context.Background(), "tyris")
	if err != nil || u.Role != auth.RoleOwner {
		t.Errorf("owner not created: %+v %v", u, err)
	}

	// Setup can't be repeated.
	if _, body := h.get("/setup"); strings.Contains(body, "Welcome to MyLab") {
		t.Error("setup page must not be available after setup")
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
	if code != http.StatusOK || !strings.Contains(body, "Your services") {
		t.Fatalf("login failed: %d", code)
	}
	if _, body := h.get("/partials/services"); !strings.Contains(body, "status-running") {
		t.Error("services partial should render")
	}
	h.post("/logout", nil, h.srv.URL)
	if _, body := h.get("/"); !strings.Contains(body, "Log in") {
		t.Error("should be logged out")
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
	for _, path := range []string{"/static/style.css", "/static/htmx-2.0.11.min.js", "/static/alpine-3.17.4.min.js"} {
		if code, _ := h.get(path); code != http.StatusOK {
			t.Errorf("%s: %d", path, code)
		}
	}
}
