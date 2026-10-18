package node

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
)

// Hook progress, stored as runtime settings (hookKey). A service installed
// before it had install hooks has no entry and runs none.
const (
	hookPending = "pending"
	hookDone    = "done"
	hookFailed  = "failed: " // followed by the error
)

// Defaults for running install hooks.
const (
	defaultHookRetry    = 10 * time.Second
	defaultHookDeadline = 10 * time.Minute
	actionTimeout       = 2 * time.Minute
	maxOutput           = 4 << 10
)

// KV stores small runtime values (the store's settings table).
type KV interface {
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// SecretStore reads and deletes Podman secrets.
type SecretStore interface {
	Read(ctx context.Context, name string) ([]byte, error)
	Remove(ctx context.Context, name string) error
	Replace(ctx context.Context, name string, value []byte) error
}

// ExecFunc runs a command in a container (podman exec) with env passed
// safely, returning its output.
type ExecFunc func(ctx context.Context, container string, env map[string]string, command []string) ([]byte, error)

// HookStatus is the state of a service's install hooks.
type HookStatus struct {
	Running bool   // setting up now
	Pending bool   // waiting for the service to start
	Failed  string // why the last attempt failed
}

// ActionError is an action that ran but failed; Output is what it printed.
type ActionError struct {
	Title  string
	Output string
	Err    error
}

func (e *ActionError) Error() string { return fmt.Sprintf("%s: %v", e.Title, e.Err) }
func (e *ActionError) Unwrap() error { return e.Err }

func hookKey(service string) string { return "hooks." + service + ".install" }

// errSetupStopped ends a service's setup when the service is removed, or
// installed afresh, while it runs. It isn't a failure: the setup's state
// belongs to whatever replaced it.
var errSetupStopped = errors.New("setup stopped: the service was removed or installed again")

// stopSetupLocked stops the service's running setup, if any (n.mu held).
func (n *Local) stopSetupLocked(name string) {
	if cancel := n.hooksCancel[name]; cancel != nil {
		cancel(errSetupStopped)
		delete(n.hooksCancel, name)
	}
}

// InstallStatus reports a service's install hooks.
func (n *Local) InstallStatus(ctx context.Context, name string) HookStatus {
	if n.State == nil {
		return HookStatus{}
	}
	v, err := n.State.Setting(ctx, hookKey(name))
	if err != nil {
		return HookStatus{Failed: err.Error()}
	}
	n.mu.Lock()
	running := n.hooksRunning[name]
	n.mu.Unlock()
	st := HookStatus{Running: running, Pending: v == hookPending && !running}
	if msg, failed := strings.CutPrefix(v, hookFailed); failed {
		st.Failed = msg
	}
	return st
}

// RetryInstall tries a service's failed install again: its containers, if
// one didn't download or start (an apply now, install.go), or else its
// failed install hooks.
func (n *Local) RetryInstall(ctx context.Context, name string) error {
	if p := n.installProgress(name); p != nil && p.Failed != "" {
		n.Reapply("Trying " + name + "'s install again")
		return nil
	}
	if n.State == nil {
		return ErrNotFound
	}
	v, err := n.State.Setting(ctx, hookKey(name))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(v, hookFailed) {
		return nil
	}
	if err := n.State.SetSetting(ctx, hookKey(name), hookPending); err != nil {
		return err
	}
	n.startInstallHooks()
	return nil
}

// startInstallHooks runs pending install hooks in the background, unless
// that is already happening.
func (n *Local) startInstallHooks() {
	if n.State == nil || n.Exec == nil {
		return
	}
	n.mu.Lock()
	if n.hooksBusy {
		n.hooksAgain = true
		n.mu.Unlock()
		return
	}
	n.hooksBusy = true
	n.hooksDone = make(chan struct{})
	done := n.hooksDone
	n.mu.Unlock()

	go func() {
		defer close(done)
		for {
			n.runPendingHooks(context.Background())
			n.mu.Lock()
			if !n.hooksAgain {
				n.hooksBusy = false
				n.mu.Unlock()
				return
			}
			n.hooksAgain = false
			n.mu.Unlock()
		}
	}()
}

// WaitHooks waits for background install hooks (for tests).
func (n *Local) WaitHooks() {
	n.mu.Lock()
	done := n.hooksDone
	n.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (n *Local) runPendingHooks(ctx context.Context) {
	n.mu.Lock()
	services := n.assignment.Services
	n.mu.Unlock()
	for _, s := range services {
		name := s.Name()
		if v, err := n.State.Setting(ctx, hookKey(name)); err != nil || v != hookPending {
			continue
		}
		hctx, cancel := context.WithCancelCause(ctx)
		n.mu.Lock()
		if n.hooksCancel == nil {
			n.hooksCancel = map[string]context.CancelCauseFunc{}
		}
		n.hooksCancel[name] = cancel
		n.mu.Unlock()
		n.setHooksRunning(name, true)
		n.installStep(name, stageSetup, false)
		err := n.runInstallHooks(hctx, s)
		n.setHooksRunning(name, false)
		n.mu.Lock()
		if errors.Is(context.Cause(hctx), errSetupStopped) {
			err = errSetupStopped
		} else {
			delete(n.hooksCancel, name)
		}
		n.mu.Unlock()
		cancel(nil)
		if errors.Is(err, errSetupStopped) {
			// Leave the state alone: Apply set it for what replaced it.
			n.Log.Info("install hooks stopped", "service", name, "reason", err)
			continue
		}
		if err != nil {
			n.Log.Error("install hooks failed", "service", name, "err", err)
			n.State.SetSetting(ctx, hookKey(name), hookFailed+err.Error())
			// The service page's "Setup didn't finish" takes over.
			n.dropInstall(name)
			continue
		}
		n.installStep(name, stageSetup, true)
		n.Log.Info("install hooks done", "service", name)
		n.State.SetSetting(ctx, hookKey(name), hookDone)
		// Settings used only during the install now take their own value.
		if hasInstallValues(s.Package) {
			n.Reapply(name + " finished setting up: its settings for after the install")
		}
		// Forget values only the install needed (e.g. the admin password).
		for _, st := range s.Package.Settings {
			if st.Type == pkgspec.SettingSecret && st.InstallOnly {
				if err := n.Secrets.Remove(ctx, SecretName(name, st.Key)); err != nil {
					n.Log.Error("removing install-only secret", "service", name, "err", err)
				}
			}
		}
	}
}

func (n *Local) setHooksRunning(name string, on bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.hooksRunning == nil {
		n.hooksRunning = map[string]bool{}
	}
	n.hooksRunning[name] = on
}

// runInstallHooks runs each install hook in order. A service that has just
// started may not be ready (e.g. still migrating its database), so each
// hook is retried until it succeeds or the deadline passes.
func (n *Local) runInstallHooks(ctx context.Context, s state.Service) (err error) {
	retry, deadline := n.HookRetry, n.HookDeadline
	if retry == 0 {
		retry = defaultHookRetry
	}
	if deadline == 0 {
		deadline = defaultHookDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	act := n.Activity.Start(activity.Setup, "Setting up "+s.Title())
	defer func() { act.Finish(err) }()
	hooks := s.Package.HooksOn(pkgspec.HookInstall)
	for i, h := range hooks {
		if h.Title != "" {
			act.Step("Setup step %d of %d: %s", i+1, len(hooks), lowerFirst(h.Title))
		} else {
			act.Step("Setup step %d of %d", i+1, len(hooks))
		}
		run, err := generate.Hook(n.currentLab(), s.Package, s.Instance, h, nil)
		if err != nil {
			return err
		}
		if err := n.hookSecrets(ctx, run, nil); err != nil {
			return err
		}
		for attempt := 1; ; attempt++ {
			act.Printf("Attempt %d: running in %s", attempt, run.Container) // env holds secrets: never logged
			out, err := n.Exec(ctx, run.Container, run.Env, run.Command)
			act.Write(out)
			if err == nil {
				break
			}
			act.Printf("%v. %s may still be starting; trying again in %s.", err, s.Title(), retry)
			select {
			case <-ctx.Done():
				if errors.Is(context.Cause(ctx), errSetupStopped) {
					return errSetupStopped
				}
				return fmt.Errorf("%s didn't finish setting up: %s", s.Title(), lastLines(out, err))
			case <-time.After(retry):
			}
		}
	}
	return nil
}

// hookSecrets adds secret values to a hook's environment: secret settings
// from Podman, secret inputs from the form.
func (n *Local) hookSecrets(ctx context.Context, run *generate.HookRun, inputs map[string]string) error {
	for env, secret := range run.Secrets {
		v, err := n.Secrets.Read(ctx, secret)
		if err != nil {
			return fmt.Errorf("reading %s (it may have been used and deleted already): %w", secret, err)
		}
		run.Env[env] = string(v)
	}
	for env, key := range run.Inputs {
		run.Env[env] = inputs[key]
	}
	return nil
}

// RunAction runs one of a service's action hooks (a button on its page)
// and returns what it printed. inputs holds the form's values.
func (n *Local) RunAction(ctx context.Context, name, action string, inputs map[string]string) (string, error) {
	sv, ok := n.service(name)
	if n.Exec == nil || !ok {
		return "", ErrNotFound
	}
	p, in := sv.Package, sv.Instance
	h := p.Action(action)
	if h == nil {
		return "", ErrNotFound
	}
	values := map[string]string{}
	for _, i := range h.Inputs {
		v := inputs[i.Key]
		if i.Type == pkgspec.SettingString {
			v = strings.TrimSpace(v)
		}
		switch {
		case v == "":
			return "", formErrorf("%s is required.", i.Title)
		case i.MinLength > 0 && len([]rune(v)) < i.MinLength:
			return "", formErrorf("%s must be at least %d characters.", i.Title, i.MinLength)
		case strings.ContainsAny(v, "\n\r\x00"):
			return "", formErrorf("%s can't contain line breaks.", i.Title)
		}
		values[i.Key] = v
	}
	run, err := generate.Hook(n.currentLab(), p, in, h, values)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if err := n.hookSecrets(ctx, run, values); err != nil {
		return "", err
	}
	out, err := n.Exec(ctx, run.Container, run.Env, run.Command)
	if err != nil {
		return "", &ActionError{Title: h.Title, Output: lastLines(out, err), Err: err}
	}
	return lastLines(out, nil), nil
}

// RevealSecret returns a secret setting's value for people who need it
// (e.g. Vaultwarden's admin token, to open its admin page). Only settings
// the package marks reveal: true.
func (n *Local) RevealSecret(ctx context.Context, name, key string) (string, error) {
	sv, ok := n.service(name)
	if !ok {
		return "", ErrNotFound
	}
	s := sv.Package.Setting(key)
	if s == nil || s.Type != pkgspec.SettingSecret || !s.Reveal || n.Secrets == nil {
		return "", ErrNotFound
	}
	v, err := n.Secrets.Read(ctx, SecretName(name, key))
	return string(v), err
}

// SecretName is the Podman secret holding a service's secret setting.
func SecretName(svc, key string) string { return "roostack-" + svc + "-" + key }

// lastLines keeps the end of a command's output (and error) for display.
func lastLines(out []byte, err error) string {
	s := strings.TrimSpace(string(out))
	if err != nil {
		s = strings.TrimSpace(s + "\n" + err.Error())
	}
	if len(s) > maxOutput {
		s = "…" + s[len(s)-maxOutput:]
	}
	return s
}

// withInstallValues gives services whose install hooks haven't succeeded
// yet (pending, or failed and waiting for Try again) their settings'
// install values (SPEC.md §8.1): e.g. sign-ups on, so a hook can make the
// first account by signing up. Each such service is a copy; the rest are
// as given.
func (n *Local) withInstallValues(ctx context.Context, services []state.Service) []state.Service {
	if n.State == nil {
		return services
	}
	out := make([]state.Service, len(services))
	for i, s := range services {
		out[i] = s
		if !hasInstallValues(s.Package) {
			continue
		}
		v, err := n.State.Setting(ctx, hookKey(s.Name()))
		if err != nil || (v != hookPending && !strings.HasPrefix(v, hookFailed)) {
			continue
		}
		in := *s.Instance
		in.Settings = maps.Clone(in.Settings)
		if in.Settings == nil {
			in.Settings = map[string]any{}
		}
		for _, st := range s.Package.Settings {
			if st.InstallValue != nil {
				in.Settings[st.Key] = st.InstallValue
			}
		}
		out[i].Instance = &in
	}
	return out
}

// hasInstallValues reports whether a package has settings with install values.
func hasInstallValues(p *pkgspec.Package) bool {
	return p != nil && slices.ContainsFunc(p.Settings, func(s pkgspec.Setting) bool { return s.InstallValue != nil })
}
