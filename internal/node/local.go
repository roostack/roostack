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
	"github.com/mylab/mylab/internal/nodeid"
	"github.com/mylab/mylab/internal/reconcile"
	"github.com/mylab/mylab/internal/state"
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
	InstallStatus(ctx context.Context, service string) HookStatus
	RetryInstall(ctx context.Context, service string) error
	RunAction(ctx context.Context, service, action string, inputs map[string]string) (string, error)
	RevealSecret(ctx context.Context, service, key string) (string, error)
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

	// Hooks (hooks.go). State records install hook progress; without
	// State and Exec, hooks don't run.
	State   KV
	Secrets SecretStore
	Exec    ExecFunc
	// HookRetry and HookDeadline tune install hook retries (0: defaults).
	HookRetry, HookDeadline time.Duration

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
	n.assignment = Assignment{Services: a.Services}
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
	services := n.assignment.Services
	n.mu.Unlock()
	desired, err := state.Desired(n.Lab, services)
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
	if fw, ok := firewall.Desired(n.Lab, desired.Services); ok && n.Firewall != nil {
		act.Step("Updating the firewall")
		if err := n.Firewall.ApplyFirewall(ctx, fw); err != nil {
			return fmt.Errorf("applying the firewall: %w", err)
		}
	}
	act.Step("All changes applied")
	return nil
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
