// Package web is the MyLab web UI: server-rendered HTML with htmx and
// Alpine.js, no build step (SPEC.md §6.3).
//
// Handlers stay thin: they parse the request, call core packages, and
// render a template.
package web

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/backup"
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
	// HasGPU pre-ticks settings whose default is "auto".
	HasGPU bool
	// Backups shows backup status and runs backups. Nil hides the card.
	Backups BackupService
	// Setup is the first-run setup wizard. Nil disables it.
	Setup LabSetup
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
	Status() manager.ApplyStatus
}

// LabSetup is what the UI needs from internal/setup.
type LabSetup interface {
	Progress(ctx context.Context) (setup.Progress, error)
	SaveDomain(ctx context.Context, domain, email, token string) error
	DomainLater(ctx context.Context) error
	SaveBackup(ctx context.Context, c setup.BackupChoice) error
	BackupLater(ctx context.Context) error
	RecoveryKey() (string, error)
	ConfirmRecoveryKey(ctx context.Context) error
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
	for _, page := range []string{"login", "totp", "setup", "dashboard", "twofactor", "catalog", "install", "remove",
		"setup-domain", "setup-backup", "recovery-key", "restarting"} {
		t, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(templateFS, "templates/layout.html", "templates/"+page+".html")
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

	mux.Handle("GET /{$}", s.require(auth.ViewDashboard, s.dashboard))
	mux.Handle("GET /partials/services", s.require(auth.ViewDashboard, s.servicesPartial))

	mux.HandleFunc("GET /healthz", s.healthz)
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
	mux.Handle("GET /services/{name}/remove", s.require(auth.ManageServices, s.removePage))
	mux.Handle("POST /services/{name}/remove", s.require(auth.ManageServices, s.removeSubmit))

	return securityHeaders(sameOrigin(s.loadSession(mux)))
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
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
			return
		}
		if currentUser(r) == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
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
			http.Redirect(w, r, "/account/2fa", http.StatusSeeOther)
			return
		}
		// Owners finish the setup wizard next.
		if u.Role == auth.RoleOwner && s.Setup != nil {
			if p, err := s.Setup.Progress(r.Context()); err == nil && p.Next() != "" {
				http.Redirect(w, r, p.Next(), http.StatusSeeOther)
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages[page].Execute(w, data); err != nil {
		s.Log.Error("render", "page", page, "err", err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.Log.Error("request failed", "err", err)
	http.Error(w, "Something went wrong. Details are in the MyLab log.", http.StatusInternalServerError)
}
