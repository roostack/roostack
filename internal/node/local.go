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
	"sync/atomic"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/firewall"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labdns"
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
	// RunScheduled runs one of its scheduled tasks as soon as its
	// maintenance allows (scheduled.go).
	RunScheduled(ctx context.Context, service, hook string) error
	RevealSecret(ctx context.Context, service, key string) (string, error)
	// ShownFiles are the small files its package shows on its page.
	ShownFiles(ctx context.Context, service string) ([]string, error)
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
	// CleanUp lists and deletes images nothing uses (images.go).
	CleanUp() CleanUp
	History() History
	// Linked reports its direct links to other servers and GuestHost
	// keeps other labs' backups (guesthost.go); Processor says what kind
	// of processor it has (arch.go).
	Linked() Linked
	GuestHost() GuestHost
	Processor() Processor
	// Photos migrates photos into its Immich (photos.go).
	Photos() Photos
	// LabDNS is Roostack's own DNS server there (labdns.go).
	LabDNS() LabDNS
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
	// PortForwards are services' public ports this server sends on to
	// another (SPEC.md §10.5): on the edge, to the controller; on the
	// controller, from the edge to the server that runs the service.
	PortForwards []agent.PortForward
	// Tunnels are the other servers' addresses on Roostack's network this
	// one has a tunnel to, for public ports that keep visitors' addresses
	// (agent.Tunnel).
	Tunnels []string
	// Libraries are this server's media libraries (SPEC.md §8.1, "Media
	// libraries"): mounted, and the rest unmounted, by its agent.
	Libraries []agent.Library
	// People, on the server the lab's VPN's devices connect to, is its
	// interface (agent.PeopleInterface) with every device as a peer
	// (SPEC.md §10.11); nil elsewhere, where it's removed if it was here.
	People *agent.WireGuard
	// Relay, on a server that serves public names itself while the lab
	// comes in through Roostack Cloud's relay, is its own tunnel to the
	// relay's edges (agent.RelayInterface; SPEC.md §10.5): its key is made
	// here, and before Cloud has given it an address it has a placeholder
	// and no peers. Nil elsewhere, where it's stopped if it was here. Not
	// on the controller, whose tunnel Roostack Cloud's link keeps.
	Relay *agent.WireGuard
	// Glances, when set, is where this server's Glances listens, for its
	// live view (SPEC.md §6.15.1); "" for none.
	Glances string
	// Reason says why it was sent, for the apply's log.
	Reason string
	// CaddyFirst: the change is to web addresses and mustn't wait behind
	// an apply that's running, such as a download (a pause of public
	// access, SPEC.md §6.16). The server running Caddy writes Caddy's
	// files and reloads it at once, then applies as usual. Not sent to
	// other servers: only the controller's Caddy serves the lab's
	// addresses.
	CaddyFirst bool
}

// startedReason is why an apply with no reason ran.
const startedReason = "Roostack started, or its settings changed"

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
	// Doing are the changes the running apply is making, and Waiting the
	// ones that arrived since, oldest first: they'll apply together once
	// it has finished, so people can see why a change they made hasn't
	// happened yet.
	Doing, Waiting []Change
}

// Change is a change sent to a server: why (Assignment.Reason), and
// whether its part for web addresses is applied already (CaddyFirst).
type Change struct {
	Reason string `json:"reason"`
	Early  bool   `json:"early,omitempty"`
}

