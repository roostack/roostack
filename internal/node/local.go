package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"roostack.cloud/roostack/internal/labnet"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/firewall"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/systemd"
)

// Node is a server in the lab, as the controller sees it (SPEC.md §10.3).
// Local runs on this server; a remote implementation will speak the same
// operations to other servers over Roostack's network.
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

// FormError is a problem the user can fix in a form.
type FormError struct{ Msg string }

func (e *FormError) Error() string { return e.Msg }

func formErrorf(format string, args ...any) error { return &FormError{fmt.Sprintf(format, args...)} }

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
	Links   LinkAgent
	Version string

	sizes sizes

	// Hooks (hooks.go). State records install hook progress; without
	// State and Exec, hooks don't run.
	State   KV
	Secrets SecretStore
	Exec    ExecFunc
	// HookRetry and HookDeadline tune install hook retries (0: defaults).
	HookRetry, HookDeadline time.Duration

	// Jobs are the server's backups, restores and OS updates.
	Jobs Maintenance
	// Server changes the server's own settings (optional).
	Host Settings

	mu         sync.Mutex
	assignment Assignment
	// userSecrets holds typed-in values until an apply creates the Podman
	// secrets. Never written to disk.
	userSecrets map[string][]byte
	// replaceSecrets are values typed again for a service being set up
	// (a failed install retried with new values): they replace the Podman
	// secrets it already has, before its install hooks run.
	replaceSecrets map[string][]byte
	status         ApplyStatus
	// again is set when an assignment arrives during an apply, so another
	// apply runs afterwards.
	again bool
	done  chan struct{}

	hooksBusy, hooksAgain bool
	hooksRunning          map[string]bool
	hooksDone             chan struct{}
}

// Identity is this server's identity.
func (n *Local) Identity() nodeid.Identity { return n.ID }

// Apply records the latest assignment and applies it in the background. If
// an apply is running, another follows it, so the latest always wins.
func (n *Local) Apply(a Assignment) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.userSecrets == nil {
		n.userSecrets = map[string][]byte{}
	}
	for k, v := range a.Secrets {
		n.userSecrets[k] = v
	}
	old := n.assignment.Services
	n.assignment = Assignment{Services: a.Services, Lab: a.Lab, Proxied: a.Proxied, Storage: a.Storage,
		Guests: a.Guests, GuestAddr: a.GuestAddr, ProxyPorts: a.ProxyPorts}
	if n.State != nil {
		// A service taken off this server forgets its setup progress.
		for _, s := range old {
			if _, still := n.serviceLocked(s.Package.Name); !still {
				n.State.SetSetting(context.Background(), hookKey(s.Package.Name), "")
			}
		}
		for _, name := range a.SetUp {
			if err := n.State.SetSetting(context.Background(), hookKey(name), hookPending); err != nil {
				n.Log.Error("recording pending setup", "service", name, "err", err)
			}
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
	// A log only for applies that change something (or fail): the one at
	// every startup usually has nothing to do.
	var act *activity.Log
	defer func() {
		if err != nil && act == nil {
			act = n.Activity.Start(activity.Apply, "Applying changes")
		}
		act.Finish(err)
	}()
	n.mu.Lock()
	a := n.assignment
	n.mu.Unlock()
	desired, err := state.Desired(n.lab(a), a.Services, a.Proxied...)
	if err != nil {
		return err
	}
	if err := n.addStorage(&desired, a.Storage); err != nil {
		return err
	}
	if err := n.addGuestStorage(&desired, a); err != nil {
		return err
	}
	r := *n.Reconciler
	r.UserSecret = func(name string) ([]byte, bool) {
		n.mu.Lock()
		defer n.mu.Unlock()
		v, ok := n.userSecrets[name]
		return v, ok
	}
	plan, err := r.Plan(ctx, desired)
	if err != nil {
		return err
	}
	if !plan.Empty() {
		act = n.Activity.Start(activity.Apply, "Applying changes")
		r.Log = act
	}
	if err := r.Apply(ctx, plan); err != nil {
		return err
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
	if fw, ok := firewall.Desired(n.lab(a), desired.Services); ok && n.Firewall != nil {
		if a.Storage != nil {
			for _, port := range []int{generate.StoragePort, generate.StorageAdminPort} {
				fw.Allow = append(fw.Allow, agent.Rule{Port: port, Proto: "tcp", Iface: agent.WireGuardInterface,
					Reason: "backup store"})
			}
		}
		for _, port := range a.ProxyPorts {
			fw.Allow = append(fw.Allow, agent.Rule{Port: port, Proto: "tcp", Iface: agent.WireGuardInterface,
				Reason: "backups to another lab"})
		}
		// Other labs reach only their own stores and the guest API.
		if a.GuestAddr != "" {
			fw.Allow = append(fw.Allow,
				agent.Rule{Port: agent.GuestsPort, Proto: "udp", From: agent.FromAny, Reason: "storage for other labs (wireguard)"},
				agent.Rule{Port: labnet.GuestAPIPort, Proto: "tcp", Iface: agent.GuestsInterface, Reason: "storage for other labs"})
			for _, g := range a.Guests {
				for _, port := range []int{g.Port, g.Port + 1} {
					fw.Allow = append(fw.Allow, agent.Rule{Port: port, Proto: "tcp", Iface: agent.GuestsInterface,
						Reason: "storage for " + g.User})
				}
			}
		}
		act.Step("Updating the firewall")
		if err := n.Firewall.ApplyFirewall(ctx, fw); err != nil {
			return fmt.Errorf("applying the firewall: %w", err)
		}
	}
	act.Step("All changes applied")
	return nil
}

// lab is the node's lab config with the assignment's lab-wide settings.
func (n *Local) lab(a Assignment) config.Lab {
	lab := n.Lab
	if a.Lab.Domain != "" {
		lab.Domain, lab.Email = a.Lab.Domain, a.Lab.Email
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
	i := slices.IndexFunc(n.assignment.Services, func(s state.Service) bool { return s.Package.Name == name })
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
		name := s.Package.Name
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
		if err := os.WriteFile(path+".tmp", []byte(data), 0o600); err != nil {
			return err
		}
		return os.Rename(path+".tmp", path)
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
		for name, hash := range map[string]string{generate.StorePasswords: g.StoreHash, generate.AdminPasswords: g.AdminHash} {
			if err := writeIfChanged(filepath.Join(dir, name), g.User+":"+hash+"\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeIfChanged writes a private file unless it already has data.
func writeIfChanged(path, data string) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == data {
		return nil
	}
	if err := os.WriteFile(path+".tmp", []byte(data), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
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
