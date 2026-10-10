// Package web is the Roostack web UI: server-rendered HTML with htmx and
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

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/hosttime"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	sessionCookie   = "roostack_session"
	sessionDuration = 7 * 24 * time.Hour
	// pendingCookie links the password step of a login to the 2FA step.
	pendingCookie   = "roostack_pending"
	pendingDuration = 5 * time.Minute
	// failedLoginDelay slows down password guessing. Caddy rate limiting is
	// the main defence (SPEC.md §6.10).
	failedLoginDelay = 500 * time.Millisecond
)

// StatusWaiting is a service waiting to be restored: its server is gone
// (SPEC.md §10.10).
const StatusWaiting = "waiting"

// ServiceView is one row on the dashboard.
type ServiceView struct {
	Name   string
	Title  string
	Status string // running, starting, stopped, failed, unknown
	URLs   []string
	// NoAddress: it has web routes, but the lab has no address yet, so it
	// can't be reached on the web (SPEC.md §6.6).
	NoAddress bool
	// Server is the name of the server it runs on.
	Server string
	// WasOn names the server a service waiting to be restored ran on
	// (Status is StatusWaiting).
	WasOn string
	// What its server reports: containers, ports, routes, data folders
	// and size (nothing about a service should look like magic).
	node.ServiceDetail
	// LastBackup is when its server last finished a backup.
	LastBackup time.Time
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
	// stepUpFailures counts wrong passwords or codes per user when showing
	// the recovery key (recoverykey.go).
	stepUpFailures map[int64]int
	// codeMu makes two-factor checks run one at a time (checkCode).
	codeMu sync.Mutex

	// Manager installs and removes services. Nil disables those pages.
	Manager ServiceManager
	// Node is the server running the services. Set with Manager.
	Node ServiceNode
	// Network adds servers to the lab (SPEC.md §10.2). Nil hides it.
	Network LabNetwork
	// LabName is what people call the lab (config.Lab.DisplayName).
	LabName string
	// Domain is the lab's base domain, shown next to subdomain fields.
	Domain string
	// UIHost is where Roostack's own pages are (config.Lab.UIHost).
	UIHost string
	// DetectNetwork guesses where the lab is from this server's addresses
	// (public.DetectNetwork; SPEC.md §6.12). Nil guesses nothing.
	DetectNetwork func() string
	// DNSProvider is the lab's current DNS provider ID (wizard default).
	DNSProvider string
	// OldAddress is the lab's previous address, still served for a while
	// after switching (config.Lab.OldAddress; SPEC.md §6.6).
	OldAddress *config.OldAddress
	// Cloud is the controller's link to Roostack Cloud (SPEC.md §6.6). Nil
	// when Cloud is turned off (config.Cloud): nothing about it is offered.
	Cloud CloudLink
	// BackupRepo is the backup destination, shown on the Backups page.
	BackupRepo string
	// HasGPU pre-ticks settings whose default is "auto".
	HasGPU bool
	// Setup is the first-run setup wizard. Nil disables it.
	Setup LabSetup
	// LabRestore rebuilds a lab from its backups on a fresh install. Nil
	// hides it.
	LabRestore LabRestorer
	restore    *restoreSession
	// setupPasses are browsers that entered the setup code (setup.go):
	// token hash → until when.
	setupPasses map[string]time.Time
	hostChecks  hostCheckCache
	// RestorePending reports a restored lab whose services' databases are
	// still being put back (optional).
	RestorePending func() bool
	// DNS reports whether home devices can reach services (SPEC.md §6.5.1).
	DNS DNSStatus
	// Checks are the outside check links for public addresses, and
	// Protected says whether CrowdSec is running (SPEC.md §6.11; public.go).
	Checks    *public.Checks
	Protected func(ctx context.Context) bool
	// JoinLab adds this fresh server to an existing lab, named name, and
	// restarts Roostack as part of it once the page has been sent; it
	// returns this server's address on the lab's network. Undone if it
	// fails. Nil hides the choice (SPEC.md §10.2).
	JoinLab func(ctx context.Context, code labnet.JoinCode, name string) (string, error)
	// Mail sends email (SPEC.md §6.13). Nil hides Settings → Email.
	Mail MailSender
	// Attacks says what CrowdSec saw on the public addresses over a while,
	// for the weekly summary (optional).
	Attacks func(ctx context.Context, since time.Duration) (public.Attacks, error)
	// Version is Roostack's version, shown in every page's footer and on
	// the About page.
	Version string
	// Maintenance is the lab's restart policy, shown in Settings.
	Maintenance config.Maintenance
	// RunningJob names a maintenance job running on this server, which a
	// restart waits for ("" if none; optional).
	RunningJob func() string
	// bootID changes each time Roostack starts; the "restarting" page waits
	// for a new one.
	bootID string
	// help is the embedded help site (help.go).
	help *helpSite

	pages map[string]*template.Template
}

