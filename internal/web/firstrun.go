package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/trust"
)

// First-run setup from another device (SPEC.md §6.3.2). Until setup is
// finished, Roostack also serves its setup pages, and only those, to the
// home network over HTTPS on config.SetupPort, with a self-signed
// certificate whose fingerprint the installer and the log show. When setup
// is finished, the listener stops, the firewall closes the port and the
// certificate is deleted: from then on the UI is 127.0.0.1:8480 and the
// lab's address through Caddy, as before.
//
// How each threat is met:
//   - Someone else on the network setting the lab up first: they'd need
//     the setup code, which is only on the server (the installer's message,
//     its log and a 0600 file). After the Owner exists, the Owner's
//     password and two-factor code.
//   - Someone in the middle (ARP or DHCP spoofing): the certificate is
//     self-signed, so the browser warns either way; the person compares
//     its SHA-256 fingerprint with the installer's before going on.
//   - DNS rebinding: a page from another site, whose name now points at
//     this server, can't complete a TLS handshake (the certificate isn't
//     valid for its name, and scripts can't click through a warning), and
//     the Host header must be an IP address anyway, never a name. A page
//     can only make a browser send an address it was itself opened at,
//     which then reaches that address, not this server. (Not this server's
//     own address: a port forward, or a VM's, changes it on the way.)
//   - Cross-site forms (CSRF): sameOrigin requires the Origin to match the
//     Host, so the form came from the setup page itself; the setup code's
//     cookie is SameSite.
//   - Anyone outside the home network: connections from addresses outside
//     trust.HomeRanges are closed before TLS, and the firewall
//     (roostack-agent) drops them, also before the Owner has chosen one.

// FirstRun is first-run setup (SPEC.md §6.3.2): the setup code, and
// what a fresh install can become instead of a new lab.
type FirstRun struct {
	// OnSetupComplete is called after the first Owner is created (e.g. to
	// delete the token file).
	OnSetupComplete func()
	// FirstRunAddress is the lab's address for Roostack's own pages
	// ("https://…"), as saved now, or "" without one: where the setup
	// listener sends the Owner once setup is finished (SPEC.md §6.3.2).
	FirstRunAddress func() string
	// JoinLab adds this fresh server to an existing lab, named name, and
	// restarts Roostack as part of it once the page has been sent; it
	// returns this server's address on the lab's network. Undone if it
	// fails. Nil hides the choice (SPEC.md §10.2).
	JoinLab func(ctx context.Context, code labnet.JoinCode, name string) (string, error)
	// LabRestore rebuilds a lab from its backups on a fresh install. Nil
	// hides it. RestorePending reports a restored lab whose services'
	// databases are still being put back (optional).
	LabRestore     LabRestorer
	RestorePending func() bool

	// setupMu guards setupToken and restore. setupToken must be entered
	// to create the first Owner, so whoever reaches the UI first on the
	// network can't claim the lab; it's empty once setup is done. restore
	// is a lab restore's search (labrestore.go).
	setupMu    sync.Mutex
	setupToken string
	restore    *restoreSession
	// setupPasses are browsers that entered the setup code (setup.go), by
	// where it was entered and the token's hash.
	setupPasses expiring[string, struct{}]
}

// setupCertLife is how long a setup certificate lasts: under browsers'
// limit for certificates people trust by hand (825 days), and long enough
// that the one the installer showed is still the one in use.
const setupCertLife = 397 * 24 * time.Hour

