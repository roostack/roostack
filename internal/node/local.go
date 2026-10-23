package node

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/firewall"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/systemd"
)

// Node is a server in the lab, as the controller sees it (SPEC.md §10.3).
// Local runs on this server; Remote speaks the same operations to other
// servers over Roostack's network.
//
// Each capability a server may not have (Backups, Mover, Photos...) is a
// method that returns nil when it doesn't; callers check for nil, and
// Capability also covers a server that is itself nil (one that left the
// lab). Remote's Backups, Restores, Updates, Settings and Imports look at
// the server's cached status (WireStatus), so they're nil while it
// doesn't answer; its other capabilities are always there, and the server
// answers "not found" to one it lacks. Photos isn't on the lab API yet,
// so Remote's is nil. Local's Photos is a type of its own (localPhotos),
// as Remote's capabilities are, and so is its Imports (localImports); its
// others return the Local itself, so a type that embeds Local and
// overrides a capability's methods overrides its capability method too.
type Node interface {
	Identity() nodeid.Identity
	// Apply hands the node its latest assignment; the node works towards
	// it in the background.
	Apply(a Assignment)
	// Status reports the current or last apply.
	Status() ApplyStatus
	// ServiceStates reports each of its services as running, starting,
	// stopped, failed or unknown.
	ServiceStates(ctx context.Context) (map[string]string, error)
	// ServiceDetails and BuiltIns describe its services and Roostack's own
	// parts on it (details.go).
	ServiceDetails(ctx context.Context) (map[string]ServiceDetail, error)
	BuiltIns(ctx context.Context) ([]BuiltIn, error)
	InstallStatus(ctx context.Context, service string) HookStatus
	RetryInstall(ctx context.Context, service string) error
	RunAction(ctx context.Context, service, action string, inputs map[string]string) (string, error)
	RevealSecret(ctx context.Context, service, key string) (string, error)
	// LatestActivity is the node's latest log of a kind (activity.Apply, ...).
	LatestActivity(kind string) (activity.Snapshot, bool)
	// Maintenance jobs (maintenance.go); nil when not available.
	Backups() Backups
	Restores() Restores
	Updates() Updates
	// Settings are the server's own settings (settings.go); nil when
	// they can't be changed from Roostack.
	Settings() Settings
	// Media is its media libraries (media.go); nil when it can't have any.
	Media() Media
	// Imports is bringing data into its services (import.go); nil when
	// it can't.
	Imports() Imports
	// Mover moves services' data to and from it (move.go); Updater
	// updates its services (update.go).
	Mover() Mover
	Updater() Updater
	// Disks, Memory, LeftData and History are what the controller reads
	// about it (disk.go, memory.go, leftdata.go, history.go).
	Disks() Disks
	Memory() Memory
	LeftData() LeftData
	History() History
	// Linked reports its direct links to other servers and GuestHost
	// keeps other labs' backups (guesthost.go); Processor says what kind
	// of processor it has (arch.go).
	Linked() Linked
	GuestHost() GuestHost
	Processor() Processor
	// Photos migrates photos into its Immich (photos.go).
	Photos() Photos
}

// Capability is one of n's capabilities, named by a method expression
// such as node.Node.Mover, or nil when n itself is nil.
func Capability[T any](n Node, capability func(Node) T) T {
	if n == nil {
		var none T
		return none
	}
	return capability(n)
}

// Assignment is what the controller wants a node to run.
type Assignment struct {
	// Services are the services placed on the node, with their packages.
	Services []state.Service
	// Secrets are values people typed (e.g. an admin password), keyed by
	// Podman secret name. The node forgets them once the secrets exist;
	// the controller doesn't keep them after handing them over.
	Secrets map[string][]byte
	// SetUp names newly installed services whose install hooks should run.
	SetUp []string
	// Lab is the lab-wide settings the services need (e.g. the domain).
	// Empty uses the node's own.
	Lab LabContext
	// Proxied are services on other servers whose routes this server's
	// Caddy serves (the controller, SPEC.md §10.5).
	Proxied []state.Proxied
	// Storage, if set, makes the server offer its disk for backups
	// (SPEC.md §10.10).
	Storage *Storage
	// Guests are other labs storing backups here, reached on GuestAddr,
	// this lab's address on the guests' link (the controller only).
	Guests    []generate.GuestStorage
	GuestAddr string
	// ProxyPorts are where the controller forwards its servers' backups
	// to other labs' storage (on Roostack's network).
	ProxyPorts []int
	// Links are this server's direct links to other servers, for backups
	// to their disks (SPEC.md §10.10); nil leaves them as they are (the
	// controller, which every server dials anyway).
	Links *agent.Links
	// Edge, if set, makes this server the lab's edge (SPEC.md §10.5): its
	// Caddy passes visitors for the public names on to the controller.
	Edge *caddy.EdgeConfig
	// Libraries are this server's media libraries (SPEC.md §8.1, "Media
	// libraries"): mounted, and the rest unmounted, by its agent.
	Libraries []agent.Library
	// Reason says why it was sent, for the apply's log.
	Reason string
}