// ServiceManager is what the UI needs from internal/manager, by area.
type ServiceManager interface {
	ServiceCatalog
	LabServers
	BackupPlan
	StorageSharing
	PublicAccess
	RecoveryKeys
	LabRestores
}

// LabRestores restores a service from any of the lab's backups, onto any
// server (SPEC.md §10.10, "Restoring across the lab").
type LabRestores interface {
	ServiceBackups(ctx context.Context, name string) ([]manager.BackupMoment, []string, error)
	RestoreService(ctx context.Context, name, id, to string) error
	Restoring(name string) bool
	// A server that's gone: its services' plan, restoring them, and
	// removing one that waits (with its backups).
	LostServerPlan(ctx context.Context, id string) (manager.LostPlan, error)
	RestoreLostServer(ctx context.Context, id string, names []string, to string) error
	RemoveWaiting(ctx context.Context, name string) error
}

// ServiceCatalog is the catalog and the installed services: installing,
// settings, ports and moving between servers.
type ServiceCatalog interface {
	Catalog() ([]manager.CatalogItem, error)
	Package(name string) (*pkgspec.Package, error)
	// ServicePackage is an installed service's package (its name can be
	// a second copy's own), titled as the service.
	ServicePackage(name string) (*pkgspec.Package, error)
	// SetTitle changes what people call a service (never its name).
	SetTitle(ctx context.Context, name, title string) error
	Install(ctx context.Context, name string, req manager.InstallRequest) error
	Remove(ctx context.Context, name string) error
	DataDirs(name string) []string
	Ports(name string) ([]manager.PortStatus, error)
	SetPort(ctx context.Context, name, port string, enabled bool, hostPort int) error
	CurrentSettings(name string) (map[string]any, string, error)
	UpdateSettings(ctx context.Context, name string, req manager.SettingsRequest) error
	RetryInstallWith(ctx context.Context, name string, settings, secrets map[string]string) error
	Status() manager.ApplyStatus
	// NodeFor is the server a service runs on (nil if none).
	NodeFor(service string) node.Node
	MoveInfo(ctx context.Context, name string) (manager.MoveInfo, error)
	MoveService(ctx context.Context, name, to, way string) error
}

// LabServers are the lab's servers (SPEC.md §10): listing, renaming and
// removing them, their built-in services and the data moves leave behind.
type LabServers interface {
	Servers() ([]manager.Server, error)
	// NodeByID is a server by ID (nil if none).
	NodeByID(id string) node.Node
	AllBuiltIns(ctx context.Context) []manager.ServerBuiltIns
	RemoveServer(ctx context.Context, id string) error
	RenameServer(ctx context.Context, id, name string) error
	KeptData(ctx context.Context, serverID string) ([]node.KeptFolder, error)
	RemoveKeptData(ctx context.Context, serverID, path string) error
	// Direct links between servers for backups (SPEC.md §10.10): where a
	// server is reached directly (typed by the Owner), and how one server
	// reaches another's backup store.
	LinkAddress(ctx context.Context, id string) string
	SetLinkAddress(ctx context.Context, id, addr string) error
	LinkPath(ctx context.Context, from, to string) string
	LinkFailed(ctx context.Context, a, b string) bool
	ApplyBecause(reason string)
}

