package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
)

// Single sign-on's hooks (SPEC.md §5.2, "Connecting the remaining apps"):
// after each apply, a service whose single sign-on changed (on, off, or
// what its sso hooks would be given, such as the lab's address) runs its
// `on: sso` hooks, once its install hooks are done. A fingerprint of the
// last successful run is kept, so nothing runs twice for the same values.
// Turning it off also deletes the app's client secret afterwards.

// SSOHookStatus is a service's single sign-on hooks' last run.
type SSOHookStatus struct {
	// Running: they're running now.
	Running bool
	// On is what the last successful run did: connected (true) or
	// disconnected the app.
	On bool
	// Failed is why the last run failed, "" if it didn't.
	Failed string
	// Output is what the last run printed (secrets replaced), and At when
	// it finished.
	Output string
	At     time.Time
	// NeedsKey names the action that makes a key the hooks need and
	// Roostack doesn't have (a service installed before its package
	// made one): "connect".
	NeedsKey string
}

// ssoRecord is kept in the store under ssoKey.
type ssoRecord struct {
	On          bool      `json:"on"`
	Fingerprint string    `json:"fingerprint"`
	Failed      string    `json:"failed,omitempty"`
	Output      string    `json:"output,omitempty"`
	At          time.Time `json:"at"`
}

func ssoKey(service string) string { return "hooks." + service + ".sso" }

func (n *Local) ssoRecord(ctx context.Context, name string) *ssoRecord {
	if n.State == nil {
		return nil
	}
	v, err := n.State.Setting(ctx, ssoKey(name))
	if err != nil || v == "" {
		return nil
	}
	var rec ssoRecord
	if json.Unmarshal([]byte(v), &rec) != nil {
		return nil
	}
	return &rec
}

func (n *Local) saveSSORecord(ctx context.Context, name string, rec *ssoRecord) {
	b, _ := json.Marshal(rec)
	if err := n.State.SetSetting(ctx, ssoKey(name), string(b)); err != nil {
		n.Log.Error("recording single sign-on's hooks", "service", name, "err", err)
	}
}

// ssoStatus is a service's single sign-on hooks for its page.
func (n *Local) ssoStatus(ctx context.Context, name string) SSOHookStatus {
	var st SSOHookStatus
	n.mu.Lock()
	st.Running = n.ssoRunning[name]
	n.mu.Unlock()
	if rec := n.ssoRecord(ctx, name); rec != nil {
		st.On, st.Failed, st.Output, st.At = rec.On, rec.Failed, rec.Output, rec.At
	}
	if s, ok := n.service(name); ok && n.Secrets != nil {
		st.NeedsKey = n.missingKey(ctx, s)
	}
	return st
}

// missingKey is the action that makes a from_hook key a service's sso
// hooks use and Roostack doesn't have, "" if none is missing.
func (n *Local) missingKey(ctx context.Context, s state.Service) string {
	p := s.Package
	for _, h := range p.HooksOn(pkgspec.HookSSO) {
		for _, ref := range h.Secrets {
			st := p.Setting(ref)
			if st == nil || !st.FromHook {
				continue
			}
			if _, err := n.Secrets.Read(ctx, SecretName(s.Name(), ref)); err == nil {
				continue
			}
			for _, a := range p.HooksOn(pkgspec.HookAction) {
				if a.Saves == ref {
					return a.Name
				}
			}
		}
	}
	return ""
}

// retrySSO clears a failed single sign-on run, so the next pass runs it
// again. It reports whether there was one.
func (n *Local) retrySSO(ctx context.Context, name string) bool {
	rec := n.ssoRecord(ctx, name)
	if rec == nil || rec.Failed == "" {
		return false
	}
	rec.Fingerprint = "" // differs from any run's
	n.saveSSORecord(ctx, name, rec)
	return true
}

// runSSOHooks runs each service's sso hooks whose values changed.
func (n *Local) runSSOHooks(ctx context.Context) {
	if n.State == nil || n.Exec == nil || n.Secrets == nil {
		return
	}
	n.mu.Lock()
	services := n.assignment.Services
	n.mu.Unlock()
	for _, s := range services {
		if s.Package == nil || s.Package.SSO == nil {
			continue
		}
		// After its install hooks: an app must be set up first (Immich's
		// key is made then).
		if v, err := n.State.Setting(ctx, hookKey(s.Name())); err != nil || v != "" && v != hookDone {
			continue
		}
		if err := n.syncSSO(ctx, s); err != nil {
			n.Log.Error("single sign-on's hooks", "service", s.Name(), "err", err)
		}
	}
}

