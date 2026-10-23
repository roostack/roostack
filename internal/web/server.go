// Package web is the Roostack web UI: server-rendered HTML with htmx and
// Alpine.js, no build step (SPEC.md §6.3).
//
// Handlers stay thin: they parse the request, call core packages, and
// render a template.
package web

import (
	"context"
	"embed"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/editor"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

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
	// Update is the app version it can update to ("" if none, SPEC.md §7).
	Update string
	// Package is the name of the package it was installed from (a second
	// copy's Name is its own, SPEC.md §8.2).
	Package string
	// Local: installed from a package the Owner added themselves ("Your
	// own", SPEC.md §8.3.4).
	Local bool
	// Tier is who looks after its package in the service registry now
	// (registry.TierOfficial or TierCommunity), for its badge (SPEC.md
	// §8.3.4); empty for the Owner's own, or one the registry no longer
	// lists. Filled in by the web layer (withTiers).
	Tier string
	// IconURL is its package's logo, for those who manage services, and
	// DrawnIcon the icon shown without one (withIcons).
	IconURL, DrawnIcon string
	// Held: it's held stopped after a restore (or a move) that didn't
	// finish (Status is StatusHeld; SPEC.md §6.9).
	Held *instance.Hold
}

// Server serves the web UI.
//
// What every page relies on comes to NewServer, so it's always there (the
// quality review's S1, decided by the Owner on 2026-10-22): the store, the
// log, the lab's services and its manager, the setup steps and the lab's
// settings. What comes with a feature is in that feature's group, or a
// field of its own, and nil hides the feature.
type Server struct {
	Store *store.Store
	Log   *slog.Logger
	// Services lists installed services and their status. It is a function
	// so the UI doesn't depend on systemd directly (and tests can fake it).
	Services func(ctx context.Context) ([]ServiceView, error)
	// Manager installs and removes services; Guests is its storage for
	// other labs (manager.Guests).
	Manager ServiceManager
	Guests  StorageSharing
	// Setup is the first-run setup wizard's steps.
	Setup LabSetup
	// LabSettings are the lab's settings, as Roostack started with them;
	// labMu guards LabName, which first-run setup changes while pages are
	// being served (labName).
	LabSettings
	labMu sync.Mutex
	// Version is Roostack's version, shown in every page's footer and on
	// the About page.
	Version string
	// Node is the server running the services. Set with Manager.
	Node ServiceNode

	// The features a server may not have, each nil (or empty) when it
	// doesn't.
	FirstRun
	Monitoring
	SingleSignOn
	PublicAddresses
	// Libraries are the media libraries (media.go); nil: none.
	Libraries LibraryManager
	// Photos migrates photos into Immich (photos.go, SPEC.md §8.5); nil:
	// none.
	Photos PhotoMigrations
	// Network adds servers to the lab (SPEC.md §10.2). Nil hides it.
	Network LabNetwork
	// DetectNetwork guesses where the lab is from this server's addresses
	// (public.DetectNetwork; SPEC.md §6.12). Nil guesses nothing.
	DetectNetwork func() string
	// Cloud is the controller's link to Roostack Cloud (SPEC.md §6.6). Nil
	// when Cloud is turned off (config.Cloud): nothing about it is offered.
	Cloud CloudLink
	// Registry keeps the catalog up to date with the service registry
	// (SPEC.md §8.3.3): the controller's only. Nil on other servers and in
	// tests that don't need it.
	Registry RegistryUpdater
	// DNS reports whether home devices can reach services (SPEC.md §6.5.1).
	DNS DNSStatus
	// Mail sends email (SPEC.md §6.13). Nil hides Settings → Email.
	Mail MailSender
	// RunningJob names a maintenance job running on this server, which a
	// restart waits for ("" if none; optional).
	RunningJob func() string

	// What the pages keep in memory, each with its own lock. pending holds
	// logins that passed the password check and are waiting for a 2FA
	// code, keyed by token hash; handoffs are sign-ins at login.<domain>
	// on their way to Roostack's own pages, keyed by the one-time code's
	// hash (pagessignin.go).
	pending  expiring[string, *pendingLogin]
	handoffs expiring[string, *pagesHandoff]
	stepUps  stepUpFailures
	// codeMu makes two-factor checks run one at a time (checkCode).
	codeMu     sync.Mutex
	issues     serverIssues
	hostChecks attention.HostChecks
	// bootID changes each time Roostack starts; the "restarting" page waits
	// for a new one.
	bootID string
	// help is the embedded help site (help.go).
	help *helpSite

	pages map[string]*template.Template
}