// BackupPlan is each server's backups (SPEC.md §6.9): the main one, its
// copies and the places they can go.
type BackupPlan interface {
	BackupChoices(ctx context.Context) ([]manager.BackupChoice, error)
	BackupTargetOf(ctx context.Context, serverID string) (store.BackupTarget, string, bool)
	BackupCopiesOf(ctx context.Context, serverID string) []manager.BackupCopy
	AddBackupCopy(ctx context.Context, serverID, ref string) error
	PromoteBackupCopy(ctx context.Context, serverID, ref string) error
	BackupKind(ctx context.Context, serverID, ref string) string
	RemoveBackupCopy(ctx context.Context, serverID, ref string) error
	SetBackupDestination(ctx context.Context, serverID, ref string) error
	AddS3Provider(ctx context.Context, name string, p manager.S3Provider) error
}

// StorageSharing is storage for other labs, and this lab's storage at
// other labs (SPEC.md §10.10).
type StorageSharing interface {
	OfferStorage(ctx context.Context) bool
	StorageGuests(ctx context.Context) ([]manager.GuestView, error)
	InviteGuest(ctx context.Context, name string, quota int64, endpoint string) (labnet.StorageInvite, error)
	RemoveGuest(ctx context.Context, id int64) error
	DeleteRemovedGuest(ctx context.Context, id int64) error
	GuestServer(ctx context.Context) string
	SetGuestServer(ctx context.Context, id string) error
	EndingStorages(ctx context.Context) []manager.EndingStorage
	SetGuestQuota(ctx context.Context, id, quota int64) error
	GuestSpaceLeft(ctx context.Context) (manager.GuestSpace, bool)
	LeftoverGuests(ctx context.Context) ([]manager.LeftoverGuest, error)
	DeleteLeftoverGuest(ctx context.Context, user string) error
	AddRemoteStorage(ctx context.Context, code string) error
	RemoteStorages(ctx context.Context) []manager.RemoteStorage
	RemoteUsage(ctx context.Context, r manager.RemoteStorage) (labnet.GuestUsage, error)
}

// ServiceNode is what the UI needs from the server running a service
// (internal/node): its setup, actions and secrets.
type ServiceNode interface {
	Identity() nodeid.Identity
	InstallStatus(ctx context.Context, name string) manager.HookStatus
	RetryInstall(ctx context.Context, name string) error
	RunAction(ctx context.Context, name, action string, inputs map[string]string) (string, error)
	RevealSecret(ctx context.Context, name, key string) (string, error)
	LatestActivity(kind string) (activity.Snapshot, bool)
	Backups() node.Backups
	Restores() node.Restores
	Updates() node.Updates
	Settings() node.Settings
}

// LabNetwork is what the UI needs from labnet.Hub.
type LabNetwork interface {
	NewJoinCode(ctx context.Context, endpoint, dial string) (labnet.JoinCode, error)
}

// LabSetup is what the UI needs from internal/setup.
type LabSetup interface {
	Progress(ctx context.Context) (setup.Progress, error)
	// Done marks a server settings step (setup.Step*) done.
	Done(ctx context.Context, step string) error
	SaveDomain(ctx context.Context, domain, email, provider string, values map[string]string, opts setup.DomainOptions) error
	DomainLater(ctx context.Context) error
	// EndOldAddress stops the previous address working now, and restarts.
	EndOldAddress(ctx context.Context) error
	// BackupAdded records the wizard's backup step done (first: its first
	// backup, so the recovery key must be confirmed).
	BackupAdded(ctx context.Context, first bool) error
	// BackupDrives lists the drives backups can go on, best first;
	// EraseDrive erases a disk and prepares it.
	BackupDrives(ctx context.Context) ([]agent.Drive, error)
	EraseDrive(ctx context.Context, disk string) error
	BackupLater(ctx context.Context) error
	RecoveryKey() (string, error)
	ConfirmRecoveryKey(ctx context.Context) error
	// PendingRestart: wizard steps' settings wait for the restart at its end.
	PendingRestart() bool
	SetLabName(ctx context.Context, name string, restart bool) error
	// Network is where the lab is, saved and guessed (detect guesses);
	// SetNetwork saves it and restarts Roostack (SPEC.md §6.12).
	Network(detect func() string) setup.NetworkChoice
	SetNetwork(network string, uiTunnelOnly bool) error
}

