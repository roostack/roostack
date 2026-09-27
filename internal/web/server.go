// Package web is the MyLab web UI: server-rendered HTML with htmx and
// Alpine.js, no build step (SPEC.md §6.3).
//
// Handlers stay thin: they parse the request, call core packages, and
// render a template.
package web

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"sync"
	"time"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/dnsrecords"
	"github.com/mylab/mylab/internal/hosttime"
	"github.com/mylab/mylab/internal/manager"
	"github.com/mylab/mylab/internal/pkgspec"
	"github.com/mylab/mylab/internal/setup"
	"github.com/mylab/mylab/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	sessionCookie   = "mylab_session"
	sessionDuration = 7 * 24 * time.Hour
	// pendingCookie links the password step of a login to the 2FA step.
	pendingCookie   = "mylab_pending"
	pendingDuration = 5 * time.Minute
	// maxTOTPAttempts before the password must be entered again.
	maxTOTPAttempts = 5
	// failedLoginDelay slows down password guessing. Caddy rate limiting is
	// the main defence (SPEC.md §6.10).
	failedLoginDelay = 500 * time.Millisecond
)

// ServiceView is one row on the dashboard.
type ServiceView struct {
	Name   string
	Title  string
	Status string // running, starting, stopped, failed, unknown
	URLs   []string
}

// Server serves the web UI.
type Server struct {
	Store *store.Store
	// Services lists installed services and their status. It is a function
	// so the UI doesn't depend on systemd directly (and tests can fake it).
	Services func(ctx context.Context) ([]ServiceView, error)
	Log      *slog.Logger

	mu sync.Mutex
	// setupToken must be entered to create the first Owner, so whoever
	// reaches the UI first on the network can't claim the lab. Empty once
	// setup is done.
	setupToken string
	// OnSetupComplete is called after the first Owner is created (e.g. to
	// delete the token file).
	OnSetupComplete func()

	// pending holds logins that passed the password check and are waiting
	// for a 2FA code, keyed by token hash. In memory: a restart just means
	// logging in again.
	pending map[string]*pendingLogin

	// Manager installs and removes services. Nil disables those pages.
	Manager ServiceManager
	// Domain is the lab's base domain, shown next to subdomain fields.
	Domain string
	// DNSProvider is the lab's current DNS provider ID (wizard default).
	DNSProvider string
	// BackupRepo is the backup destination, shown on the Backups page.
	BackupRepo string
	// HasGPU pre-ticks settings whose default is "auto".
	HasGPU bool
	// Backups shows backup status and runs backups. Nil hides the card.
	Backups BackupService
	// Setup is the first-run setup wizard. Nil disables it.
	Setup LabSetup
	// DNS reports whether home devices can reach services (SPEC.md §6.5.1).
	DNS DNSStatus
	// Activity holds live logs of host operations (optional).
	Activity *activity.Registry
	// Restores restores services from backups (Owner). Nil hides it.
	Restores RestoreService
	// Updates runs OS updates and restarts (Owner). Nil hides the card.
	Updates HostUpdates
	// Version is MyLab's version, shown on the About page.
	Version string
	// Maintenance is the lab's restart policy, shown in Settings.
	Maintenance config.Maintenance
	// bootID changes each time MyLab starts; the "restarting" page waits
	// for a new one.
	bootID string

	pages map[string]*template.Template
}

// ServiceManager is what the UI needs from internal/manager.
type ServiceManager interface {
	Catalog() ([]manager.CatalogItem, error)
	Package(name string) (*pkgspec.Package, error)
	Install(ctx context.Context, name string, req manager.InstallRequest) error
	Remove(ctx context.Context, name string) error
	DataDirs(name string) []string
	Ports(name string) ([]manager.PortStatus, error)
	SetPort(ctx context.Context, name, port string, enabled bool, hostPort int) error
	InstallStatus(ctx context.Context, name string) manager.HookStatus
	RetryInstall(ctx context.Context, name string) error
	RunAction(ctx context.Context, name, action string, inputs map[string]string) (string, error)
	CurrentSettings(name string) (map[string]any, string, error)
	UpdateSettings(ctx context.Context, name string, req manager.SettingsRequest) error
	RevealSecret(ctx context.Context, name, key string) (string, error)
	Status() manager.ApplyStatus
}