// LabSettings are the lab's settings the pages show (config.Lab).
type LabSettings struct {
	// LabName is what people call the lab (config.Lab.DisplayName).
	LabName string
	// Domain is the lab's base domain, shown next to subdomain fields.
	Domain string
	// UIHost is where Roostack's own pages are (config.Lab.UIHost).
	UIHost string
	// DNSProvider is the lab's current DNS provider ID (wizard default).
	DNSProvider string
	// OldAddress is the lab's previous address, still served for a while
	// after switching (config.Lab.OldAddress; SPEC.md §6.6).
	OldAddress *config.OldAddress
	// BackupRepo is the backup destination, shown on the Backups page.
	BackupRepo string
	// HasGPU pre-ticks settings whose default is "auto".
	HasGPU bool
	// Maintenance is the lab's restart policy, shown in Settings.
	Maintenance config.Maintenance
}

// ServiceManager is what the UI needs from internal/manager, by area.
type ServiceManager interface {
	ServiceCatalog
	LabServers
	BackupPlan
	StorageAtOtherLabs
	PublicAccess
	RecoveryKeys
	LabRestores
	Withdrawals
	Holds
	ServiceUpdates
	BackupJunk
	DataImports
	// BackupAccess is what a server's own credential can do at one of its
	// backups: whether they'd survive it being taken over (SPEC.md §10.10).
	BackupAccess(ctx context.Context, serverID, ref string) manager.Access
	// SharedCredentials are the credentials a server keeps for its backups
	// that others use too, to change after removing it.
	SharedCredentials(ctx context.Context, serverID string) []manager.SharedCredential
	// ChangeS3Key gives Roostack a new key for an online storage account,
	// and every server backing up there its new credentials.
	ChangeS3Key(ctx context.Context, ref, accessKey, secretKey string) error
	// LostMainBackup says whether a server's main backup was on a server
	// that left the lab, while it has no new one.
	LostMainBackup(ctx context.Context, serverID string) (store.LostBackup, bool)
	// CheckRollback works out what putting a server's backups at online
	// storage back as they were at a time changes; RollbackChecked is the
	// latest check, and RollBack does it once checked (SPEC.md §10.10).
	CheckRollback(ctx context.Context, serverID, ref string, before time.Time) error
	RollbackChecked(serverID, ref string) *manager.RollbackPlan
	RollBack(ctx context.Context, serverID, ref string, before time.Time) error
}