// Storage is who may use a server's backup store: bcrypt password hashes
// (never the passwords) for each server, and for the controller's admin
// access.
type Storage struct {
	Users map[string]string `json:"users"`
	Admin string            `json:"admin"`
}

// ErrNotFound means no such service or action on this node.
var ErrNotFound = errors.New("not found")

// ApplyStatus is shown on the dashboard.
type ApplyStatus struct {
	Running  bool
	LastRun  time.Time
	LastErr  error
	Finished bool // at least one apply has finished
}

// FirewallApplier is roostack-agent's firewall operation.
type FirewallApplier interface {
	ApplyFirewall(ctx context.Context, fw agent.Firewall) error
}

// Local is this server: it applies its assignment (Quadlets, secrets,
// Caddy, firewall) and runs package hooks.
type Local struct {
	ID  nodeid.Identity
	Lab config.Lab
	// Reconciler applies desired state. Its UserSecret is set by Local.
	Reconciler *reconcile.Reconciler
	Log        *slog.Logger
	// Activity shows applies and install hooks live in the UI (optional).
	Activity *activity.Registry
	// Firewall applies the host firewall through roostack-agent after each
	// apply. Nil leaves the firewall alone.
	Firewall FirewallApplier
	// Units reads systemd unit states, for ServiceStates (optional).
	Units UnitStatuser
	// Launch runs commands as the roostack user outside Roostack's sandbox, for
	// moving service data (move.go; optional).
	Launch Launcher
	// WireGuard reads the WireGuard interfaces, and Version is Roostack's, to
	// describe this server (details.go; optional).
	WireGuard WireGuardStatuser
	// Links configures WireGuard links (the guests' link when this server
	// hosts storage for other labs).
	Links LinkAgent
	// Libraries mounts media libraries and services' views of them
	// (libraries.go; optional: without it, none are mounted).
	Libraries LibraryAgent
	// ImportAgent mounts the person's copy of a service's data, to bring
	// it in (import.go; optional: without it, data can't be brought).
	ImportAgent ImportAgent
	// PhotoAgent mounts a person's export, and PhotoUnits runs immich-go,
	// to migrate photos into Immich (photos.go; optional: without both,
	// photos can't be migrated). OnPhotos hears when a migration ends.
	PhotoAgent PhotoAgent
	PhotoUnits PhotoUnits
	OnPhotos   func(ctx context.Context, s PhotoSummary)
	Version    string

	sizes sizes

	// Hooks (hooks.go). State records install hook progress; without
	// State and Exec, hooks don't run.
	State KV
	// Events keeps finished logs as history (history.go; optional).
	Events  HistoryStore
	Secrets SecretStore
	Exec    ExecFunc
	// HookRetry and HookDeadline tune install hook retries (0: defaults).
	HookRetry, HookDeadline time.Duration

	// Jobs are the server's backups, restores and OS updates.
	Jobs Maintenance
	// Server changes the server's own settings (optional).
	Host Settings

	// labServers, on the hub, lists the lab's servers' addresses on
	// Roostack's network (SetLabServers).
	labServers func(ctx context.Context) ([]string, error)
	// setupPort is the first-run setup pages' port while setup lasts
	// (SetSetupPort).
	setupPort int

	mu         sync.Mutex
	assignment Assignment
	// userSecrets holds typed-in values until an apply creates the Podman
	// secrets. Never written to disk.
	userSecrets map[string][]byte
	// replaceSecrets are values typed again for a service being set up
	// (a failed install retried with new values): they replace the Podman
	// secrets it already has, before its install hooks run.
	replaceSecrets map[string][]byte
	// sshChoice is a new SSH choice made while running (UseSSH); "" uses
	// Lab's.
	sshChoice string
	status    ApplyStatus
	// again is set when an assignment arrives during an apply, so another
	// apply runs afterwards.
	again bool
	done  chan struct{}

	hooksBusy, hooksAgain bool
	hooksRunning          map[string]bool
	// ssoRunning: a service's single sign-on hooks are running.
	ssoRunning map[string]bool
	// hooksCancel stops a service's running setup, when the service is
	// removed or installed afresh meanwhile (errSetupStopped).
	hooksCancel map[string]context.CancelCauseFunc
	hooksDone   chan struct{}

	// followingBuild: a Caddy image build is being shown (build.go).
	followingBuild bool
	// installs are new services being installed, step by step (install.go);
	// installsLoaded, once they've been read from State.
	installs       map[string]*InstallProgress
	installsLoaded bool

	// updates are services updating here (update.go).
	updates updates

	// libWatch is what the watch of libraries at their own paths saw last
	// (libwatch.go).
	libWatch libraryWatch
	// imp is what bringing data into services shares (import.go).
	imp importRuns
	// pho is what migrating photos into Immich shares (photos.go).
	pho photoRuns
}