// ResolverSetter is roostack-agent's DNS server operation (SPEC.md
// §6.5.2).
type ResolverSetter interface {
	SetResolver(ctx context.Context, servers []string) error
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
	// DNSClient asks Roostack's own DNS server on this server (SPEC.md
	// §6.5.3), on its socket.
	DNSClient labdns.Client
	// Resolver sets the DNS server this server's own lookups use, when
	// the Owner chose the lab's (SPEC.md §6.5.2). Nil leaves it alone.
	Resolver ResolverSetter
	// resolverSet is the last one set, "" for its own, and resolverDone
	// whether it has been since Roostack started.
	resolverSet  string
	resolverDone bool
	// labDNSOn: Roostack's own DNS server runs here (labdns.go).
	labDNSOn bool
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
	// SetUpDone, on the controller, is told when a service's install hooks
	// have succeeded, so its addresses waiting for setup can be public
	// (manager.Manager.SetUpDone, SPEC.md §8.1).
	SetUpDone func(service string)

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
	// role and overlayAddr are the server's role and address on Roostack's
	// network once it's set up while running (UseNetwork); "" uses Lab's.
	role, overlayAddr string
	// vitals reads how the server is doing (vitals.go).
	vitalsOnce sync.Once
	vitals     *vitalsReader
	status     ApplyStatus
	// again is set when an assignment arrives during an apply, so another
	// apply runs afterwards.
	again bool
	done  chan struct{}
	// caddyMu keeps caddyNow's writes of Caddy's files one at a time, and
	// caddyGen counts them, so an apply planned before one writes them
	// again (caddyNow).
	caddyMu  sync.Mutex
	caddyGen atomic.Int64
	// fwMu keeps the firewall's applies one at a time, and lastFW is the
	// one applied last, which a closing of public access changes at once
	// (firewallNow).
	fwMu   sync.Mutex
	lastFW *agent.Firewall
	// nowWG is the firewall and Caddy changes Apply makes at once, in
	// the background, so Wait covers them too.
	nowWG sync.WaitGroup

	hooksBusy, hooksAgain bool
	hooksRunning          map[string]bool
	// ssoRunning: a service's single sign-on hooks are running.
	ssoRunning map[string]bool
	// ssoApplied is single sign-on as the last successful apply left
	// each service (noteSSOApplied), nil until the first.
	ssoApplied map[string]ssoApplied
	// statusApplied is the public status page as the last successful
	// apply left Caddy (noteStatusPage).
	statusApplied StatusPageApplied
	// hooksCancel stops a service's running setup, when the service is
	// removed or installed afresh meanwhile (errSetupStopped).
	hooksCancel map[string]context.CancelCauseFunc
	hooksDone   chan struct{}
	// scheduled is what each scheduled task is doing on the coordinator,
	// by scheduledKey (scheduled.go): queued or running.
	scheduled map[string]int
	// scheduledTimeout, if set, replaces every scheduled task's timeout
	// (tests: a package's is at least a minute).
	scheduledTimeout time.Duration

	// followingImage: Caddy's image download is being shown
	// (caddyimage.go), and imagePoll how often its log is read (zero:
	// caddyimage.go's imagePoll).
	followingImage bool
	imagePoll      time.Duration
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
	// cfg is the look for the files config file blocks wait for
	// (configfiles.go).
	cfg configWait
	// cpu keeps the last reading of how busy the processors were
	// (usage.go).
	cpu cpuSampler
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
	// All of it but what's for this apply alone, so that a field added
	// later can't be left out (the tunnels were, at first).
	n.assignment = a
	n.assignment.Secrets, n.assignment.SetUp, n.assignment.Reason, n.assignment.CaddyFirst = nil, nil, "", false
	if n.State != nil {
		// A service taken off this server forgets its setup progress, and
		// stops any setup still running: its hooks would keep retrying
		// for a service that's gone.
		for _, s := range old {
			if _, still := n.serviceLocked(s.Name()); !still {
				n.stopSetupLocked(s.Name())
				n.State.SetSetting(context.Background(), hookKey(s.Name()), "")
				n.State.SetSetting(context.Background(), ssoKey(s.Name()), "")
				n.forgetScheduled(s)
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
	ch := Change{Reason: cmp.Or(a.Reason, startedReason), Early: a.CaddyFirst}
	// Closing public access completely, or opening it again, reaches the
	// firewall at once, on every server (SPEC.md §6.16).
	n.nowWG.Add(1)
	go func() {
		defer n.nowWG.Done()
		if err := n.firewallNow(context.Background()); err != nil {
			n.Log.Error("closing or opening public access at the firewall at once", "reason", ch.Reason, "err", err)
		}
	}()
	if a.CaddyFirst {
		n.nowWG.Add(1)
		go func() {
			defer n.nowWG.Done()
			if err := n.caddyNow(context.Background()); err != nil {
				n.Log.Error("applying web address changes at once", "reason", ch.Reason, "err", err)
			}
		}()
	}
	if n.status.Running {
		n.again = true
		// The same change twice (pausing, then pausing more) waits once.
		if w := n.status.Waiting; len(w) > 0 && w[len(w)-1].Reason == ch.Reason {
			w[len(w)-1].Early = w[len(w)-1].Early || ch.Early
		} else {
			n.status.Waiting = append(n.status.Waiting, ch)
		}
		return
	}
	n.status.Running, n.status.Doing = true, []Change{ch}
	n.done = make(chan struct{})
	go n.applyLoop(n.done)
}

// caddyNow writes Caddy's files for this server's newest assignment and
// reloads Caddy, without waiting for an apply that's running (SPEC.md
// §6.16): visitors from outside are turned away within seconds of a pause,
// even while a download holds the apply up. Only Caddy's files change;
// the apply that follows does the rest (public ports at the firewall). An
// apply planned before this writes Caddy's files again from the newest
// assignment once it has written its own (applyOnce's AfterWrite), so it
// can't undo it.
func (n *Local) caddyNow(ctx context.Context) error {
	n.caddyMu.Lock()
	defer n.caddyMu.Unlock()
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	lab := n.lab(a)
	if !lab.CaddyHere() || n.Reconciler == nil {
		return nil // the controller's Caddy serves the lab's addresses
	}
	desired, err := state.Desired(lab, n.withInstallValues(ctx, a.Services), a.Proxied...)
	if err != nil {
		return err
	}
	r := *n.Reconciler
	r.Log, r.UserSecret = nil, n.userSecret
	plan, err := r.Plan(ctx, desired)
	if err != nil {
		return err
	}
	changed, err := r.ApplyCaddy(ctx, plan)
	n.caddyGen.Add(1)
	if err == nil && changed {
		n.Log.Info("applied changes to web addresses at once")
	}
	return err
}

// firewallNow applies the newest assignment's closing of public access
// (config.PublicPause.Closed) to the firewall applied last, without
// waiting for an apply that's running: visitors from outside are refused
// within seconds, even while a download holds the apply up. The apply
// itself takes the newest closing too (applyFirewall), so one planned
// before can't undo it.
func (n *Local) firewallNow(ctx context.Context) error {
	n.fwMu.Lock()
	defer n.fwMu.Unlock()
	closed := n.closedNow()
	if n.lastFW == nil || n.Firewall == nil || n.lastFW.Closed == closed {
		return nil
	}
	fw := firewall.WithClosed(*n.lastFW, closed)
	if err := n.Firewall.ApplyFirewall(ctx, fw); err != nil {
		return err
	}
	n.lastFW = &fw
	n.Log.Info("applied the firewall at once", "closed", closed)
	return nil
}

// applyFirewall applies an apply's firewall, closed or not as the newest
// assignment says.
func (n *Local) applyFirewall(ctx context.Context, fw agent.Firewall) error {
	n.fwMu.Lock()
	defer n.fwMu.Unlock()
	fw = firewall.WithClosed(fw, n.closedNow())
	if err := n.Firewall.ApplyFirewall(ctx, fw); err != nil {
		return err
	}
	n.lastFW = &fw
	return nil
}

// closedNow says whether the newest assignment closes public access.
func (n *Local) closedNow() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.assignment.Lab.Pause.Closed != nil
}

// userSecret is a value typed in for a secret not created yet.
func (n *Local) userSecret(name string) ([]byte, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	v, ok := n.userSecrets[name]
	return v, ok
}

// LatestActivity is this server's latest log of a kind.
func (n *Local) LatestActivity(kind string) (activity.Snapshot, bool) { return n.Activity.Latest(kind) }

// Status reports the current or last apply.
func (n *Local) Status() ApplyStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.status
	st.Doing, st.Waiting = slices.Clone(st.Doing), slices.Clone(st.Waiting)
	return st
}

// Wait blocks until the current apply finishes, and the changes Apply
// made at once (for tests and tools).
func (n *Local) Wait() {
	n.mu.Lock()
	done := n.done
	n.mu.Unlock()
	if done != nil {
		<-done
	}
	n.nowWG.Wait()
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
			n.status.Running, n.status.Doing = false, nil
			n.mu.Unlock()
			return
		}
		n.again = false
		n.status.Doing, n.status.Waiting = n.status.Waiting, nil
		n.mu.Unlock()
	}
}

