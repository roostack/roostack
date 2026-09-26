package manager

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/pkgspec"
	"github.com/mylab/mylab/internal/state"
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

// InstallStatus reports a service's install hooks.
func (m *Manager) InstallStatus(ctx context.Context, name string) HookStatus {
	if m.State == nil {
		return HookStatus{}
	}
	v, err := m.State.Setting(ctx, hookKey(name))
	if err != nil {
		return HookStatus{Failed: err.Error()}
	}
	m.mu.Lock()
	running := m.hooksRunning[name]
	m.mu.Unlock()
	st := HookStatus{Running: running, Pending: v == hookPending && !running}
	if msg, failed := strings.CutPrefix(v, hookFailed); failed {
		st.Failed = msg
	}
	return st
}

// RetryInstall runs a service's failed install hooks again.
func (m *Manager) RetryInstall(ctx context.Context, name string) error {
	if m.State == nil {
		return ErrNotFound
	}
	v, err := m.State.Setting(ctx, hookKey(name))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(v, hookFailed) {
		return nil
	}
	if err := m.State.SetSetting(ctx, hookKey(name), hookPending); err != nil {
		return err
	}
	m.startInstallHooks()
	return nil
}

// startInstallHooks runs pending install hooks in the background, unless
// that is already happening.
func (m *Manager) startInstallHooks() {
	if m.State == nil || m.Exec == nil {
		return
	}
	m.mu.Lock()
	if m.hooksBusy {
		m.hooksAgain = true
		m.mu.Unlock()
		return
	}
	m.hooksBusy = true
	m.hooksDone = make(chan struct{})
	done := m.hooksDone
	m.mu.Unlock()

	go func() {
		defer close(done)
		for {
			m.runPendingHooks(context.Background())
			m.mu.Lock()
			if !m.hooksAgain {
				m.hooksBusy = false
				m.mu.Unlock()
				return
			}
			m.hooksAgain = false
			m.mu.Unlock()
		}
	}()
}

// WaitHooks waits for background install hooks (for tests).
func (m *Manager) WaitHooks() {
	m.mu.Lock()
	done := m.hooksDone
	m.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (m *Manager) runPendingHooks(ctx context.Context) {
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		m.Log.Error("install hooks: loading services", "err", err)
		return
	}
	for _, s := range services {
		name := s.Package.Name
		if v, err := m.State.Setting(ctx, hookKey(name)); err != nil || v != hookPending {
			continue
		}
		m.setHooksRunning(name, true)
		err := m.runInstallHooks(ctx, s)
		m.setHooksRunning(name, false)
		if err != nil {
			m.Log.Error("install hooks failed", "service", name, "err", err)
			m.State.SetSetting(ctx, hookKey(name), hookFailed+err.Error())
			continue
		}
		m.Log.Info("install hooks done", "service", name)
		m.State.SetSetting(ctx, hookKey(name), hookDone)
		// Forget values only the install needed (e.g. the admin password).
		for _, st := range s.Package.Settings {
			if st.Type == pkgspec.SettingSecret && st.InstallOnly {
				if err := m.Secrets.Remove(ctx, secretName(name, st.Key)); err != nil {
					m.Log.Error("removing install-only secret", "service", name, "err", err)
				}
			}
		}
	}
}

func (m *Manager) setHooksRunning(name string, on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hooksRunning == nil {
		m.hooksRunning = map[string]bool{}
	}
	m.hooksRunning[name] = on
}

// runInstallHooks runs each install hook in order. A service that has just
// started may not be ready (e.g. still migrating its database), so each
// hook is retried until it succeeds or the deadline passes.
func (m *Manager) runInstallHooks(ctx context.Context, s state.Service) error {
	retry, deadline := m.HookRetry, m.HookDeadline
	if retry == 0 {
		retry = defaultHookRetry
	}
	if deadline == 0 {
		deadline = defaultHookDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for _, h := range s.Package.HooksOn(pkgspec.HookInstall) {
		run, err := generate.Hook(m.Lab, s.Package, s.Instance, h, nil)
		if err != nil {
			return err
		}
		if err := m.hookSecrets(ctx, run, nil); err != nil {
			return err
		}
		for {
			out, err := m.Exec(ctx, run.Container, run.Env, run.Command)
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("%s didn't finish setting up: %s", s.Package.Title, lastLines(out, err))
			case <-time.After(retry):
			}
		}
	}
	return nil
}

// hookSecrets adds secret values to a hook's environment: secret settings
// from Podman, secret inputs from the form.
func (m *Manager) hookSecrets(ctx context.Context, run *generate.HookRun, inputs map[string]string) error {
	for env, secret := range run.Secrets {
		v, err := m.Secrets.Read(ctx, secret)
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
func (m *Manager) RunAction(ctx context.Context, name, action string, inputs map[string]string) (string, error) {
	if m.Exec == nil {
		return "", ErrNotFound
	}
	p, in, err := m.installedService(name)
	if err != nil {
		return "", err
	}
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
	run, err := generate.Hook(m.Lab, p, in, h, values)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	if err := m.hookSecrets(ctx, run, values); err != nil {
		return "", err
	}
	out, err := m.Exec(ctx, run.Container, run.Env, run.Command)
	if err != nil {
		return "", &ActionError{Title: h.Title, Output: lastLines(out, err), Err: err}
	}
	return lastLines(out, nil), nil
}

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