// syncSSO runs a service's sso hooks if what they'd do changed since
// their last successful run, then tidies its secrets.
func (n *Local) syncSSO(ctx context.Context, s state.Service) error {
	lab := n.currentLab()
	p, in, name := s.Package, s.Instance, s.Name()
	on := generate.SSOOn(lab, p, in)
	var runs []*generate.HookRun
	sum := sha256.New()
	fmt.Fprintf(sum, "on=%v\n", on)
	for _, h := range p.HooksOn(pkgspec.HookSSO) {
		run, err := generate.Hook(lab, p, in, h, nil)
		if err != nil {
			return err
		}
		runs = append(runs, run)
		fmt.Fprintln(sum, run.Fingerprint())
	}
	fp := hex.EncodeToString(sum.Sum(nil))
	rec := n.ssoRecord(ctx, name)
	switch {
	case rec == nil && !on:
		return nil // never turned on: nothing to undo
	case rec != nil && rec.Fingerprint == fp:
		return nil // done already, or failed and waiting for Try again
	}
	if rec == nil {
		rec = &ssoRecord{}
	}

	var out []byte
	err := func() error {
		if len(runs) == 0 {
			return nil
		}
		n.setSSORunning(name, true)
		defer n.setSSORunning(name, false)
		title := "Connecting " + s.Title() + " to the sign-in page"
		if !on {
			title = "Disconnecting " + s.Title() + " from the sign-in page"
		}
		act := n.Activity.Start(activity.Setup, title)
		retry, deadline := n.HookRetry, n.HookDeadline
		if retry == 0 {
			retry = defaultHookRetry
		}
		if deadline == 0 {
			deadline = defaultHookDeadline
		}
		hctx, cancel := context.WithTimeout(ctx, deadline)
		defer cancel()
		var err error
		for i, run := range runs {
			act.Step("Single sign-on step %d of %d", i+1, len(runs))
			if err = n.ssoHookSecrets(hctx, s, run); err != nil {
				break
			}
			var o []byte
			o, err = n.retryHook(hctx, act, retry, s.Title(), run)
			out = append(out, o...)
			if err != nil {
				break
			}
		}
		act.Finish(err)
		return err
	}()
	rec.Fingerprint, rec.At, rec.Output = fp, time.Now(), lastLines(out, nil)
	if err != nil {
		rec.Failed = err.Error()
		n.saveSSORecord(ctx, name, rec)
		return err
	}
	rec.On, rec.Failed = on, ""
	n.saveSSORecord(ctx, name, rec)
	return n.tidySSOSecrets(ctx, lab, s, on)
}

// ssoHookSecrets adds an sso hook's secrets, saying what to do when a key
// the app makes is missing.
func (n *Local) ssoHookSecrets(ctx context.Context, s state.Service, run *generate.HookRun) error {
	if action := n.missingKey(ctx, s); action != "" {
		title := action
		if h := s.Package.Action(action); h != nil {
			title = h.Title
		}
		return fmt.Errorf("Roostack has no key to %s's settings yet: use %q on its page", s.Title(), title)
	}
	return n.hookSecrets(ctx, run, nil)
}

// tidySSOSecrets deletes what single sign-on no longer needs: the client
// secret and secret env once it's off, and secret env made for other
// values (the lab's last address) while it's on.
func (n *Local) tidySSOSecrets(ctx context.Context, lab config.Lab, s state.Service, on bool) error {
	name := s.Name()
	want := map[string]bool{}
	if on {
		out, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			return err
		}
		for _, sec := range out.Secrets {
			want[sec.Name] = true
		}
	}
	have, err := n.Secrets.List(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for sec := range have {
		stale := strings.HasPrefix(sec, generate.SSOSecretEnvPrefix(name)) || sec == generate.SSOSecretName(name)
		if stale && !want[sec] {
			errs = append(errs, n.Secrets.Remove(ctx, sec))
		}
	}
	return errors.Join(errs...)
}

func (n *Local) setSSORunning(name string, on bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ssoRunning == nil {
		n.ssoRunning = map[string]bool{}
	}
	n.ssoRunning[name] = on
}