// LabSetup is what the UI needs from internal/setup.
type LabSetup interface {
	Progress(ctx context.Context) (setup.Progress, error)
	Timezones() (current string, zones []string, err error)
	SaveTimezone(ctx context.Context, name string) (changed bool, err error)
	SSHAccess() (setup.SSHAccess, error)
	SaveSSH(ctx context.Context, choice string, confirmed bool) error
	SaveRestartPolicy(ctx context.Context, mode string, hour int) error
	SaveDomain(ctx context.Context, domain, email, provider string, values map[string]string, opts setup.DomainOptions) error
	DomainLater(ctx context.Context) error
	SaveBackup(ctx context.Context, c setup.BackupChoice) error
	BackupLater(ctx context.Context) error
	RecoveryKey() (string, error)
	ConfirmRecoveryKey(ctx context.Context) error
}

// DNSStatus is what the UI needs from internal/dnsrecords.
type DNSStatus interface {
	Status() dnsrecords.Status
	Records(ctx context.Context) ([]dnsrecords.Record, error)
	SetManaging(on, acceptRisk bool) error
	TakeOver(ctx context.Context) error
}

// BackupService is what the UI needs from internal/backup.
type BackupService interface {
	Status(ctx context.Context) (backup.Status, error)
	RunNow() error
}

type pendingLogin struct {
	userID   int64
	expires  time.Time
	attempts int
}