// SetupCert returns the setup pages' certificate and its fingerprint,
// making it if there's none or it has expired. addrs are this server's
// addresses on the home network, named in the certificate (browsers warn
// about a self-signed one anyway, but not about its name as well).
func SetupCert(paths config.Paths, addrs []netip.Addr, now time.Time) (tls.Certificate, string, error) {
	cert, err := tls.LoadX509KeyPair(paths.SetupCertFile(), paths.SetupKeyFile())
	if err == nil && cert.Leaf != nil && now.Before(cert.Leaf.NotAfter) {
		return cert, Fingerprint(cert.Leaf.Raw), nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	name, _ := os.Hostname()
	// No SerialNumber: x509.CreateCertificate makes a random one.
	tmpl := &x509.Certificate{
		Subject:     pkix.Name{CommonName: "Roostack setup (" + name + ")"},
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(setupCertLife),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, a := range addrs {
		tmpl.IPAddresses = append(tmpl.IPAddresses, a.AsSlice())
	}
	if name != "" {
		tmpl.DNSNames = []string{name, name + ".local"}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	// The key first, so a certificate on disk always has its key.
	if err := fsutil.WritePrivate(paths.SetupKeyFile(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return tls.Certificate{}, "", err
	}
	// The certificate is public: the installer's message reads it for its
	// fingerprint. fsutil writes through a new file, never through a
	// symlink someone put there.
	if err := fsutil.WriteFile(paths.SetupCertFile(), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err = tls.LoadX509KeyPair(paths.SetupCertFile(), paths.SetupKeyFile())
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, Fingerprint(der), nil
}

// RemoveSetupCert deletes the setup pages' certificate and key, once setup
// is finished.
func RemoveSetupCert(paths config.Paths) error {
	var errs []error
	for _, f := range []string{paths.SetupKeyFile(), paths.SetupCertFile()} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Fingerprint is a certificate's SHA-256 as browsers show it: pairs of
// capital hex digits, separated by colons.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	pairs := make([]string, len(sum))
	for i, b := range sum {
		pairs[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(pairs, ":")
}

// HomeAddrs lists this server's addresses on the home network, IPv4 first:
// where the setup pages can be opened from (link-local IPv6 addresses
// can't be typed into a browser, and are left out).
func HomeAddrs() []netip.Addr {
	ifaddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var v4, v6 []netip.Addr
	for _, ia := range ifaddrs {
		p, err := netip.ParsePrefix(ia.String())
		if err != nil {
			continue
		}
		a := p.Addr().Unmap()
		switch {
		case !trust.IsHome(a) || a.IsLinkLocalUnicast():
		case a.Is4():
			v4 = append(v4, a)
		default:
			v6 = append(v6, a)
		}
	}
	return append(v4, v6...)
}

// SetupURL is the address to open the setup pages at, on addr.
func SetupURL(addr netip.Addr) string {
	return "https://" + netip.AddrPortFrom(addr, config.SetupPort).String()
}

// SetupOwnerLimit is how long the setup pages stay on the home network
// once the Owner exists, finished or not (SPEC.md §6.3.2). It counts from
// the account's creation, kept in the database, so a restart doesn't
// start it again.
const SetupOwnerLimit = 24 * time.Hour

// FirstRunOpen reports whether the setup pages are served to the home
// network: while the setup code hasn't been used (codeUnused), then while
// the Owner hasn't finished the wizard, for SetupOwnerLimit at most.
func FirstRunOpen(ctx context.Context, st *store.Store, codeUnused bool, wizard interface {
	Progress(context.Context) (setup.Progress, error)
}, now time.Time) bool {
	if codeUnused {
		return true
	}
	if wizard == nil || ownerTooLong(ctx, st, now) {
		return false
	}
	p, err := wizard.Progress(ctx)
	return err == nil && p.Next() != ""
}

// ownerTooLong reports whether the Owner was made SetupOwnerLimit ago or
// more (or that can't be told).
func ownerTooLong(ctx context.Context, st *store.Store, now time.Time) bool {
	created, ok, err := st.FirstUserCreated(ctx)
	return err != nil || !ok || now.Sub(created) >= SetupOwnerLimit
}

// setupOpen is FirstRunOpen for this server.
func (s *Server) setupOpen(ctx context.Context) bool {
	return FirstRunOpen(ctx, s.Store, s.needsSetup(), s.Setup, time.Now())
}

// EndSetupPages signs out everything signed in on the setup pages once
// they've closed (SPEC.md §6.3.2). Roostack's start does it too, in case
// they closed with Roostack.
func EndSetupPages(ctx context.Context, st *store.Store) error {
	return st.DeleteSessionsAt(ctx, store.OriginSetup)
}

// EndSetupPages is EndSetupPages, and this process's own records of the
// setup pages' browsers: the setup code's passes, logins waiting for a
// two-factor code and a restore's search.
func (s *Server) EndSetupPages(ctx context.Context) error {
	setupOrigin := func(k string) bool { return strings.HasPrefix(k, originSetup) }
	s.setupPasses.deleteIf(setupOrigin)
	s.pending.deleteIf(setupOrigin)
	s.setupMu.Lock()
	if s.restore != nil && s.restore.origin == originSetup {
		s.restore = nil
	}
	s.setupMu.Unlock()
	return EndSetupPages(ctx, s.Store)
}

// setupListenerKey marks a request that came in on the setup listener,
// with a setupListened.
type setupListenerKey struct{}

// setupListened is the setup listener a request came in on.
type setupListened struct {
	s *Server
	// done closes the listener, once the page saying so has gone.
	done func()
}

// originSetup prefixes the setup listener's in-memory records (originOf).
const originSetup = store.OriginSetup + ":"

// originOf is where r came in, for the setup code's passes, logins waiting
// for a code and a restore's search: originSetup on the setup listener, ""
// on Roostack's own pages. Each works only where it was made.
func originOf(r *http.Request) string {
	if fromSetupListener(r) {
		return originSetup
	}
	return ""
}

// sessionOrigin is where r came in, as sessions record it.
func sessionOrigin(r *http.Request) string {
	if fromSetupListener(r) {
		return store.OriginSetup
	}
	return ""
}

// fromSetupListener reports whether r came in on the setup listener.
func fromSetupListener(r *http.Request) bool {
	_, ok := r.Context().Value(setupListenerKey{}).(setupListened)
	return ok
}

// setupGate, on the setup listener, lets only the Owner past once the
// Owner exists, signed in there, and only to the wizard's two-factor step
// (and its help) until they've turned it on; before, the setup code guards
// the pages. Logging in and the files its page needs are all that's open
// to anyone else (SPEC.md §6.3.2).
func (s *Server) setupGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if s.needsSetup() || path == "/login" || path == "/login/totp" || strings.HasPrefix(path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		u := currentUser(r)
		switch {
		case u == nil:
			redirect(w, r, "/login")
		case u.Role != auth.RoleOwner:
			http.Error(w, "Only the Owner can finish setup here.", http.StatusForbidden)
		case u.TOTPSecret == "" && path != wizardTwoFactor && path != "/logout" && path != "/help" && !strings.HasPrefix(path, "/help/"):
			// The listener lasts only while the wizard does, so two-factor
			// is always its step here.
			redirect(w, r, wizardTwoFactor)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// finishedOnSetupListener answers a request whose redirect follows the
// end of first-run setup on the setup listener: with the page saying where
// Roostack is now, not a redirect into a listener that's closing. It
// reports whether it did.
func finishedOnSetupListener(w http.ResponseWriter, r *http.Request) bool {
	l, ok := r.Context().Value(setupListenerKey{}).(setupListened)
	if !ok || l.s.setupOpen(r.Context()) {
		return false
	}
	l.s.setupFinished(w, r)
	l.done()
	return true
}

// firstRunPaths are what the setup listener serves: the first-run pages
// and wizard (two-factor set-up is its first step), logging in, the parts
// the wizard's pages load or post to (its address step's Cloud link and
// network choice, the backup step's live log and a destination's
// location), the help and static files. "/" sends the Owner on to the
// wizard's next step. Nothing else, even for the Owner.
var firstRunPaths = []string{"/", "/login", "/login/totp", "/logout", "/settings/network",
	"/settings/home-network", "/settings/cloud/live", "/settings/cloud/linked", "/settings/cloud/progress",
	"/settings/cloud/name", "/settings/cloud/link", "/settings/cloud/cancel", "/setup", "/help"}

// firstRunPrefixes are folders the setup listener serves all of.
var firstRunPrefixes = []string{"/setup/", "/static/", "/help/", "/activity/" + activity.BackupSetup}

// firstRunPath reports whether the setup listener serves path.
func firstRunPath(path string) bool {
	for _, p := range firstRunPaths {
		if path == p {
			return true
		}
	}
	for _, p := range firstRunPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	// A backup destination at home or away, in the wizard's plan.
	server, ok := strings.CutPrefix(path, "/settings/servers/")
	return ok && strings.Count(server, "/") == 1 && strings.HasSuffix(server, "/location")
}

// hostIsAddr reports whether host (a request's Host) is an IP address
// and port: never a name, which another site could point here (DNS
// rebinding).
func hostIsAddr(host string) bool {
	_, err := netip.ParseAddrPort(host)
	return err == nil
}

// SetupHandler serves the first-run pages on the setup listener. done is
// called once setup is finished and the page saying so has been sent.
func (s *Server) SetupHandler(done func()) http.Handler {
	inner := s.handler(s.setupGate)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hostIsAddr(r.Host) {
			http.Error(w, "Open the setup page at the server's address, as the installer showed it.", http.StatusMisdirectedRequest)
			return
		}
		if !firstRunPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		// Nothing stands between the browser and Roostack here, so nothing
		// a proxy would add is believed.
		for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host", "X-Real-Ip"} {
			r.Header.Del(h)
		}
		// Once setup is finished every page is the one saying so, but its
		// own stylesheets and scripts still load while the listener lingers.
		if !s.setupOpen(r.Context()) && !strings.HasPrefix(r.URL.Path, "/static/") {
			s.setupFinished(w, r)
			done()
			return
		}
		inner.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), setupListenerKey{}, setupListened{s, done})))
	})
}

// setupFinished says setup is done and where Roostack is from now on, or,
// past SetupOwnerLimit with the wizard unfinished, how to finish it over
// SSH: the setup listener closes.
func (s *Server) setupFinished(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{"Host": r.Host, "Hours": int(SetupOwnerLimit.Hours())}
	if p, err := s.Setup.Progress(r.Context()); err == nil && p.Next() != "" {
		data["Unfinished"] = true
	}
	if s.FirstRunAddress != nil && data["Unfinished"] == nil {
		data["Address"] = s.FirstRunAddress()
	}
	if ap, err := netip.ParseAddrPort(r.Host); err == nil {
		data["ServerAddr"] = ap.Addr().String()
	}
	if r.Header.Get("HX-Request") != "" {
		// A part of a page: the whole page shows it instead.
		w.Header().Set("HX-Refresh", "true")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "setup-finished", data)
}

// SetupListener serves the first-run pages to the home network while
// setup lasts (SPEC.md §6.3.2).
type SetupListener struct {
	Server *Server
	Cert   tls.Certificate
	// Addr is where to listen, ":8443" (config.SetupPort) on every address:
	// connections not between two home network addresses are closed.
	Addr string
	Log  *slog.Logger
	// Check is how often to look whether setup was finished elsewhere
	// (e.g. over an SSH tunnel); Linger how long the page saying it's
	// finished, and the files it loads, have to reach the browser before
	// the listener closes. Defaults: 30 s and 5 s.
	Check, Linger time.Duration
	// Allow says which connections are served, by the addresses at each
	// end; the default is the home network to this server's address on
	// it (trust.IsHome). For tests.
	Allow func(remote, local netip.Addr) bool

	once, finishOnce sync.Once
	finished         chan struct{} // closed by Finish
	closed           chan struct{} // closed when Serve returns
}

// init makes the channels, whichever of Finish, Closed and Serve comes
// first.
func (l *SetupListener) init() {
	l.once.Do(func() { l.finished, l.closed = make(chan struct{}), make(chan struct{}) })
}

// Finish says setup is finished: the listener closes Linger later, time
// for the page saying so and its files. Called by the wizard's end, which
// may come before the page is asked for; more calls do nothing.
func (l *SetupListener) Finish() {
	l.init()
	l.finishOnce.Do(func() { close(l.finished) })
}

// Closed is closed once the listener has stopped serving: the restart at
// the wizard's end waits for it, or the page's files would find the port
// closed.
func (l *SetupListener) Closed() <-chan struct{} {
	l.init()
	return l.closed
}

// Run serves until setup is finished (or ctx ends), then returns nil.
func (l *SetupListener) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", l.Addr)
	if err != nil {
		l.init()
		close(l.closed)
		return err
	}
	return l.Serve(ctx, ln)
}

// Serve is Run on a listener of the caller's.
func (l *SetupListener) Serve(ctx context.Context, ln net.Listener) error {
	l.init()
	defer close(l.closed)
	stopped := make(chan struct{})
	defer func() { <-stopped }()
	defer l.Finish() // the goroutine below ends with the listener
	srv := &http.Server{
		Handler:           l.Server.SetupHandler(l.Finish),
		ReadHeaderTimeout: 10 * time.Second,
		// Browsers that haven't accepted the certificate yet end each
		// handshake with an error: not worth a log line each.
		ErrorLog: log.New(io.Discard, "", 0),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{l.Cert}, MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2", "http/1.1"}},
	}
	if l.Check <= 0 {
		l.Check = 30 * time.Second
	}
	if l.Linger <= 0 {
		l.Linger = 5 * time.Second
	}
	if l.Allow == nil {
		l.Allow = func(remote, local netip.Addr) bool { return trust.IsHome(remote) && trust.IsHome(local) }
	}
	go func() {
		defer close(stopped)
		tick := time.NewTicker(l.Check)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
			case <-l.finished:
				// Let the page saying so, and its files, reach the browser
				// first.
				time.Sleep(l.Linger)
			case <-tick.C:
				if l.Server.setupOpen(ctx) {
					continue
				}
			}
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			srv.Shutdown(shutdown)
			cancel()
			return
		}
	}()
	if err := srv.ServeTLS(homeOnly{ln, l.Allow, l.Log}, "", ""); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// homeOnly closes connections that aren't from the home network to this
// server's address on it (allow), before TLS. The firewall drops them
// too; this holds if it hasn't been applied yet.
type homeOnly struct {
	net.Listener
	allow func(remote, local netip.Addr) bool
	log   *slog.Logger
}

func (h homeOnly) Accept() (net.Conn, error) {
	for {
		c, err := h.Listener.Accept()
		if err != nil {
			return nil, err
		}
		remote, rok := c.RemoteAddr().(*net.TCPAddr)
		local, lok := c.LocalAddr().(*net.TCPAddr)
		if rok && lok && h.allow(remote.AddrPort().Addr().Unmap(), local.AddrPort().Addr().Unmap()) {
			return c, nil
		}
		h.log.Warn("setup page: refused a connection from outside the home network", "remote", c.RemoteAddr().String())
		c.Close()
	}
}
