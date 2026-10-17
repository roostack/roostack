package web

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
)

// setupClient is a browser on the home network that has checked the
// setup listener's certificate against fingerprint (or accepts any, for
// httptest's).
func setupClient(t *testing.T, fingerprint string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if fingerprint != "" && Fingerprint(raw[0]) != fingerprint {
				return errors.New("not the setup certificate")
			}
			return nil
		}}}}
}

// do sends a request to the setup listener at base, with host as its Host
// header ("" for base's) and origin as its Origin ("" for none).
func do(t *testing.T, c *http.Client, method, base, path, host, origin string, form url.Values) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, base+path, strings.NewReader(form.Encode()))
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestSetupListenerServesOnlySetup: the setup listener serves the first-run
// pages and nothing else, only at the address it was reached on, and
// refuses forms from other sites (SPEC.md §6.3.2).
func TestSetupListenerServesOnlySetup(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	fs := &fakeSetup{}
	h.server.Setup = fs
	done := make(chan struct{}, 1)
	ts := httptest.NewTLSServer(h.server.SetupHandler(func() { done <- struct{}{} }))
	defer ts.Close()
	c := setupClient(t, "")
	origin := ts.URL

	if code, body := do(t, c, "GET", ts.URL, "/setup", "", "", nil); code != http.StatusOK || !strings.Contains(body, "Welcome to Roostack") {
		t.Fatalf("the setup page: %d", code)
	}
	for _, path := range []string{"/services", "/settings/people", "/backups/recovery-key", "/healthz", "/catalog",
		"/settings/cloud/unlink", "/.well-known/roostack-check/x", "/restarting", "/settings/servers/abc/remove",
		"/settings/servers/abc/x/location", "/activity/apply/status", "/activity/os-updates/log"} {
		if code, _ := do(t, c, "GET", ts.URL, path, "", "", nil); code != http.StatusNotFound {
			t.Errorf("%s on the setup listener: %d, want 404", path, code)
		}
	}
	// A name, even one pointing here (DNS rebinding), or another port.
	u, _ := url.Parse(ts.URL)
	for _, host := range []string{"roostack.example", "attacker.example:" + u.Port(), "localhost:" + u.Port(), u.Hostname()} {
		if code, _ := do(t, c, "GET", ts.URL, "/setup", host, "", nil); code != http.StatusMisdirectedRequest {
			t.Errorf("Host %s: %d, want 421", host, code)
		}
	}
	// Forms only from the setup page itself.
	for _, o := range []string{"", "https://attacker.example", "https://" + u.Hostname() + ":1"} {
		if code, _ := do(t, c, "POST", ts.URL, "/setup/code", "", o, url.Values{"token": {"the-setup-code"}}); code != http.StatusForbidden {
			t.Errorf("a form from %q: %d, want 403", o, code)
		}
	}
	if code, _ := do(t, c, "POST", ts.URL, "/setup/code", "", origin, url.Values{"token": {"wrong"}}); code != http.StatusBadRequest {
		t.Errorf("a wrong setup code: %d", code)
	}

	// The Owner, then the wizard's steps, still on the setup listener.
	do(t, c, "POST", ts.URL, "/setup/code", "", origin, url.Values{"token": {"the-setup-code"}})
	form := url.Values{"token": {"the-setup-code"}, "username": {"tyris"}, "password": {"a long password"},
		"confirm": {"a long password"}}
	if _, body := do(t, c, "POST", ts.URL, "/setup", "", origin, form); !strings.Contains(body, "need to turn on two-factor") {
		t.Fatal("after the Owner, two-factor on the setup listener")
	}
	select {
	case <-done:
		t.Fatal("the setup listener closed before the wizard was finished")
	default:
	}

	// The wizard's last step: the page says where Roostack is now, and
	// the listener closes.
	fs.p = setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}
	h.server.FirstRunAddress = func() string { return "https://smiths.roost.ac" }
	code, body := do(t, c, "GET", ts.URL, "/setup/backup", "", "", nil)
	if code != http.StatusOK || !strings.Contains(body, "Setup is finished") || !strings.Contains(body, `href="https://smiths.roost.ac"`) {
		t.Errorf("after setup: %d %s", code, body)
	}
	select {
	case <-done:
	default:
		t.Error("the setup listener wasn't told to close")
	}
}