// DataImports brings a service's data in from the person's own copy
// (SPEC.md §8.4): its server keeps the import (node.Imports), and the
// controller switches it in.
type DataImports interface {
	ImportsOf(service string) (node.Imports, error)
	SwitchImport(ctx context.Context, service string) error
	Switching(service string) bool
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
	// ServiceFiles are the Quadlet and Caddy files Roostack writes for a
	// service, for the technical side of its page.
	ServiceFiles(name string) (*manager.ServiceFiles, error)
	// SetTitle changes what people call a service (never its name).
	SetTitle(ctx context.Context, name, title string) error
	Install(ctx context.Context, name string, req manager.InstallRequest) error
	// InstallSummary is what installing with these choices would set up
	// (SPEC.md §8.3.4); PackageFile and PackageIcon are a catalog
	// package's file and logo.
	InstallSummary(name string, req manager.InstallRequest) (*manager.InstallSummary, error)
	PackageFile(name string) ([]byte, error)
	PackageIcon(name string) ([]byte, error)
	// CheckPackage is how a package stands against the package policy
	// (SPEC.md §8.3.4).
	CheckPackage(p *pkgspec.Package) manager.PolicyCheck
	// NoBuildOn lists the servers a package has no version for the
	// processor of (SPEC.md §8.3.3).
	NoBuildOn(p *pkgspec.Package) []manager.NoBuild
	// The Owner's own packages ("Add your own", SPEC.md §8.3.4): adding
	// (or replacing) one, removing one, and the folder they're kept in.
	AddLocalPackage(data, icon []byte) (*pkgspec.Package, error)
	// CheckLocalPackage checks one without adding it, for the package
	// editor's Check with this Roostack (SPEC.md §8.3.7).
	CheckLocalPackage(data []byte) (manager.LocalPackageCheck, error)
	RemoveLocalPackage(name string) error
	LocalPackagesDir() string
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
	// KeptCopies lists the data set aside on a server (SPEC.md §8.4);
	// PutBackKept and RemoveKept put back or delete a copy, named by one
	// of its folders.
	KeptCopies(ctx context.Context, serverID string) ([]manager.KeptCopy, error)
	PutBackKept(ctx context.Context, serverID, path string) error
	RemoveKept(ctx context.Context, serverID, path string) error
	// Direct links between servers for backups (SPEC.md §10.10): where a
	// server is reached directly (typed by the Owner), and how one server
	// reaches another's backup store.
	LinkAddress(ctx context.Context, id string) string
	SetLinkAddress(ctx context.Context, id, addr string) error
	LinkPath(ctx context.Context, from, to string) string
	LinkFailed(ctx context.Context, a, b string) bool
	ApplyBecause(reason string)
	// Where each server is, at home or away, as the Owner says (SPEC.md
	// §6.9), and what that makes a backup on another server.
	ServerLocation(ctx context.Context, id string) string
	SetServerLocation(ctx context.Context, id, loc string) error
	ServerPlace(ctx context.Context, from, host string) (atHome, away bool, unknown []string)
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

// StorageSharing is storage for other labs (SPEC.md §10.10).
type StorageSharing interface {
	OfferStorage(ctx context.Context) bool
	StorageGuests(ctx context.Context) ([]manager.GuestView, error)
	InviteGuest(ctx context.Context, name string, quota int64, endpoint string) (labnet.StorageInvite, error)
	RemoveGuest(ctx context.Context, id int64) error
	DeleteRemovedGuest(ctx context.Context, id int64) error
	GuestServer(ctx context.Context) string
	SetGuestServer(ctx context.Context, id string) error
	SetGuestQuota(ctx context.Context, id, quota int64) error
	GuestSpaceLeft(ctx context.Context) (manager.GuestSpace, bool)
	LeftoverGuests(ctx context.Context) ([]manager.LeftoverGuest, error)
	DeleteLeftoverGuest(ctx context.Context, user string) error
}

// StorageAtOtherLabs is this lab's storage at other labs (SPEC.md
// §10.10).
type StorageAtOtherLabs interface {
	EndingStorages(ctx context.Context) []manager.EndingStorage
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
	Disks() node.Disks
	History() node.History
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
	// FinishOldAddress forgets an old address that has stopped working
	// once its DNS records are gone (or forgotten), with its login.
	FinishOldAddress(ctx context.Context) error
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
	// Forget stops treating as Roostack's the records it couldn't delete
	// under an address the lab no longer uses (the Owner deletes them).
	Forget(ctx context.Context) error
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

// NewServer prepares the pages, with what every page relies on: none of
// these may be nil. setupToken is empty if users already exist.
func NewServer(st *store.Store, log *slog.Logger, services func(context.Context) ([]ServiceView, error), mgr ServiceManager,
	wizard LabSetup, lab LabSettings, setupToken string) (*Server, error) {
	s := &Server{Store: st, Log: log, Services: services, Manager: mgr, Setup: wizard, LabSettings: lab,
		stepUps: stepUpFailures{byUser: map[int64]int{}}, pages: map[string]*template.Template{}}
	s.setupToken = setupToken
	var err error
	if s.bootID, err = authkit.NewToken(); err != nil {
		return nil, err
	}
	if s.servedToken, err = authkit.NewToken(); err != nil {
		return nil, err
	}
	if s.help, err = newHelpSite(helpFS); err != nil {
		return nil, err
	}
	for _, page := range []string{"login", "totp", "setup", "twofactor", "catalog", "local-add", "local-editor", "install", "remove", "setup-timezone", "setup-ssh",
		"setup-domain", "setup-backup", "recovery-key", "restarting", "dns-takeover", "setup-restore", "setup-joined",
		"home", "services", "service", "backups", "restore", "error", "settings-server-gone", "settings-server-unreachable", "settings", "account", "account-appearance", "account-notifications", "users", "about",
		"settings-domain", "settings-network", "settings-email", "settings-history", "settings-history-event", "settings-updates", "settings-servers", "move", "settings-storage", "settings-lab",
		"backups-recovery-key", "service-public", "service-settings", "settings-public", "backups-key-change", "settings-cloud", "cloud-open", "cloud-linked", "visitors",
		"backups-junk", "setup-finished", "settings-media", "settings-media-add", "settings-media-folders", "settings-media-library", "monitoring", "monitoring-check",
		"import", "import-folders", "photos", "photos-home", "photos-folders"} {
		t, err := template.New("layout.html").Funcs(templateFuncs()).ParseFS(templateFS,
			"templates/layout.html", "templates/components.html", "templates/parts.html", "templates/"+page+".html")
		if err != nil {
			return nil, err
		}
		s.pages[page] = t
	}
	if s.signInPages, err = parseSignInPages(); err != nil {
		return nil, err
	}
	return s, nil
}

// Handler returns all routes wrapped in the common middleware.
func (s *Server) Handler() http.Handler { return s.handler(nil) }

// handler is Handler, with gate (if any) between the session and the
// routes: the setup listener's (setupGate).
func (s *Server) handler(gate func(http.Handler) http.Handler) http.Handler {
	static, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(static)))
	// The package editor's own files (SPEC.md §8.3.7), the same as
	// roostack.cloud serves.
	mux.Handle("GET /editor/", http.StripPrefix("/editor/", staticHandler(editor.Files())))
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
	mux.HandleFunc("GET "+pathPagesStart, s.pagesStart)
	mux.HandleFunc("GET "+pathPagesCallback, s.pagesCallback)

	mux.Handle("GET /account/2fa", s.requireLogin(s.twoFactorPage))
	mux.Handle("POST /account/2fa", s.requireLogin(s.twoFactorSubmit))
	mux.Handle("GET "+wizardTwoFactor, s.requireLogin(s.twoFactorPage))
	mux.Handle("POST "+wizardTwoFactor, s.requireLogin(s.twoFactorSubmit))

	mux.Handle("GET /{$}", s.require(auth.ViewDashboard, s.homePage))
	mux.Handle("GET /partials/home", s.require(auth.ViewDashboard, s.homePartial))
	mux.Handle("GET /services", s.require(auth.ViewDashboard, s.servicesPage))
	mux.Handle("GET /partials/services", s.require(auth.ViewDashboard, s.servicesPartial))
	mux.Handle("GET /services/{name}", s.require(auth.ManageServices, s.servicePage))
	mux.Handle("GET /services/{name}/settings", s.require(auth.ManageServices, s.serviceSettingsPage))
	// Bringing a service's data in (SPEC.md §8.4): the Owner only.
	// Migrating photos into Immich (SPEC.md §8.5): the steps on Immich's
	// page for Owners and Admins, and in a page of their own for everyone
	// the Owner allows (photos.go checks who).
	mux.Handle("GET /immich-photos", s.require(auth.ViewDashboard, s.photosHome))
	mux.Handle("GET /immich-photos/{name}", s.require(auth.ViewDashboard, s.photosOwnPage))
	mux.Handle("GET /immich-photos/{name}/folders", s.require(auth.ViewDashboard, func(w http.ResponseWriter, r *http.Request) {
		s.photoFoldersPage(w, r, false)
	}))
	mux.Handle("GET /services/{name}/photos", s.require(auth.ManageServices, s.photosPanelPage))
	mux.Handle("GET /services/{name}/photos/folders", s.require(auth.ManageServices, func(w http.ResponseWriter, r *http.Request) {
		s.photoFoldersPage(w, r, true)
	}))
	mux.Handle("GET /services/{name}/import", s.requireOwner(s.importPage))
	mux.Handle("GET /services/{name}/import/folders", s.requireOwner(s.importFoldersPage))
	mux.Handle("GET /services/{name}/import/browse", s.requireOwner(s.importBrowsePage))
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
	// Servers is a main area (docs/DESIGN.md "Layout"); its list was under
	// Settings, and each server's own pages still are.
	mux.Handle("GET /servers", s.requireOwner(s.serversPage))
	mux.Handle("GET /settings/servers", s.requireOwner(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := "/servers"
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	})))
	mux.Handle("GET /settings/servers/{server}", s.requireOwner(s.serverPage))
	mux.Handle("GET /settings/servers/{server}/disk", s.requireOwner(s.diskPart))
	s.serverRoutes(mux)
	mux.Handle("POST /settings/servers/{server}/rename", s.requireOwner(s.renameServerSubmit))
	mux.Handle("POST /settings/servers/{server}/link-address", s.requireOwner(s.linkAddressSubmit))
	mux.Handle("POST /settings/servers/{server}/remove", s.requireOwner(s.removeServerSubmit))
	mux.Handle("POST /settings/servers/{server}/kept/remove", s.requireOwner(s.removeKeptServerSubmit))
	mux.Handle("POST /settings/servers/{server}/kept/put-back", s.requireOwner(s.putBackKeptServerSubmit))
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
	mux.Handle("GET /monitoring", s.require(auth.ManageServices, s.monitoringPage))
	mux.Handle("GET /monitoring/check/{key}", s.require(auth.ManageServices, s.monitorCheckPage))
	mux.Handle("POST /monitoring/checks", s.require(auth.ManageServices, s.monitorAddSubmit))
	mux.Handle("POST /monitoring/checks/{id}/remove", s.require(auth.ManageServices, s.monitorRemoveSubmit))
	mux.Handle("POST /monitoring/switch", s.requireOwner(s.monitorSwitchSubmit))
	mux.Handle("POST /monitoring/status-page", s.requireOwner(s.statusPageSubmit))
	mux.Handle("GET /settings/media", s.requireOwner(s.mediaPage))
	mux.Handle("GET /settings/media/add", s.requireOwner(s.mediaAddPage))
	mux.Handle("POST /settings/media/add", s.requireOwner(s.mediaAddSubmit))
	mux.Handle("GET /settings/media/folders", s.requireOwner(s.mediaFoldersPage))
	mux.Handle("GET /settings/media/{server}/{name}", s.requireOwner(s.mediaLibraryPage))
	mux.Handle("POST /settings/media/{server}/{name}/readers", s.requireOwner(s.mediaReadersSubmit))
	mux.Handle("POST /settings/media/{server}/{name}/remove", s.requireOwner(s.mediaRemoveSubmit))
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
	mux.Handle("POST /services/{name}/public/{route}/waf", s.requireOwner(s.wafSubmit))
	mux.Handle("POST /services/{name}/start-anyway", s.requireOwner(s.startAnywaySubmit))
	mux.Handle("POST /services/{name}/held/start", s.requireOwner(s.heldStartSubmit))
	mux.Handle("POST /services/{name}/held/put-back", s.requireOwner(s.heldPutBackSubmit))
	mux.Handle("GET /settings/public", s.requireOwner(s.publicSettingsPage))
	mux.Handle("POST /settings/public/sharing", s.requireOwner(s.publicSharingSubmit))
	mux.Handle("POST /settings/public/edge", s.requireOwner(s.publicEdgeSubmit))
	mux.Handle("POST /settings/public/countries", s.requireOwner(s.publicCountriesSubmit))
	mux.Handle("POST /settings/public/relay-check", s.requireOwner(s.publicRelayCheck))
	mux.Handle("POST /settings/public/relay-on", s.requireOwner(s.publicRelayOn))
	// Visitors through the relay: Owners on Public access, Admins on their own page.
	mux.Handle("GET /partials/relay-stats", s.require(auth.ManageServices, s.relayStatsPart))
	mux.Handle("GET /visitors", s.require(auth.ManageServices, s.visitorsPage))
	mux.Handle("POST /settings/public/countries/open", s.requireOwner(s.publicCountriesOpen))
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
	s.ssoRoutes(mux)
	mux.Handle("GET /backups/recovery-key", s.requireOwner(s.recoveryKeyShowPage))
	mux.Handle("POST /backups/recovery-key", s.requireOwner(s.recoveryKeyShowSubmit))
	mux.Handle("GET /backups/recovery-key/change", s.requireOwner(s.keyChangePage))
	mux.Handle("POST /backups/recovery-key/change", s.requireOwner(s.keyChangeSubmit))
	mux.Handle("POST /backups/recovery-key/change/confirm", s.requireOwner(s.keyChangeConfirm))
	mux.Handle("POST /backups/recovery-key/change/cancel", s.requireOwner(s.keyChangeCancel))
	mux.Handle("POST /backups/keep-this-copy", s.requireOwner(s.keepThisCopySubmit))
	mux.Handle("GET /backups/junk", s.requireOwner(s.junkPage))
	mux.Handle("POST /backups/junk/dismiss", s.requireOwner(s.junkDismissSubmit))
	mux.Handle("GET /backups/restore/{name}", s.requireOwner(s.restorePage))
	mux.Handle("POST /backups/restore/{name}", s.requireOwner(s.restoreSubmit))
	mux.Handle("POST /backups/restore/{name}/kept/remove", s.requireOwner(s.removeKeptSubmit))
	mux.Handle("POST /backups/restore/{name}/kept/put-back", s.requireOwner(s.putBackKeptSubmit))
	mux.Handle("POST /settings/updates/run", s.requireOwner(s.updateNowSubmit))
	mux.Handle("POST /settings/updates/series", s.requireOwner(s.seriesAcceptSubmit))
	mux.Handle("POST /settings/updates/registry/check", s.requireOwner(s.registryCheckSubmit))
	mux.Handle("POST /settings/updates/registry/on", s.requireOwner(s.registryOnSubmit))
	mux.Handle("POST /settings/updates/registry/stops", s.requireOwner(s.registryStopsSubmit))
	mux.Handle("POST /settings/host-changes/known", s.requireOwner(s.hostChangesKnownSubmit))
	mux.Handle("POST /settings/restart", s.requireOwner(s.restartNowSubmit))
	mux.Handle("POST /settings/restart-policy", s.requireWizard(s.restartPolicySubmit))
	mux.Handle("GET /setup/ssh", s.requireWizard(s.sshPage))
	mux.Handle("POST /setup/ssh", s.requireWizard(s.sshSubmit))
	mux.Handle("GET /setup/domain", s.requireWizard(s.domainPage))
	mux.Handle("POST /setup/domain", s.requireWizard(s.domainSubmit))
	mux.Handle("POST /setup/domain/later", s.requireWizard(s.domainLater))
	mux.Handle("POST /settings/domain/old/end", s.requireWizard(s.oldAddressEnd))
	mux.Handle("POST /settings/domain/old/forget", s.requireWizard(s.oldRecordsForget))
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
	mux.Handle("POST /backups/plan/key", s.requireOwner(s.planKeySubmit))
	mux.Handle("POST /backups/plan/rollback", s.requireOwner(s.planRollbackSubmit))
	mux.Handle("POST /setup/backup/erase", s.requireWizard(s.eraseDriveSubmit))
	mux.Handle("GET /setup/recovery-key", s.requireWizard(s.recoveryPage))
	mux.Handle("POST /setup/recovery-key", s.requireWizard(s.recoveryConfirm))
	mux.Handle("GET /restarting", s.requireLogin(s.restartingPage))

	mux.Handle("POST /backup/run", s.require(auth.ManageServices, s.backupRun))
	mux.Handle("GET /catalog", s.require(auth.ManageServices, s.catalogPage))
	mux.Handle("GET /catalog/ready", s.require(auth.ManageServices, s.catalogReady))
	mux.Handle("GET /catalog/add", s.requireOwner(s.localAddPage))
	mux.Handle("GET /catalog/editor", s.requireOwner(s.localEditorPage))
	mux.Handle("POST /catalog/editor/check", s.requireOwner(s.localEditorCheck))
	mux.Handle("POST /catalog/add", s.requireOwner(s.localAddSubmit))
	mux.Handle("POST /catalog/{name}/remove", s.requireOwner(s.localRemoveSubmit))
	mux.Handle("GET /install/{name}", s.require(auth.ManageServices, s.installPage))
	mux.Handle("POST /install/{name}", s.require(auth.ManageServices, s.installSubmit))
	mux.Handle("POST /install/{name}/summary", s.require(auth.ManageServices, s.installSummaryPart))
	mux.Handle("GET /install/{name}/package.yaml", s.require(auth.ManageServices, s.packageFile))
	mux.Handle("GET /install/{name}/icon.png", s.require(auth.ManageServices, s.packageIcon))
	mux.Handle("POST /services/{name}/ports/{port}", s.require(auth.ManageServices, s.portSubmit))
	mux.Handle("POST /services/{name}/settings", s.require(auth.ManageServices, s.settingsSubmit))
	mux.Handle("POST /immich-photos/{name}/from", s.require(auth.ViewDashboard, s.photoKindSubmit))
	mux.Handle("POST /immich-photos/{name}/export", s.require(auth.ViewDashboard, s.photoGuidedSubmit))
	mux.Handle("POST /immich-photos/{name}/where", s.require(auth.ViewDashboard, s.photoSourceSubmit))
	mux.Handle("POST /immich-photos/{name}/key", s.require(auth.ViewDashboard, s.photoKeySubmit))
	mux.Handle("POST /immich-photos/{name}/choices", s.require(auth.ViewDashboard, s.photoChoicesSubmit))
	mux.Handle("POST /immich-photos/{name}/check", s.require(auth.ViewDashboard, s.photoCheckSubmit))
	mux.Handle("POST /immich-photos/{name}/start", s.require(auth.ViewDashboard, s.photoStartSubmit))
	mux.Handle("POST /immich-photos/{name}/stop", s.require(auth.ViewDashboard, s.photoStopSubmit))
	mux.Handle("POST /immich-photos/{name}/forget", s.require(auth.ViewDashboard, s.photoForgetSubmit))
	mux.Handle("POST /services/{name}/photos/settings", s.requireOwner(s.photoSettingsSubmit))
	mux.Handle("POST /services/{name}/photos/hide", s.require(auth.ManageServices, s.photoHideSubmit))
	mux.Handle("POST /services/{name}/photos/locations/{location}/remove", s.requireOwner(s.photoLocationRemoveSubmit))
	mux.Handle("POST /services/{name}/import/source", s.requireOwner(s.importSourceSubmit))
	mux.Handle("POST /services/{name}/import/setup", s.requireOwner(s.importSetupSubmit))
	mux.Handle("POST /services/{name}/import/paths", s.requireOwner(s.importPathsSubmit))
	mux.Handle("POST /services/{name}/import/choices", s.requireOwner(s.importChoicesSubmit))
	mux.Handle("POST /services/{name}/import/check", s.requireOwner(s.importCheckSubmit))
	mux.Handle("POST /services/{name}/import/copy", s.requireOwner(s.importCopySubmit))
	mux.Handle("POST /services/{name}/import/stop", s.requireOwner(s.importStopSubmit))
	mux.Handle("POST /services/{name}/import/switch", s.requireOwner(s.importSwitchSubmit))
	mux.Handle("POST /services/{name}/import/cancel", s.requireOwner(s.importCancelSubmit))
	mux.Handle("POST /services/{name}/libraries", s.require(auth.ManageServices, s.serviceLibrariesSubmit))
	mux.Handle("POST /services/{name}/reveal/{key}", s.require(auth.ManageServices, s.revealSubmit))
	mux.Handle("POST /services/{name}/setup/retry", s.require(auth.ManageServices, s.retrySetupSubmit))
	mux.Handle("POST /services/{name}/setup/retry-with", s.require(auth.ManageServices, s.retryWithSubmit))
	mux.Handle("POST /services/{name}/actions/{action}", s.require(auth.ManageServices, s.actionSubmit))
	mux.Handle("GET /services/{name}/remove", s.require(auth.ManageServices, s.removePage))
	mux.Handle("POST /services/{name}/title", s.require(auth.ManageServices, s.titleSubmit))
	mux.Handle("POST /services/{name}/update", s.require(auth.ManageServices, s.updateSubmit))
	mux.Handle("POST /services/{name}/updates", s.require(auth.ManageServices, s.updatePolicySubmit))
	mux.Handle("POST /services/{name}/remove", s.require(auth.ManageServices, s.removeSubmit))

	routes := s.errorPages(mux)
	if gate != nil {
		routes = gate(routes)
	}
	return s.recoverPanics(compress(securityHeaders(sameOrigin(s.loadSession(routes)))))
}

// labName is LabName, which first-run setup may change while pages are
// being served.
func (s *Server) labName() string {
	s.labMu.Lock()
	defer s.labMu.Unlock()
	return s.LabName
}

func (s *Server) needsSetup() bool {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setupToken != ""
}