// DNSStatus is what the UI needs from internal/dnsrecords.
type DNSStatus interface {
	Status() dnsrecords.Status
	Records(ctx context.Context) ([]dnsrecords.Record, error)
	SetManaging(on, acceptRisk bool) error
	TakeOver(ctx context.Context) error
	// Refresh syncs the records soon (after a change).
	Refresh()
}

// The server's maintenance jobs (internal/node). Each is nil when not
// available, which hides its card or page.
type (
	BackupService  = node.Backups
	RestoreService = node.Restores
	HostUpdates    = node.Updates
)

// The maintenance jobs and settings of the server a request is about
// (serverOf), or nil.

func (s *Server) backups(r *http.Request) BackupService {
	if n := s.serverOf(r); n != nil {
		return n.Backups()
	}
	return nil
}

func (s *Server) restores(r *http.Request) RestoreService {
	if n := s.serverOf(r); n != nil {
		return n.Restores()
	}
	return nil
}

func (s *Server) server(r *http.Request) node.Settings {
	if n := s.serverOf(r); n != nil {
		return n.Settings()
	}
	return nil
}

func (s *Server) updates(r *http.Request) HostUpdates {
	if n := s.serverOf(r); n != nil {
		return n.Updates()
	}
	return nil
}

type pendingLogin struct {
	userID   int64
	expires  time.Time
	remember bool // "keep me signed in" was ticked
}