// services is a copy of the services this server runs.
func (n *Local) services() []state.Service {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.assignment.Services)
}

// liveOutput is what runs of a service now: all of it, or only its
// network and databases while it waits for its data, is stopped by
// Roostack or is held after a restore that didn't finish (generate.Stop),
// as the controller's assignment has it. Its app's units don't exist
// then, so they can't be stopped or started.
func (n *Local) liveOutput(service string) (*generate.Output, error) {
	s, ok := n.service(service)
	if !ok {
		return nil, ErrNotFound
	}
	return state.Output(n.currentLab(), s)
}

// Identity is this server's identity.
func (n *Local) Identity() nodeid.Identity { return n.ID }

// Apply records the latest assignment and applies it in the background. If
// an apply is running, another follows it, so the latest always wins.
func (n *Local) Apply(a Assignment) {
	// The first assignment says which services are here: a copy that a
	// restart interrupted carries on (import.go), and so does a photo
	// migration (photos.go).
	n.imp.mu.Lock()
	if !n.imp.resumed && n.State != nil {
		n.imp.resumed = true
		defer func() {
			go n.imports().resumeImports(context.Background())
			go n.photos().resumePhotos(context.Background())
		}()
	}
	n.imp.mu.Unlock()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.userSecrets == nil {
		n.userSecrets = map[string][]byte{}
	}
	for k, v := range a.Secrets {
		n.userSecrets[k] = v
	}
	old := n.assignment.Services
	n.assignment = Assignment{Services: a.Services, Lab: a.Lab, Proxied: a.Proxied, Storage: a.Storage, Links: a.Links,
		Guests: a.Guests, GuestAddr: a.GuestAddr, ProxyPorts: a.ProxyPorts, Edge: a.Edge, Libraries: a.Libraries}
	if n.State != nil {
		// A service taken off this server forgets its setup progress, and
		// stops any setup still running: its hooks would keep retrying
		// for a service that's gone.
		for _, s := range old {
			if _, still := n.serviceLocked(s.Name()); !still {
				n.stopSetupLocked(s.Name())
				n.State.SetSetting(context.Background(), hookKey(s.Name()), "")
				n.State.SetSetting(context.Background(), ssoKey(s.Name()), "")
			}
		}
		for _, name := range a.SetUp {
			// Installed afresh (removed and installed again while its
			// setup ran): the old setup's package may differ.
			n.stopSetupLocked(name)
			if err := n.State.SetSetting(context.Background(), hookKey(name), hookPending); err != nil {
				n.Log.Error("recording pending setup", "service", name, "err", err)
			}
			// A new app knows nothing of single sign-on yet.
			n.State.SetSetting(context.Background(), ssoKey(name), "")
			for k, v := range a.Secrets {
				if strings.HasPrefix(k, SecretName(name, "")) {
					if n.replaceSecrets == nil {
						n.replaceSecrets = map[string][]byte{}
					}
					n.replaceSecrets[k] = v
				}
			}
		}
	}
	if n.status.Running {
		n.again = true
		return
	}
	n.status.Running = true
	n.done = make(chan struct{})
	go n.applyLoop(n.done)
}

// LatestActivity is this server's latest log of a kind.
func (n *Local) LatestActivity(kind string) (activity.Snapshot, bool) { return n.Activity.Latest(kind) }

// Status reports the current or last apply.
func (n *Local) Status() ApplyStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.status
}

