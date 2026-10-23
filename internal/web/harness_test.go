package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/store"
)

// The harness the web tests drive the real handlers through: an
// httptest server, a cookie jar and a real store, with a fake manager and
// a user logged in (loggedIn).

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
	store  *store.Store
	server *Server
	setup  bool // OnSetupComplete called
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
	// The manager and the setup steps are always there: a fake manager
	// with nothing to say, and a setup wizard that's finished. Tests that
	// need them otherwise replace them.
	s, err := NewServer(st, slog.New(slog.NewTextHandler(io.Discard, nil)), services, &fakeManager{}, &fakeSetup{p: allDone},
		LabSettings{}, setupToken)
	if err != nil {
		t.Fatal(err)
	}
	s.SetupTokenFile = config.Default().Paths.SetupTokenFile()
	h := &harness{t: t, store: st, server: s}
	s.OnSetupComplete = func() { h.setup = true }
	h.srv = httptest.NewServer(s.Handler())
	t.Cleanup(h.srv.Close)
	jar, _ := cookiejar.New(nil)
	h.client = &http.Client{Jar: jar}
	return h
}

// checkRendered fails the test when a page shows a value its template
// didn't get, which pages built from maps (pageData) make easy to miss:
// "<no value>" (text/template's missing key) or a printf's "%!" (one of
// its values missing or of the wrong kind). html/template shows a missing
// key as nothing, which only a test of the page's words can catch.
func checkRendered(t *testing.T, path, body string) {
	t.Helper()
	for _, bad := range []string{"<no value>", "&lt;no value&gt;", "%!"} {
		if strings.Contains(body, bad) {
			t.Errorf("%s shows %q: a template asks for something its data doesn't have", path, bad)
		}
	}
}

func (h *harness) get(path string) (int, string) {
	h.t.Helper()
	resp, err := h.client.Get(h.srv.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	checkRendered(h.t, path, string(body))
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
	checkRendered(h.t, path, string(body))
	return resp.StatusCode, string(body)
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

// loggedIn returns a harness with a fake manager and a logged-in user.
func loggedIn(t *testing.T, role string) (*harness, *fakeManager) {
	t.Helper()
	h := newHarness(t, "")
	fm := &fakeManager{}
	h.server.Manager, h.server.Guests, h.server.Node = fm, fm, fm
	h.server.Domain = "lab.example.com"
	h.server.HasGPU = true
	hash, _ := authkit.HashPassword("a long password")
	u := store.User{Username: "u", PasswordHash: hash, Role: role}
	if role == auth.RoleOwner {
		u.Email = "u@example.com" // every Owner has one (ownerNeedsEmail)
	}
	id, _ := h.store.CreateUser(context.Background(), u)
	if role == auth.RoleOwner {
		secret, _ := authkit.NewTOTPSecret()
		h.store.SetTOTP(context.Background(), id, secret, 0)
		h.post("/login", url.Values{"username": {"u"}, "password": {"a long password"}}, h.srv.URL)
		code, _ := authkit.TOTPCode(secret, nowFunc())
		h.post("/login/totp", url.Values{"code": {code}}, h.srv.URL)
	} else {
		h.post("/login", url.Values{"username": {"u"}, "password": {"a long password"}}, h.srv.URL)
	}
	return h, fm
}

// setSetup sets the wizard; a fakeSetup is also the server's settings.
func (h *harness) setSetup(ls LabSetup) {
	h.server.Setup = ls
	if fs, ok := ls.(*fakeSetup); ok {
		h.node().settings = fs
	}
}

// node is the fake server.
func (h *harness) node() *fakeManager { return h.server.Node.(*fakeManager) }

// notify does one run of the notifier, wired as Roostack wires it.
func (h *harness) notify(ctx context.Context, now time.Time) error {
	n := &attention.Notifier{Store: h.store, Log: h.server.Log, Lab: h.server.AttentionLab}
	if h.server.Mail != nil {
		n.Mail = h.server.Mail
	}
	return n.Notify(ctx, now)
}

// acts is the fake node's activity registry.
func (h *harness) acts() *activity.Registry {
	fm := h.server.Node.(*fakeManager)
	if fm.acts == nil {
		fm.acts = activity.New()
	}
	return fm.acts
}

// postFiles posts a multipart form with these files (field: contents),
// as a browser's file upload does.
func (h *harness) postFiles(path string, files map[string][]byte) (int, string) {
	h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for field, data := range files {
		w, _ := mw.CreateFormFile(field, field+".file")
		w.Write(data)
	}
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", h.srv.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	checkRendered(h.t, path, string(body))
	return resp.StatusCode, string(body)
}

// get with htmx's header: a part of a page refreshing itself.
func (h *harness) poll(path string) *http.Response {
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// testWriter sends a log to the test's.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(b []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(b)))
	return len(b), nil
}