// NewServer prepares templates. setupToken is empty if users already exist.
func NewServer(st *store.Store, services func(context.Context) ([]ServiceView, error), setupToken string, log *slog.Logger) (*Server, error) {
	s := &Server{Store: st, Services: services, Log: log, setupToken: setupToken,
		pending: map[string]*pendingLogin{}, pages: map[string]*template.Template{}}
	var err error
	if s.bootID, err = auth.NewToken(); err != nil {
		return nil, err
	}
	for _, page := range []string{"login", "totp", "setup", "twofactor", "catalog", "install", "remove", "setup-timezone", "setup-ssh",
		"setup-domain", "setup-backup", "recovery-key", "restarting", "dns-takeover",
		"home", "services", "service", "backups", "restore", "settings", "account", "users", "about",
		"settings-domain", "settings-network", "settings-updates"} {
		t, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(templateFS,
			"templates/layout.html", "templates/components.html", "templates/parts.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		s.pages[page] = t
	}
	return s, nil
}

// Handler returns all routes wrapped in the common middleware.
func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.setupSubmit)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginSubmit)
	mux.HandleFunc("POST /login/totp", s.loginTOTP)
	mux.HandleFunc("POST /logout", s.logout)

	mux.Handle("GET /account/2fa", s.requireLogin(s.twoFactorPage))
	mux.Handle("POST /account/2fa", s.requireLogin(s.twoFactorSubmit))

	mux.Handle("GET /{$}", s.require(auth.ViewDashboard, s.homePage))
	mux.Handle("GET /partials/home", s.require(auth.ViewDashboard, s.homePartial))
	mux.Handle("GET /services", s.require(auth.ViewDashboard, s.servicesPage))
	mux.Handle("GET /partials/services", s.require(auth.ViewDashboard, s.servicesPartial))
	mux.Handle("GET /services/{name}", s.require(auth.ManageServices, s.servicePage))
	mux.Handle("GET /backups", s.require(auth.ManageServices, s.backupsPage))
	mux.Handle("GET /partials/backups", s.require(auth.ManageServices, s.backupsPartial))
	mux.Handle("GET /settings", s.requireOwner(s.settingsPage))
	mux.Handle("GET /settings/people", s.requireOwner(s.usersPage))
	mux.Handle("GET /settings/remote-access", s.requireWizard(s.sshPage))
	mux.Handle("GET /settings/domain", s.requireOwner(s.domainAreaPage))
	mux.Handle("GET /settings/home-network", s.requireOwner(s.homeNetworkPage))
	mux.Handle("GET /settings/updates", s.requireOwner(s.updatesPage))
	mux.Handle("GET /settings/time-zone", s.requireWizard(s.timezonePage))
	mux.Handle("GET /account", s.requireLogin(s.accountPage))
	mux.Handle("GET /design", s.requireLogin(s.galleryPage))
	mux.Handle("GET /about", s.requireLogin(s.aboutPage))
	mux.Handle("POST /account/theme", s.requireLogin(s.themeSubmit))
	mux.Handle("POST /account/appearance", s.requireLogin(s.appearanceSubmit))
	mux.Handle("POST /account/password", s.requireLogin(s.passwordSubmit))

	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("POST /dns/manage", s.requireOwner(s.dnsManage))
	mux.Handle("GET /dns/take-over", s.requireOwner(s.dnsTakeOverPage))
	mux.Handle("POST /dns/take-over", s.requireOwner(s.dnsTakeOver))
	mux.Handle("GET /setup/timezone", s.requireWizard(s.timezonePage))
	mux.Handle("POST /setup/timezone", s.requireWizard(s.timezoneSubmit))
	mux.Handle("GET /activity/{kind}/{part}", s.require(auth.ManageServices, s.activityPart))
	mux.Handle("GET /users", s.requireOwner(s.usersPage))
	mux.Handle("POST /users", s.requireOwner(s.userAddSubmit))
	mux.Handle("POST /users/{id}/role", s.requireOwner(s.userRoleSubmit))
	mux.Handle("POST /users/{id}/reset-password", s.requireOwner(s.userResetPasswordSubmit))
	mux.Handle("POST /users/{id}/reset-2fa", s.requireOwner(s.userReset2FASubmit))
	mux.Handle("POST /users/{id}/remove", s.requireOwner(s.userRemoveSubmit))
	mux.Handle("GET /backups/restore/{name}", s.requireOwner(s.restorePage))
	mux.Handle("POST /backups/restore/{name}", s.requireOwner(s.restoreSubmit))
	mux.Handle("POST /backups/restore/{name}/kept/remove", s.requireOwner(s.removeKeptSubmit))
	mux.Handle("POST /settings/updates/run", s.requireOwner(s.updateNowSubmit))
	mux.Handle("POST /settings/restart", s.requireOwner(s.restartNowSubmit))
	mux.Handle("POST /settings/restart-policy", s.requireWizard(s.restartPolicySubmit))
	mux.Handle("GET /setup/ssh", s.requireWizard(s.sshPage))
	mux.Handle("POST /setup/ssh", s.requireWizard(s.sshSubmit))
	mux.Handle("GET /setup/domain", s.requireWizard(s.domainPage))
	mux.Handle("POST /setup/domain", s.requireWizard(s.domainSubmit))
	mux.Handle("POST /setup/domain/later", s.requireWizard(s.domainLater))
	mux.Handle("GET /setup/backup", s.requireWizard(s.backupPage))
	mux.Handle("POST /setup/backup", s.requireWizard(s.backupSubmit))
	mux.Handle("POST /setup/backup/later", s.requireWizard(s.backupLater))
	mux.Handle("GET /setup/recovery-key", s.requireWizard(s.recoveryPage))
	mux.Handle("POST /setup/recovery-key", s.requireWizard(s.recoveryConfirm))
	mux.Handle("GET /restarting", s.requireLogin(s.restartingPage))

	mux.Handle("POST /backup/run", s.require(auth.ManageServices, s.backupRun))
	mux.Handle("GET /catalog", s.require(auth.ManageServices, s.catalogPage))
	mux.Handle("GET /install/{name}", s.require(auth.ManageServices, s.installPage))
	mux.Handle("POST /install/{name}", s.require(auth.ManageServices, s.installSubmit))
	mux.Handle("POST /services/{name}/ports/{port}", s.require(auth.ManageServices, s.portSubmit))
	mux.Handle("POST /services/{name}/settings", s.require(auth.ManageServices, s.settingsSubmit))
	mux.Handle("POST /services/{name}/reveal/{key}", s.require(auth.ManageServices, s.revealSubmit))
	mux.Handle("POST /services/{name}/setup/retry", s.require(auth.ManageServices, s.retrySetupSubmit))
	mux.Handle("POST /services/{name}/actions/{action}", s.require(auth.ManageServices, s.actionSubmit))
	mux.Handle("GET /services/{name}/remove", s.require(auth.ManageServices, s.removePage))
	mux.Handle("POST /services/{name}/remove", s.require(auth.ManageServices, s.removeSubmit))

	return s.recoverPanics(securityHeaders(sameOrigin(s.loadSession(mux))))
}