// Wait blocks until the current apply finishes (for tests and tools).
func (n *Local) Wait() {
	n.mu.Lock()
	done := n.done
	n.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (n *Local) applyLoop(done chan struct{}) {
	defer close(done)
	for {
		err := n.applyOnce(context.Background())
		n.mu.Lock()
		n.status.LastRun, n.status.LastErr, n.status.Finished = time.Now(), err, true
		if err != nil {
			n.Log.Error("applying changes", "err", err)
		} else {
			defer n.startInstallHooks() // after unlocking
		}
		if !n.again {
			n.status.Running = false
			n.mu.Unlock()
			return
		}
		n.again = false
		n.mu.Unlock()
	}
}

func (n *Local) applyOnce(ctx context.Context) (err error) {
	// Every apply has a log saying why it ran and what it did; one that
	// changed nothing (as at most startups) is routine: shown as the
	// latest, not kept as history (SPEC.md §6.14).
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	act := n.Activity.Start(activity.Apply, "Applying changes")
	defer func() { act.Finish(err) }()
	act.Printf("Why: %s.", cmp.Or(a.Reason, "Roostack started, or its settings changed"))
	desired, err := state.Desired(n.lab(a), n.withInstallValues(ctx, a.Services), a.Proxied...)
	if err != nil {
		return err
	}
	if err := n.addStorage(&desired, a.Storage); err != nil {
		return err
	}
	if err := n.addGuestStorage(&desired, a); err != nil {
		return err
	}
	// Media libraries, and services' views of them, before services start.
	n.applyLibraries(ctx, act, a, desired)
	if a.Edge != nil && n.lab(a).IsNode() {
		// Nodes have no Caddy of their own: the edge's is the system's.
		edge, err := generate.Edge(n.lab(a), *a.Edge)
		if err != nil {
			return err
		}
		desired.System.Quadlets = append(desired.System.Quadlets, edge.Quadlets...)
		desired.System.Caddyfile = edge.Caddyfile
		desired.System.Dirs = append(desired.System.Dirs, edge.Dirs...)
		desired.System.Units = append(desired.System.Units, edge.Units...)
	}
	r := *n.Reconciler
	r.UserSecret = func(name string) ([]byte, bool) {
		n.mu.Lock()
		defer n.mu.Unlock()
		v, ok := n.userSecrets[name]
		return v, ok
	}
	// An install a failed download or a restart cut off starts again.
	r.Unfinished = n.unfinishedUnits()
	plan, err := r.Plan(ctx, desired)
	if err != nil {
		return err
	}
	if plan.Empty() {
		act.Step("Nothing to change")
		act.Routine()
	} else {
		r.Log = act
	}
	// Caddy's image builds in the background (for minutes the first time):
	// show it, whether this apply starts it or it was already running when
	// Roostack restarted (e.g. at the end of the setup wizard).
	building := (!n.lab(a).IsNode() || a.Edge != nil) && (slices.Contains(plan.BuildUnits, CaddyBuildUnit) || n.caddyBuilding(ctx))
	if building {
		n.clearBuildLog(ctx, plan, n.lab(a).Paths.CaddyBuildLog())
	}
	// New services are shown step by step while they install.
	n.planInstalls(a.Services, desired, plan, building)
	r.Progress = n.installEvent
	if err := r.Apply(ctx, plan); err != nil {
		n.failInstalls(err)
		return err
	}
	n.installsApplied()
	if building {
		n.followCaddyBuild(n.lab(a).Paths.CaddyBuildLog())
	}
	// Created secrets live in Podman now; forget the typed-in values.
	n.mu.Lock()
	for _, s := range plan.Secrets {
		delete(n.userSecrets, s.Name)
		delete(n.replaceSecrets, s.Name) // new, so nothing to replace
	}
	replace := n.replaceSecrets
	n.replaceSecrets = nil
	n.mu.Unlock()
	for name, v := range replace {
		if n.Secrets == nil {
			break
		}
		if err := n.Secrets.Replace(ctx, name, v); err != nil {
			return fmt.Errorf("updating %s: %w", name, err)
		}
		n.mu.Lock()
		delete(n.userSecrets, name)
		n.mu.Unlock()
	}

	// The firewall follows the services, e.g. a package port just turned on.
	roles := firewall.Roles{Store: a.Storage != nil, ProxyPorts: a.ProxyPorts, GuestAddr: a.GuestAddr, Guests: a.Guests,
		LinksListen: a.Links != nil && a.Links.Listen}
	n.mu.Lock()
	servers := n.labServers
	roles.SetupPort = n.setupPort
	n.mu.Unlock()
	if lab := n.lab(a); servers != nil && n.Firewall != nil && lab.OverlayAddr != "" && !lab.IsNode() && lab.Firewall.SSH != "" {
		members, err := servers(ctx)
		if err != nil {
			return fmt.Errorf("listing the lab's servers: %w", err)
		}
		roles.Hub, roles.Members = true, members
	}
	if fw, ok := firewall.Desired(n.lab(a), desired.Services, roles); ok && n.Firewall != nil {
		act.Step("Updating the firewall")
		if err := n.Firewall.ApplyFirewall(ctx, fw); err != nil {
			return fmt.Errorf("applying the firewall: %w", err)
		}
	}
	if a.Links != nil && n.Links != nil {
		act.Step("Updating direct links to other servers")
		for _, p := range a.Links.Peers {
			to := "waits for it to connect"
			if p.Endpoint != "" {
				to = "at " + p.Endpoint
			}
			act.Printf("%s %s", strings.Join(p.AllowedIPs, ", "), to)
		}
		if err := n.Links.ApplyLinks(ctx, *a.Links); err != nil {
			return fmt.Errorf("setting up direct links: %w", err)
		}
	}
	act.Step("All changes applied")
	return nil
}

// UseSSH applies a new SSH choice (HostSettings.SetSSH, already saved in
// lab.yaml) without a restart: the next apply's firewall uses it, and one
// starts now.
func (n *Local) UseSSH(choice string) {
	n.mu.Lock()
	n.sshChoice = choice
	a := n.assignment
	n.mu.Unlock()
	a.Secrets, a.SetUp, a.Reason = nil, nil, "Who can log in over SSH changed"
	n.Apply(a)
}

// SetLabServers tells the hub how to list the lab's servers, for the
// firewall: anyone else on Roostack's network reaches only the lab API.
func (n *Local) SetLabServers(f func(ctx context.Context) ([]string, error)) {
	n.mu.Lock()
	n.labServers = f
	n.mu.Unlock()
}

// SetSetupPort opens the first-run setup pages' port to the home network
// from the next apply, or closes it with 0 (SPEC.md §6.3.2).
func (n *Local) SetSetupPort(port int) {
	n.mu.Lock()
	n.setupPort = port
	n.mu.Unlock()
}

// Reapply applies the current assignment again, e.g. so the firewall
// lets a server that just joined the lab in.
func (n *Local) Reapply(reason string) {
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	a.Secrets, a.SetUp, a.Reason = nil, nil, reason
	n.Apply(a)
}

// lab is the node's lab config with the assignment's lab-wide settings.
func (n *Local) lab(a Assignment) config.Lab {
	lab := n.Lab
	n.mu.Lock()
	if n.sshChoice != "" {
		lab.Firewall.SSH = n.sshChoice
	}
	n.mu.Unlock()
	if a.Lab.Domain != "" {
		lab.Domain, lab.Email = a.Lab.Domain, a.Lab.Email
		lab.Public.NoSharing = a.Lab.NoSharing
	}
	lab.EdgeAddrs = a.Lab.EdgeAddrs
	lab.Monitoring.StatusPage = a.Lab.StatusPage
	return lab
}

// Assigned is this server's services and the lab config to generate
// them with, as of its current assignment (e.g. for backups and restores).
func (n *Local) Assigned() (config.Lab, []state.Service) {
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	return n.lab(a), a.Services
}

// currentLab is the lab config for the current assignment.
func (n *Local) currentLab() config.Lab {
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	return n.lab(a)
}

// service finds a service in the current assignment.
func (n *Local) service(name string) (state.Service, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.serviceLocked(name)
}

func (n *Local) serviceLocked(name string) (state.Service, bool) {
	i := slices.IndexFunc(n.assignment.Services, func(s state.Service) bool { return s.Name() == name })
	if i < 0 {
		return state.Service{}, false
	}
	return n.assignment.Services[i], true
}

// UnitStatuser reads systemd unit states (internal/systemd).
type UnitStatuser interface {
	Status(ctx context.Context, units ...string) ([]systemd.UnitStatus, error)
}

// ServiceStates reports each assigned service's state from its units.
func (n *Local) ServiceStates(ctx context.Context) (map[string]string, error) {
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	lab := n.lab(a)
	out := map[string]string{}
	for _, s := range a.Services {
		name := s.Name()
		out[name] = "unknown"
		gen, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			out[name] = "failed"
			continue
		}
		if n.Units == nil {
			continue
		}
		units, err := n.Units.Status(ctx, gen.Units...)
		if err != nil {
			n.Log.Error("reading service status", "service", name, "err", err)
			continue
		}
		out[name] = Summarize(units)
	}
	return out, nil
}