// TestSetupFinishedRedirect: the redirect after the wizard's last step,
// on the setup listener, is the page saying where Roostack is now, not
// a redirect into a listener that's closing.
func TestSetupFinishedRedirect(t *testing.T) {
	h := newHarness(t, "")
	h.store.CreateFirstUser(context.Background(), store.User{Username: "tyris", PasswordHash: "x", Role: auth.RoleOwner})
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	h.server.Setup = fs
	closed := false
	r := httptest.NewRequest("POST", "/setup/backup/later", nil)
	r = r.WithContext(context.WithValue(r.Context(), setupListenerKey{}, setupListened{h.server, func() { closed = true }}))
	r.Host = "192.168.1.20:8443"
	w := httptest.NewRecorder()
	redirect(w, r, "/")
	if body := w.Body.String(); w.Code != http.StatusOK || !strings.Contains(body, "Setup is finished") ||
		!strings.Contains(body, "ssh -L 8480:127.0.0.1:8480 you@192.168.1.20") || !closed {
		t.Errorf("finished without an address: %d, closed %v\n%s", w.Code, closed, body)
	}
	// Elsewhere, or while setup goes on, a redirect is a redirect.
	fs.p = setup.Progress{}
	w = httptest.NewRecorder()
	redirect(w, r, "/")
	if w.Code != http.StatusSeeOther {
		t.Errorf("during setup: %d", w.Code)
	}
}

// TestSetupListenerStops: the listener serves over TLS with its own
// certificate, closes once setup is finished (here, elsewhere: over an SSH
// tunnel), and refuses connections from outside the home network.
func TestSetupListenerStops(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	h.server.Setup = &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	paths := config.Paths{StateDir: t.TempDir()}
	cert, fp, err := SetupCert(paths, []netip.Addr{netip.MustParseAddr("127.0.0.1")}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := &SetupListener{Server: h.server, Cert: cert, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Check: 50 * time.Millisecond, Linger: time.Millisecond,
		Allow: func(remote, local netip.Addr) bool { return remote.IsLoopback() && local.IsLoopback() }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- l.Serve(ctx, ln) }()
	base := "https://" + ln.Addr().String()

	if code, body := do(t, setupClient(t, fp), "GET", base, "/setup", "", "", nil); code != http.StatusOK ||
		!strings.Contains(body, "Welcome to Roostack") {
		t.Fatalf("over TLS with the setup certificate: %d", code)
	}
	// The setup code is used elsewhere and the wizard was already done.
	h.server.mu.Lock()
	h.server.setupToken = ""
	h.server.mu.Unlock()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the setup listener didn't close after setup")
	}
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		c.Close()
		t.Error("the setup port still takes connections")
	}

	// From outside the home network: closed before TLS.
	ln, _ = net.Listen("tcp", "127.0.0.1:0")
	h.server.mu.Lock()
	h.server.setupToken = "the-setup-code"
	h.server.mu.Unlock()
	outside := &SetupListener{Server: h.server, Cert: cert, Log: l.Log}
	go outside.Serve(ctx, ln)
	if _, err := setupClient(t, fp).Get("https://" + ln.Addr().String() + "/setup"); err == nil {
		t.Error("the default listener served loopback, which isn't the home network")
	}
}

// TestSetupCert: the certificate is made once and kept (the installer
// showed its fingerprint), made again once expired, its key private, and
// both deleted when setup is done.
func TestSetupCert(t *testing.T) {
	paths := config.Paths{StateDir: t.TempDir()}
	addr := netip.MustParseAddr("192.168.1.20")
	now := time.Now()
	cert, fp, err := SetupCert(paths, []netip.Addr{addr}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(fp) != 95 || fp != Fingerprint(cert.Leaf.Raw) || !cert.Leaf.IPAddresses[0].Equal(addr.AsSlice()) {
		t.Errorf("fingerprint %q, addresses %v", fp, cert.Leaf.IPAddresses)
	}
	if fi, err := os.Stat(paths.SetupKeyFile()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the key's mode: %v %v", fi.Mode(), err)
	}
	if _, again, _ := SetupCert(paths, nil, now.Add(time.Hour)); again != fp {
		t.Error("a second start made a new certificate")
	}
	if _, later, _ := SetupCert(paths, nil, now.Add(setupCertLife+time.Hour)); later == fp {
		t.Error("an expired certificate was kept")
	}
	if err := RemoveSetupCert(paths); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{paths.SetupCertFile(), paths.SetupKeyFile()} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there", f)
		}
	}
	if err := RemoveSetupCert(paths); err != nil {
		t.Errorf("removing it again: %v", err)
	}
}

func TestHostIsAddr(t *testing.T) {
	for host, want := range map[string]bool{
		"192.168.1.20:8443": true,
		"[fd00::20]:8443":   true,
		"127.0.0.1:28444":   true, // a port forward
		"192.168.1.20":      false,
		"nuc.local:8443":    false,
		"nuc:8443":          false,
		"attacker.example":  false,
		"":                  false,
	} {
		if got := hostIsAddr(host); got != want {
			t.Errorf("hostIsAddr(%q) = %v", host, got)
		}
	}
}