type userKey struct{}

// currentUser returns the logged-in user, or nil.
func currentUser(r *http.Request) *store.User {
	u, _ := r.Context().Value(userKey{}).(*store.User)
	return u
}

func (s *Server) loadSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
			u, err := s.Store.SessionUser(r.Context(), auth.HashToken(c.Value))
			switch {
			case err == nil:
				r = r.WithContext(context.WithValue(r.Context(), userKey{}, u))
			case !errors.Is(err, store.ErrNotFound):
				s.serverError(w, err)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireLogin checks the user is logged in.
func (s *Server) requireLogin(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.needsSetup() {
			redirect(w, r, "/setup")
			return
		}
		if currentUser(r) == nil {
			redirect(w, r, "/login")
			return
		}
		h(w, r)
	})
}

// require checks the user is logged in and has a permission. Owners must
// set up two-factor authentication before doing anything else (SPEC.md §5).
func (s *Server) require(p auth.Permission, h http.HandlerFunc) http.Handler {
	return s.requireLogin(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u.Role == auth.RoleOwner && u.TOTPSecret == "" {
			redirect(w, r, "/account/2fa")
			return
		}
		if u.MustChangePassword {
			redirect(w, r, "/account")
			return
		}
		// Owners finish the setup wizard next.
		if u.Role == auth.RoleOwner && s.Setup != nil {
			if p, err := s.Setup.Progress(r.Context()); err == nil && p.Next() != "" {
				redirect(w, r, p.Next())
				return
			}
		}
		if !auth.Can(u.Role, p) {
			http.Error(w, "You don't have permission to do that.", http.StatusForbidden)
			return
		}
		h(w, r)
	})
}

// sameOrigin rejects cross-site form posts (CSRF). Browsers always send an
// Origin header on POST; requests without one are rejected.
func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			o, err := url.Parse(r.Header.Get("Origin"))
			if err != nil || o.Host == "" || o.Host != r.Host {
				http.Error(w, "Cross-site request rejected.", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders sets browser protections on every response.
//
// Alpine.js's standard build needs 'unsafe-eval'; switching to its CSP
// build would remove that (docs/ASSUMPTIONS.md K1).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-eval'; "+
			"style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// recoverPanics turns a crash in one request into a logged error and an
// error page, instead of the browser getting an empty response.
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Log.Error("request crashed", "method", r.Method, "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				http.Error(w, "Something went wrong. Details are in the MyLab log.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// redirect sends the browser elsewhere. For htmx background requests (like
// the dashboard's live refresh) a normal redirect would be followed
// silently and the other page's HTML swapped into the current page, so htmx
// is told to navigate the whole page instead.
func redirect(w http.ResponseWriter, r *http.Request, url string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

func (s *Server) needsSetup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setupToken != ""
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User) error {
	token, err := auth.NewToken()
	if err != nil {
		return err
	}
	expires := time.Now().Add(sessionDuration)
	if err := s.Store.CreateSession(r.Context(), auth.HashToken(token), u.ID, expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
	return nil
}

// isHTTPS reports whether the browser connection is HTTPS. Behind Caddy the
// request arrives over plain HTTP with X-Forwarded-Proto set.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) render(w http.ResponseWriter, page string, data map[string]any) {
	if u, ok := data["User"].(*store.User); ok && u != nil {
		data["Nav"] = navFor(u, page)
		// For the banner shown when the browser's clock differs from the
		// host's (every time MyLab shows is host time).
		if page != "setup-timezone" {
			data["Clock"] = clockView{hosttime.Now(), auth.Can(u.Role, auth.ManageHost)}
		}
	}
	// Render into a buffer first, so a template error becomes an error
	// page rather than half a page sent with 200 OK.
	var buf bytes.Buffer
	if err := s.pages[page].Execute(&buf, data); err != nil {
		s.Log.Error("render", "page", page, "err", err)
		http.Error(w, "Something went wrong showing this page. Details are in the MyLab log.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.Log.Error("request failed", "err", err)
	http.Error(w, "Something went wrong. Details are in the MyLab log.", http.StatusInternalServerError)
}