// Summarize turns unit states into one status for people.
func Summarize(units []systemd.UnitStatus) string {
	if len(units) == 0 {
		return "unknown"
	}
	status := "running"
	for _, u := range units {
		switch {
		case u.ActiveState == "failed":
			return "failed"
		case u.ActiveState == "activating" || u.ActiveState == "reloading":
			status = "starting"
		case u.ActiveState != "active" && status == "running":
			status = "stopped"
		}
	}
	return status
}

// addStorage adds the backup store to desired state and writes its
// password files (rest-server reloads them when they change).
func (n *Local) addStorage(d *reconcile.Desired, st *Storage) error {
	if st == nil {
		return nil
	}
	lab := n.Lab
	if lab.OverlayAddr == "" {
		return fmt.Errorf("offering this server's disk for backups needs Roostack's network")
	}
	files, units, err := generate.Storage(lab, lab.OverlayAddr)
	if err != nil {
		return err
	}
	d.System.Quadlets = append(d.System.Quadlets, files...)
	d.System.Units = append(d.System.Units, units...)
	dir := lab.Paths.BackupStoreDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	var users []string
	for name, hash := range st.Users {
		users = append(users, name+":"+hash)
	}
	slices.Sort(users)
	write := func(name string, lines []string) error {
		data := strings.Join(lines, "\n") + "\n"
		path := filepath.Join(dir, name)
		if old, err := os.ReadFile(path); err == nil && string(old) == data {
			return nil
		}
		return fsutil.WritePrivate(path, []byte(data))
	}
	if err := write(generate.StorePasswords, users); err != nil {
		return err
	}
	return write(generate.AdminPasswords, []string{"controller:" + st.Admin})
}