// TestSetupListenerOwnerOnly: once the Owner exists, the setup listener
// serves only logging in to anyone else; a session works only where it
// was signed in (the setup pages or Roostack's own), and closing the
// setup pages signs out everything signed in on them (SPEC.md §6.3.2).
func TestSetupListenerOwnerOnly(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, "the-setup-code")
	h.server.Setup = &fakeSetup{}
	ts := httptest.NewTLSServer(h.server.SetupHandler(func() {}))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	c := setupClient(t, "")
	do(t, c, "POST", ts.URL, "/setup/code", "", ts.URL, url.Values{"token": {"the-setup-code"}})
	do(t, c, "POST", ts.URL, "/setup", "", ts.URL, url.Values{"token": {"the-setup-code"}, "username": {"tyris"},
		"password": {"a long password"}, "confirm": {"a long password"}})
	var listenerSession string
	for _, ck := range c.Jar.Cookies(u) {
		if ck.Name == sessionCookie {
			listenerSession = ck.Value
		}
	}
	if listenerSession == "" {
		t.Fatal("no session on the setup listener")
	}
	// The Owner, signed in here, goes on to two-factor first.
	if _, body := do(t, c, "GET", ts.URL, "/setup/timezone", "", "", nil); !strings.Contains(body, "need to turn on two-factor") {
		t.Error("the Owner without two-factor: not sent to set it up")
	}
	// The setup code no longer works here.
	if _, body := do(t, setupClient(t, ""), "POST", ts.URL, "/setup/code", "", ts.URL, url.Values{"token": {"the-setup-code"}}); !strings.Contains(body, `action="/login"`) {
		t.Error("the setup code after the Owner: not the login page")
	}

	// Anyone else gets the login page, whatever they ask for, even with
	// a session from Roostack's own pages.
	owner, _ := h.store.UserByUsername(ctx, "tyris")
	h.store.CreateSessionAt(ctx, "", authkit.HashToken("ui-session"), owner.ID, time.Now().Add(time.Hour), false)
	stranger := setupClient(t, "")
	stranger.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: "ui-session"}})
	for _, path := range []string{"/", "/setup/timezone", "/help/", "/account/2fa", "/settings/network", "/setup/restore"} {
		if _, body := do(t, stranger, "GET", ts.URL, path, "", "", nil); !strings.Contains(body, `action="/login"`) {
			t.Errorf("%s without the setup pages' session: not the login page", path)
		}
	}
	if code, _ := do(t, stranger, "GET", ts.URL, "/static/css/app.css", "", "", nil); code != http.StatusOK {
		t.Errorf("the login page's files: %d", code)
	}
	// And the setup pages' session doesn't work on Roostack's own pages.
	req, _ := http.NewRequest("GET", h.srv.URL+"/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: listenerSession})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/login" {
		t.Errorf("the setup pages' session on Roostack's own pages ended at %s", resp.Request.URL.Path)
	}

	// Closing the setup pages signs out their sessions, not the others.
	if err := h.server.EndSetupPages(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := h.store.SessionInfoAt(ctx, store.OriginSetup, authkit.HashToken(listenerSession)); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the setup pages' session after they closed: %v", err)
	}
	if _, _, _, err := h.store.SessionInfo(ctx, authkit.HashToken("ui-session")); err != nil {
		t.Errorf("Roostack's own session after the setup pages closed: %v", err)
	}
}

// TestSetupOwnerLimit: the setup pages close a day after the Owner was
// made, finished or not, counted from the account in the database; then
// the page shows how to carry on over SSH.
func TestSetupOwnerLimit(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, "")
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true}}
	if !FirstRunOpen(ctx, h.store, true, fs, time.Now()) {
		t.Error("closed before the setup code was used")
	}
	if FirstRunOpen(ctx, h.store, false, fs, time.Now()) {
		t.Error("open with the code used but no Owner")
	}
	h.store.CreateFirstUser(ctx, store.User{Username: "tyris", PasswordHash: "x", Role: auth.RoleOwner})
	if !FirstRunOpen(ctx, h.store, false, fs, time.Now().Add(SetupOwnerLimit-time.Minute)) {
		t.Error("closed within the day, wizard unfinished")
	}
	if FirstRunOpen(ctx, h.store, false, fs, time.Now().Add(SetupOwnerLimit+time.Minute)) {
		t.Error("still open a day after the Owner was made")
	}
	fs.p = setup.Progress{TimezoneDone: true, SSHDone: true, DomainLater: true, BackupLater: true}
	if FirstRunOpen(ctx, h.store, false, fs, time.Now()) {
		t.Error("open with the wizard finished")
	}

	// The page once closed with the wizard unfinished.
	fs.p = setup.Progress{TimezoneDone: true}
	h.server.Setup = fs
	r := httptest.NewRequest("GET", "/setup/ssh", nil)
	r.Host = "192.168.1.20:8443"
	w := httptest.NewRecorder()
	h.server.setupFinished(w, r)
	if body := w.Body.String(); !strings.Contains(body, "This setup page has closed") || !strings.Contains(body, "24 hours") ||
		!strings.Contains(body, "ssh -L 8480:127.0.0.1:8480 you@192.168.1.20") {
		t.Errorf("closed unfinished:\n%s", body)
	}
}
