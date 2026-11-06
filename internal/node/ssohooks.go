package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/systemd"
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
	// Due: the hooks will run for a change this server has applied
	// (single sign-on turned on or off, or the lab's address), once the
	// app's install hooks are done.
	Due bool

	// Turning it on or off, for the card while that happens (SPEC.md
	// §5.2, "While it's being set up"); for every app, with hooks or
	// without.

	// Applying: single sign-on was turned on or off, and this server
	// hasn't applied it yet (the app's settings, and its restart).
	// ApplyFailed is why the last apply failed, if it did meanwhile.
	Applying    bool
	ApplyFailed string
	// Applied is when this server applied the last change to it since
	// Roostack started, zero if none.
	Applied time.Time
	// Restarts is how often systemd has started the app's units again
	// since then: an app that stops as it starts with its new settings
	// (Plik when it can't reach the sign-in page).
	Restarts int
}

// ssoApplied is single sign-on as a service's last successful apply
// left it: On, and if that changed it, when and its units' restart count
// then.
type ssoApplied struct {
	On       bool
	At       time.Time
	Restarts int
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
	rec, err := store.GetJSON[*ssoRecord](ctx, n.State, ssoKey(name))
	if err != nil {
		return nil
	}
	return rec
}

func (n *Local) saveSSORecord(ctx context.Context, name string, rec *ssoRecord) {
	if err := store.PutJSON(ctx, n.State, ssoKey(name), rec); err != nil {
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
	s, ok := n.service(name)
	if !ok {
		return st
	}
	if n.Secrets != nil {
		st.NeedsKey = n.missingKey(ctx, s)
	}
	if s.Package == nil || s.Package.SSO == nil {
		return st
	}
	lab := n.currentLab()
	n.mu.Lock()
	applied, known := n.ssoApplied[name]
	running, lastErr := n.status.Running, n.status.LastErr
	n.mu.Unlock()
	if known && applied.On != generate.SSOOn(lab, s.Package, s.Instance) {
		st.Applying = true
		if !running && lastErr != nil {
			st.ApplyFailed = lastErr.Error()
		}
		return st
	}
	st.Applied = applied.At
	if !applied.At.IsZero() && n.Units != nil {
		if out, err := generate.Service(lab, s.Package, s.Instance); err == nil {
			if units, err := n.Units.Status(ctx, out.Units...); err == nil {
				st.Restarts = max(0, restarts(units)-applied.Restarts)
			}
		}
	}
	if len(s.Package.HooksOn(pkgspec.HookSSO)) > 0 && !st.Running {
		if fp, on, _, err := ssoFingerprint(lab, s); err == nil {
			rec := n.ssoRecord(ctx, name)
			st.Due = (rec != nil || on) && (rec == nil || rec.Fingerprint != fp)
		}
	}
	return st
}

// restarts adds up units' restart counts.
func restarts(units []systemd.UnitStatus) int {
	total := 0
	for _, u := range units {
		total += u.NRestarts
	}
	return total
}

// noteSSOApplied records, after a successful apply, single sign-on as it
// left each service with a package that takes it: when it changed, for
// the card (ssoStatus). The first apply since Roostack started changes
// nothing: it only learns how things are.
func (n *Local) noteSSOApplied(ctx context.Context, a Assignment) {
	lab := n.lab(a)
	now := time.Now()
	next := map[string]ssoApplied{}
	for _, s := range a.Services {
		if s.Package == nil || s.Package.SSO == nil {
			continue
		}
		name := s.Name()
		on := generate.SSOOn(lab, s.Package, s.Instance)
		n.mu.Lock()
		prev, known := n.ssoApplied[name]
		n.mu.Unlock()
		switch {
		case !known:
			next[name] = ssoApplied{On: on}
		case prev.On == on:
			next[name] = prev
		default:
			cur := ssoApplied{On: on, At: now}
			if n.Units != nil {
				if out, err := generate.Service(lab, s.Package, s.Instance); err == nil {
					if units, err := n.Units.Status(ctx, out.Units...); err == nil {
						cur.Restarts = restarts(units)
					}
				}
			}
			next[name] = cur
		}
	}
	n.mu.Lock()
	n.ssoApplied = next
	n.mu.Unlock()
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
		// Nor while it waits for its data: its app isn't running, and
		// the data brought in is what they set up (SPEC.md §8.4).
		if s.Instance != nil && s.Instance.AwaitingData {
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
	name := s.Name()
	fp, on, runs, err := ssoFingerprint(lab, s)
	if err != nil {
		return err
	}
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
	err = func() error {
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

// ssoFingerprint is what a service's sso hooks would do now: whether
// single sign-on is on, the runs, and a fingerprint of both, which their
// last successful run is kept with.
func ssoFingerprint(lab config.Lab, s state.Service) (fp string, on bool, runs []*generate.HookRun, err error) {
	p, in := s.Package, s.Instance
	on = generate.SSOOn(lab, p, in)
	sum := sha256.New()
	fmt.Fprintf(sum, "on=%v\n", on)
	for _, h := range p.HooksOn(pkgspec.HookSSO) {
		run, err := generate.Hook(lab, p, in, h, nil)
		if err != nil {
			return "", false, nil, err
		}
		runs = append(runs, run)
		fmt.Fprintln(sum, run.Fingerprint())
	}
	return hex.EncodeToString(sum.Sum(nil)), on, runs, nil
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