func (n *Local) applyOnce(ctx context.Context) (err error) {
	// Every apply has a log saying why it ran and what it did; one that
	// changed nothing (as at most startups) is routine: shown as the
	// latest, not kept as history (SPEC.md §6.14).
	n.mu.Lock()
	a := n.assignment
	doing := slices.Clone(n.status.Doing)
	n.mu.Unlock()
	gen := n.caddyGen.Load()
	act := n.Activity.Start(activity.Apply, "Applying changes")
	defer func() { act.Finish(err) }()
	if len(doing) == 0 {
		doing = []Change{{Reason: startedReason}}
	}
	for _, ch := range doing {
		act.Printf("Why: %s.", ch.Reason)
	}
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
	r := *n.Reconciler
	r.UserSecret = n.userSecret
	// Changes to web addresses applied at once since this apply began
	// (caddyNow) are written again over its older Caddy files.
	r.AfterWrite = func(ctx context.Context) error {
		if n.caddyGen.Load() == gen {
			return nil
		}
		return n.caddyNow(ctx)
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
	// Caddy's image downloads in the background: show it, whether this
	// apply starts it or it was already running when Roostack restarted
	// (e.g. at the end of the setup wizard).
	caddyHere := n.lab(a).CaddyHere()
	downloading := caddyHere && (slices.Contains(plan.ImageUnits, CaddyImageUnit) || n.caddyDownloading(ctx))
	if downloading {
		n.clearImageLog(ctx, plan, n.lab(a).Paths.CaddyImageLog())
	}
	// New services are shown step by step while they install.
	n.planInstalls(a.Services, desired, plan, downloading)
	r.Progress = n.installEvent
	if err := r.Apply(ctx, plan); err != nil {
		// Caddy didn't start because its image didn't download: say so,
		// rather than systemd's "a dependency job failed", which stays in
		// the log.
		if why := n.caddyImageFailed(ctx, n.lab(a).Paths.CaddyImageLog()); caddyHere && why != "" {
			act.Printf("%v", err)
			err = errors.New(why)
		}
		n.failInstalls(err)
		return err
	}
	n.installsApplied()
	n.waitForConfigs(plan.ConfigsWaiting)
	for _, left := range plan.ConfigsLeft {
		act.Printf("Config file left as it is: %s", left)
		n.Log.Warn("config file left as it is", "why", left)
	}
	if downloading {
		n.followCaddyImage(n.lab(a).Paths.CaddyImageLog())
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
	n.removeOldEnvSecrets(ctx, act, a.Services, desired)

	// The firewall follows the services, e.g. a package port just turned on.
	roles := firewall.Roles{Store: a.Storage != nil, ProxyPorts: a.ProxyPorts, GuestAddr: a.GuestAddr, Guests: a.Guests,
		LinksListen: a.Links != nil && a.Links.Listen, VPN: a.People != nil, PortForwards: a.PortForwards, Tunnels: a.Tunnels, Relayed: a.Relay != nil}
	if err := n.applyPeople(ctx, act, a.People); err != nil {
		return fmt.Errorf("setting up the lab's VPN: %w", err)
	}
	if err := n.applyRelay(ctx, act, a.Relay, n.lab(a).IsNode()); err != nil {
		return fmt.Errorf("setting up this server's tunnel to Roostack Cloud's relay: %w", err)
	}
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
		if err := n.applyFirewall(ctx, fw); err != nil {
			return fmt.Errorf("applying the firewall: %w", err)
		}
	}
	if want := a.Lab.Resolver; n.Resolver != nil && (want != n.resolverSet || !n.resolverDone) {
		var servers []string
		if want != "" {
			act.Printf("This server will look names up with the lab's DNS server, at %s", want)
			servers = []string{want}
		} else if n.resolverDone {
			act.Printf("This server will look names up with its own DNS servers again")
		}
		// One it can't take (no systemd-resolved) is said once, in the
		// log, rather than failing every apply until it's unticked.
		if err := n.Resolver.SetResolver(ctx, servers); err != nil {
			act.Printf("This server's DNS server wasn't changed: %v", err)
			n.Log.Warn("setting this server's DNS server", "err", err)
		}
		n.resolverSet, n.resolverDone = want, true
	}
	if err := n.applyLabDNS(n.lab(a).Paths.LabDNSNames(), a.Lab.DNS, act.Printf); err != nil {
		return fmt.Errorf("setting up Roostack's own DNS server: %w", err)
	}
	if a.Links != nil && n.Links != nil {
		act.Step("Updating direct links to other servers")
		for _, p := range a.Links.Peers {
			to := "(that server will connect to this one)"
			if p.Endpoint != "" {
				to = "at " + p.Endpoint
			}
			act.Printf("%s %s", strings.Join(p.AllowedIPs, ", "), to)
		}
		if err := n.Links.ApplyLinks(ctx, *a.Links); err != nil {
			return fmt.Errorf("setting up direct links: %w", err)
		}
	}
	n.noteSSOApplied(ctx, a)
	n.noteStatusPage(a)
	// When each image was last named, so that one an update replaced is
	// kept a week for going back (images.go).
	n.noteImagesInUse(ctx)
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

// UseNetwork takes this server's role and address on Roostack's network,
// set up while running (the controller's first join code), without a
// restart: the next apply's firewall lets servers join, and one starts
// now.
func (n *Local) UseNetwork(role, addr string) {
	n.mu.Lock()
	n.role, n.overlayAddr = role, addr
	a := n.assignment
	n.mu.Unlock()
	a.Secrets, a.SetUp, a.Reason = nil, nil, "Roostack's network between servers is set up"
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
	if n.overlayAddr != "" {
		lab.Role, lab.OverlayAddr = n.role, n.overlayAddr
	}
	n.mu.Unlock()
	if a.Lab.Domain != "" {
		lab.Domain, lab.Email = a.Lab.Domain, a.Lab.Email
		lab.Public.NoSharing = a.Lab.NoSharing
		if a.Lab.Name != "" {
			lab.Name = a.Lab.Name
		}
		if a.Lab.TestAddress && lab.IsNode() {
			lab.DNS.Provider = config.ProviderTestAddress
		}
	}
	lab.Direct, lab.DirectHome, lab.SignInServers = a.Lab.Direct, a.Lab.DirectHome, a.Lab.SignInServers
	if lab.IsNode() {
		lab.Pass = a.Edge
	}
	lab.EdgeAddrs = a.Lab.EdgeAddrs
	lab.HomeAddress = a.Lab.HomeAddress
	lab.LabDNSHere = a.Lab.DNS != nil
	lab.Monitoring.StatusPage = a.Lab.StatusPage
	lab.Public.Pause = a.Lab.Pause
	lab.Public.Refused = a.Lab.Refused
	lab.DomainItself = a.Lab.DomainItself
	lab.Devices = a.Lab.Devices
	lab.GlancesHere, lab.GlancesBind, lab.GlancesSites = a.Glances != "", a.Glances, a.Lab.GlancesSites
	if m, err := n.MemoryInfo(context.Background()); err == nil {
		lab.MemoryTotal = m.Total
	}
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
	// Every service's units in one systemctl call: on a small server
	// whose memory is tight, each call is a program read back from disk
	// (a page that lists services waited seconds for one per service).
	unitsOf := map[string][]string{}
	var all []string
	for _, s := range a.Services {
		name := s.Name()
		out[name] = "unknown"
		gen, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			out[name] = "failed"
			continue
		}
		unitsOf[name] = gen.Units
		all = append(all, gen.Units...)
	}
	if n.Units == nil || len(all) == 0 {
		return out, nil
	}
	statuses, err := n.Units.Status(ctx, all...)
	if err != nil {
		n.Log.Error("reading service status", "err", err)
		return out, nil
	}
	byUnit := map[string]systemd.UnitStatus{}
	for _, u := range statuses {
		byUnit[u.Unit] = u
	}
	for name, units := range unitsOf {
		var mine []systemd.UnitStatus
		for _, u := range units {
			if st, ok := byUnit[u]; ok {
				mine = append(mine, st)
			}
		}
		out[name] = Summarize(mine)
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
	lab := n.currentLab()
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