// NewServer prepares templates. setupToken is empty if users already exist.
func NewServer(st *store.Store, services func(context.Context) ([]ServiceView, error), setupToken string, log *slog.Logger) (*Server, error) {
	s := &Server{Store: st, Services: services, Log: log, setupToken: setupToken,
		pending: map[string]*pendingLogin{}, stepUpFailures: map[int64]int{}, pages: map[string]*template.Template{},
		setupPasses: map[string]time.Time{}}
	var err error
	if s.bootID, err = authkit.NewToken(); err != nil {
		return nil, err
	}
	if s.help, err = newHelpSite(helpFS); err != nil {
		return nil, err
	}
	for _, page := range []string{"login", "totp", "setup", "twofactor", "catalog", "install", "remove", "setup-timezone", "setup-ssh",
		"setup-domain", "setup-backup", "recovery-key", "restarting", "dns-takeover", "setup-restore", "setup-joined",
		"home", "services", "service", "backups", "restore", "settings-server-gone", "settings-server-unreachable", "settings", "account", "account-appearance", "account-notifications", "users", "about",
		"settings-domain", "settings-network", "settings-email", "settings-history", "settings-history-event", "settings-updates", "settings-servers", "move", "settings-storage", "settings-lab",
		"backups-recovery-key", "service-public", "settings-public", "backups-key-change", "settings-cloud", "cloud-open", "cloud-linked"} {
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
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(static)))
	mux.Handle("GET /help/", s.help.Handler())
	mux.Handle("GET /help", http.RedirectHandler("/help/", http.StatusMovedPermanently))

	mux.HandleFunc("GET /setup", s.setupPage)
	mux.HandleFunc("POST /setup", s.setupSubmit)
	mux.HandleFunc("POST /setup/code", s.setupCodeSubmit)
	mux.HandleFunc("POST /setup/join", s.setupJoinSubmit)
	mux.HandleFunc("GET /setup/restore", s.labRestorePage)
	mux.HandleFunc("POST /setup/restore", s.labRestoreFind)
	mux.HandleFunc("POST /setup/restore/start", s.labRestoreStart)
	mux.HandleFunc("GET /setup/restore/progress", s.labRestoreProgress)
	// The outside check link, reached only through Caddy on a public
	// address (public.go): no login.
	mux.HandleFunc("GET /.well-known/roostack-check/{token}", s.publicCheckVisit)
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
	mux.Handle("GET /settings/lab", s.requireOwner(s.labNamePage))
	mux.Handle("POST /settings/lab", s.requireOwner(s.labNameSubmit))
	mux.Handle("GET /settings/remote-access", s.requireWizard(s.sshPage))
	mux.Handle("GET /settings/domain", s.requireOwner(s.domainAreaPage))
	mux.Handle("GET /settings/home-network", s.requireOwner(s.homeNetworkPage))
	mux.Handle("POST /settings/network", s.requireOwner(s.networkSubmit))
	mux.Handle("GET /settings/servers", s.requireOwner(s.serversPage))
	mux.Handle("GET /settings/servers/{server}", s.requireOwner(s.serverPage))
	mux.Handle("GET /settings/servers/{server}/disk", s.requireOwner(s.diskPart))
	s.serverRoutes(mux)
	mux.Handle("POST /settings/servers/{server}/rename", s.requireOwner(s.renameServerSubmit))
	mux.Handle("POST /settings/servers/{server}/link-address", s.requireOwner(s.linkAddressSubmit))
	mux.Handle("POST /settings/servers/{server}/remove", s.requireOwner(s.removeServerSubmit))
	mux.Handle("POST /settings/servers/{server}/kept/remove", s.requireOwner(s.removeKeptDataSubmit))
	mux.Handle("GET /settings/servers/{server}/gone", s.requireOwner(s.serverGonePage))
	mux.Handle("POST /settings/servers/{server}/gone", s.requireOwner(s.serverGoneSubmit))
	mux.Handle("POST /services/{name}/remove-waiting", s.requireOwner(s.removeWaitingSubmit))
	mux.Handle("POST /settings/signing-in", s.requireOwner(s.sessionSettingsSubmit))
	mux.Handle("GET /settings/history", s.requireOwner(s.historyPage))
	mux.Handle("GET /settings/history/{server}/{id}", s.requireOwner(s.historyEventPage))
	mux.Handle("POST /settings/history/keep", s.requireOwner(s.historyKeepSubmit))
	mux.Handle("POST /settings/history/delete", s.requireOwner(s.historyDeleteSubmit))
	mux.Handle("GET /settings/email", s.requireOwner(s.emailPage))
	mux.Handle("POST /settings/email", s.requireOwner(s.emailSubmit))
	mux.Handle("POST /settings/email/test", s.requireOwner(s.emailTestSubmit))
	mux.Handle("GET /account/notifications", s.requireLogin(s.notificationsPage))
	mux.Handle("POST /account/notifications", s.requireLogin(s.notificationsSubmit))
	mux.Handle("GET /account/appearance", s.requireLogin(s.appearancePage))
	mux.Handle("GET /settings/storage", s.requireOwner(s.storagePage))
	mux.Handle("POST /settings/storage/invite", s.requireOwner(s.inviteGuestSubmit))
	mux.Handle("POST /settings/storage/guests/{id}/remove", s.requireOwner(s.removeGuestSubmit))
	mux.Handle("POST /settings/storage/guests/{id}/delete", s.requireOwner(s.deleteRemovedGuestSubmit))
	mux.Handle("POST /settings/storage/server", s.requireOwner(s.guestServerSubmit))
	mux.Handle("POST /settings/storage/guests/{id}/quota", s.requireOwner(s.guestQuotaSubmit))
	mux.Handle("POST /settings/storage/leftover/{user}/delete", s.requireOwner(s.deleteLeftoverSubmit))
	mux.Handle("GET /services/{name}/move", s.requireOwner(s.movePage))
	mux.Handle("GET /services/{name}/public/{route}", s.requireOwner(s.publicPage))
	mux.Handle("POST /services/{name}/public/{route}", s.requireOwner(s.publicSubmit))
	mux.Handle("POST /services/{name}/public/{route}/check", s.requireOwner(s.publicCheckNew))
	mux.Handle("GET /settings/public", s.requireOwner(s.publicSettingsPage))
	mux.Handle("POST /settings/public/sharing", s.requireOwner(s.publicSharingSubmit))
	mux.Handle("POST /services/{name}/move", s.requireOwner(s.moveSubmit))
	mux.Handle("POST /settings/servers/join", s.requireOwner(s.joinCodeSubmit))
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
	// The People page moved to Settings; its forms still post to /users.
	mux.Handle("GET /users", http.RedirectHandler("/settings/people", http.StatusMovedPermanently))
	mux.Handle("POST /users", s.requireOwner(s.userAddSubmit))
	mux.Handle("POST /users/{id}/role", s.requireOwner(s.userRoleSubmit))
	mux.Handle("POST /users/{id}/reset-password", s.requireOwner(s.userResetPasswordSubmit))
	mux.Handle("POST /users/{id}/reset-2fa", s.requireOwner(s.userReset2FASubmit))
	mux.Handle("POST /users/{id}/remove", s.requireOwner(s.userRemoveSubmit))
	mux.Handle("GET /backups/recovery-key", s.requireOwner(s.recoveryKeyShowPage))
	mux.Handle("POST /backups/recovery-key", s.requireOwner(s.recoveryKeyShowSubmit))
	mux.Handle("GET /backups/recovery-key/change", s.requireOwner(s.keyChangePage))
	mux.Handle("POST /backups/recovery-key/change", s.requireOwner(s.keyChangeSubmit))
	mux.Handle("POST /backups/recovery-key/change/confirm", s.requireOwner(s.keyChangeConfirm))
	mux.Handle("POST /backups/recovery-key/change/cancel", s.requireOwner(s.keyChangeCancel))
	mux.Handle("POST /backups/keep-this-copy", s.requireOwner(s.keepThisCopySubmit))
	mux.Handle("GET /backups/restore/{name}", s.requireOwner(s.restorePage))
	mux.Handle("POST /backups/restore/{name}", s.requireOwner(s.restoreSubmit))
	mux.Handle("POST /backups/restore/{name}/kept/remove", s.requireOwner(s.removeKeptSubmit))
	mux.Handle("POST /settings/updates/run", s.requireOwner(s.updateNowSubmit))
	mux.Handle("POST /settings/host-changes/known", s.requireOwner(s.hostChangesKnownSubmit))
	mux.Handle("POST /settings/restart", s.requireOwner(s.restartNowSubmit))
	mux.Handle("POST /settings/restart-policy", s.requireWizard(s.restartPolicySubmit))
	mux.Handle("GET /setup/ssh", s.requireWizard(s.sshPage))
	mux.Handle("POST /setup/ssh", s.requireWizard(s.sshSubmit))
	mux.Handle("GET /setup/domain", s.requireWizard(s.domainPage))
	mux.Handle("POST /setup/domain", s.requireWizard(s.domainSubmit))
	mux.Handle("POST /setup/domain/later", s.requireWizard(s.domainLater))
	mux.Handle("POST /settings/domain/old/end", s.requireWizard(s.oldAddressEnd))
	mux.Handle("POST /setup/address/roost-ac", s.requireWizard(s.roostAcSubmit))
	mux.Handle("GET /settings/cloud", s.requireCloud(s.cloudPage))
	mux.Handle("GET /settings/cloud/live", s.requireCloud(s.cloudLive))
	mux.Handle("GET /settings/cloud/linked", s.requireCloud(s.cloudLinkedPage))
	mux.Handle("GET /setup/address/linked", s.requireCloud(s.cloudLinkedPage))
	mux.Handle("GET /settings/cloud/progress", s.requireCloud(s.cloudProgress))
	mux.Handle("GET /settings/cloud/name", s.requireCloud(s.cloudNameCheck))
	mux.Handle("POST /settings/cloud/link", s.requireCloud(s.cloudLinkSubmit))
	mux.Handle("POST /settings/cloud/cancel", s.requireCloud(s.cloudCancelSubmit))
	mux.Handle("POST /settings/cloud/unlink", s.requireCloud(s.cloudUnlinkSubmit))
	mux.Handle("POST /settings/cloud/dismiss", s.requireCloud(s.cloudDismissSubmit))
	mux.Handle("GET /setup/backup", s.requireWizard(s.backupPage))
	mux.Handle("POST /setup/backup", s.requireWizard(s.backupSubmit))
	mux.Handle("POST /setup/backup/later", s.requireWizard(s.backupLater))
	mux.Handle("GET /setup/backup/drives", s.requireWizard(s.drivesPartial))
	mux.Handle("POST /setup/backup/add", s.requireWizard(s.planAddSubmit(true)))
	mux.Handle("POST /backups/plan", s.requireOwner(s.planAddSubmit(false)))
	mux.Handle("POST /backups/plan/promote", s.requireOwner(s.planPromoteSubmit))
	mux.Handle("POST /settings/servers/{server}/location", s.requireOwner(s.locationSubmit))
	mux.Handle("POST /backups/plan/remove", s.requireOwner(s.planRemoveSubmit))
	mux.Handle("POST /setup/backup/erase", s.requireWizard(s.eraseDriveSubmit))
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
	mux.Handle("POST /services/{name}/setup/retry-with", s.require(auth.ManageServices, s.retryWithSubmit))
	mux.Handle("POST /services/{name}/actions/{action}", s.require(auth.ManageServices, s.actionSubmit))
	mux.Handle("GET /services/{name}/remove", s.require(auth.ManageServices, s.removePage))
	mux.Handle("POST /services/{name}/title", s.require(auth.ManageServices, s.titleSubmit))
	mux.Handle("POST /services/{name}/remove", s.require(auth.ManageServices, s.removeSubmit))

	return s.recoverPanics(compress(securityHeaders(sameOrigin(s.loadSession(mux)))))
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
			hash := authkit.HashToken(c.Value)
			u, lastSeen, remember, err := s.Store.SessionInfo(r.Context(), hash)
			switch {
			case err == nil:
				r = s.checkSession(r, hash, u, lastSeen, remember)
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
			if sessionOf(r).Expired {
				redirect(w, r, "/login?expired=1")
			} else {
				redirect(w, r, "/login")
			}
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
				http.Error(w, "Something went wrong. Details are in the Roostack log.", http.StatusInternalServerError)
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

// labName is LabName, which first-run setup may change while pages are
// being served.
func (s *Server) labName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.LabName
}

