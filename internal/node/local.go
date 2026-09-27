package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/firewall"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/nodeid"
	"github.com/mylab/mylab/internal/reconcile"
	"github.com/mylab/mylab/internal/state"
	"github.com/mylab/mylab/internal/systemd"
)

// Node is a machine in the lab, as the controller sees it (SPEC.md §10.3).
// Local runs on this machine; a remote implementation will speak the same
// operations to other machines over MyLab's network.
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
	// Settings are the machine's own settings (settings.go); nil when
	// they can't be changed from MyLab.
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
	// Proxied are services on other machines whose routes this machine's
	// Caddy serves (the controller, SPEC.md §10.5).
	Proxied []state.Proxied
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

// FirewallApplier is mylab-agent's firewall operation.
type FirewallApplier interface {
	ApplyFirewall(ctx context.Context, fw agent.Firewall) error
}

// Local is this machine: it applies its assignment (Quadlets, secrets,
// Caddy, firewall) and runs package hooks.
type Local struct {
	ID  nodeid.Identity
	Lab config.Lab
	// Reconciler applies desired state. Its UserSecret is set by Local.
	Reconciler *reconcile.Reconciler
	Log        *slog.Logger
	// Activity shows applies and install hooks live in the UI (optional).
	Activity *activity.Registry
	// Firewall applies the host firewall through mylab-agent after each
	// apply. Nil leaves the firewall alone.
	Firewall FirewallApplier
	// Units reads systemd unit states, for ServiceStates (optional).
	Units UnitStatuser

	// Hooks (hooks.go). State records install hook progress; without
	// State and Exec, hooks don't run.
	State   KV
	Secrets SecretStore
	Exec    ExecFunc
	// HookRetry and HookDeadline tune install hook retries (0: defaults).
	HookRetry, HookDeadline time.Duration

	// Jobs are the machine's backups, restores and OS updates.
	Jobs Maintenance
	// Machine changes the machine's own settings (optional).
	Machine Settings

	mu         sync.Mutex
	assignment Assignment
	// userSecrets holds typed-in values until an apply creates the Podman
	// secrets. Never written to disk.
	userSecrets map[string][]byte
	status      ApplyStatus
	// again is set when an assignment arrives during an apply, so another
	// apply runs afterwards.
	again bool
	done  chan struct{}

	hooksBusy, hooksAgain bool
	hooksRunning          map[string]bool
	hooksDone             chan struct{}
}

// Identity is this machine's identity.
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
	n.assignment = Assignment{Services: a.Services, Lab: a.Lab, Proxied: a.Proxied}
	if n.State != nil {
		// A service taken off this machine forgets its setup progress.
		for _, s := range old {
			if _, still := n.serviceLocked(s.Package.Name); !still {
				n.State.SetSetting(context.Background(), hookKey(s.Package.Name), "")
			}
		}
		for _, name := range a.SetUp {
			if err := n.State.SetSetting(context.Background(), hookKey(name), hookPending); err != nil {
				n.Log.Error("recording pending setup", "service", name, "err", err)
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

// LatestActivity is this machine's latest log of a kind.
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
	}
	n.mu.Unlock()

	// The firewall follows the services, e.g. a package port just turned on.
	if fw, ok := firewall.Desired(n.lab(a), desired.Services); ok && n.Firewall != nil {
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