// addGuestStorage adds other labs' stores to desired state and writes
// their password files.
func (n *Local) addGuestStorage(d *reconcile.Desired, a Assignment) error {
	if a.GuestAddr == "" {
		return nil
	}
	for _, g := range a.Guests {
		files, units, err := generate.GuestStorageUnits(n.Lab, a.GuestAddr, g)
		if err != nil {
			return err
		}
		d.System.Quadlets = append(d.System.Quadlets, files...)
		d.System.Units = append(d.System.Units, units...)
		dir := generate.GuestStoreDir(n.Lab, g.User)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		// Each of its servers has its own user; the admin access is the
		// guest lab's controller's (SPEC.md §10.10).
		var lines strings.Builder
		for _, l := range g.StoreLines() {
			lines.WriteString(l + "\n")
		}
		if err := writeIfChanged(filepath.Join(dir, generate.StorePasswords), lines.String()); err != nil {
			return err
		}
		if err := writeIfChanged(filepath.Join(dir, generate.AdminPasswords), g.User+":"+g.AdminHash+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// writeIfChanged writes a private file unless it already has data.
func writeIfChanged(path, data string) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == data {
		return nil
	}
	return fsutil.WritePrivate(path, []byte(data))
}

// GuestUsage is how much a guest lab stores on this server, in bytes.
func (n *Local) GuestUsage(_ context.Context, user string) (int64, error) {
	if !guestUser.MatchString(user) {
		return 0, ErrNotFound
	}
	var total int64
	err := filepath.WalkDir(generate.GuestStoreDir(n.Lab, user), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, err
}

// GuestDirs lists the guest users with data on this server.
func (n *Local) GuestDirs(context.Context) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(n.Lab.Paths.BackupStoreDir(), "guests"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var users []string
	for _, e := range entries {
		if e.IsDir() && guestUser.MatchString(e.Name()) {
			users = append(users, e.Name())
		}
	}
	return users, nil
}

// RemoveGuestDir deletes a removed guest's data for good.
func (n *Local) RemoveGuestDir(ctx context.Context, user string) error {
	if !guestUser.MatchString(user) {
		return ErrNotFound
	}
	dir := generate.GuestStoreDir(n.Lab, user)
	if _, err := os.Stat(dir); err != nil {
		return ErrNotFound
	}
	if out, err := n.unshare(ctx, "rm", "-rf", "--", dir).CombinedOutput(); err != nil {
		return fmt.Errorf("deleting %s: %w: %s", dir, err, out)
	}
	return nil
}

var guestUser = regexp.MustCompile(`^g[0-9]{1,9}$`)