func (s *Server) needsSetup() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setupToken != ""
}

// startSession signs u in. remember ("keep me signed in", if offered)
// makes it last rememberDuration on this browser without the idle limit;
// otherwise the cookie ends with the browser (session.go).
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User, remember bool) error {
	token, err := authkit.NewToken()
	if err != nil {
		return err
	}
	remember = remember && s.offerRemember(r.Context())
	expires := time.Now().Add(sessionDuration)
	if remember {
		expires = time.Now().Add(rememberDuration)
	}
	if err := s.Store.CreateSessionRemember(r.Context(), authkit.HashToken(token), u.ID, expires, remember); err != nil {
		return err
	}
	c := &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r)}
	if remember {
		c.Expires = expires
	}
	http.SetCookie(w, c)
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
		// Every page's footer shows Roostack's version.
		if _, set := data["Version"]; !set {
			data["Version"] = s.Version
		}
		if _, set := data["LabName"]; !set {
			data["LabName"] = s.labName()
		}
		// For the banner shown when the browser's clock differs from the
		// host's (every time Roostack shows is host time).
		// Not while changing it, nor while restarting to use a new one (the
		// old zone is still in effect).
		if page != "setup-timezone" && page != "restarting" {
			data["Clock"] = clockView{hosttime.Now(), auth.Can(u.Role, auth.ManageHost)}
		}
	}
	// Settings' sidebar leaves out Roostack Cloud while it isn't in use.
	if nav, ok := data["AreaNav"].(areaNav); ok && nav.Label == "Settings" {
		nav.Groups = s.settingsGroups()
		data["AreaNav"] = nav
	}
	// Render into a buffer first, so a template error becomes an error
	// page rather than half a page sent with 200 OK.
	var buf bytes.Buffer
	if err := s.pages[page].Execute(&buf, data); err != nil {
		s.Log.Error("render", "page", page, "err", err)
		http.Error(w, "Something went wrong showing this page. Details are in the Roostack log.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.Log.Error("request failed", "err", err)
	http.Error(w, "Something went wrong. Details are in the Roostack log.", http.StatusInternalServerError)
}
