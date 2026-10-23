package manager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/sleep"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// Bringing your own data into a service (SPEC.md §8.4): the service's
// server keeps the import and does the copying (node/import.go); the
// controller switches it in, as one piece of maintenance on that server:
// it holds the server's maintenance queue (downloading the service's
// images first, as an update does), stops the service, copies what
// changed once more, puts the copy in place, loads the dumps, and starts
// it; a service installed to wait for its data starts by the instance
// losing that mark, and a library at its old path goes into the instance
// then too. If it doesn't answer its checks, the data from before goes
// back.

// ImportsOf is bringing data into a service on its server.
func (m *Manager) ImportsOf(service string) (node.Imports, error) {
	_, in, err := m.installedService(service)
	if err != nil {
		return nil, err
	}
	n := m.node(in.HostOr(m.Lab.NodeID))
	if n == nil {
		return nil, usererr.New("Roostack can't reach the server it runs on.")
	}
	if r, ok := n.(RemoteServer); ok && r.Settings() == nil {
		return nil, usererr.New("Roostack can't reach %s right now.", r.Identity().Name)
	}
	x := n.Imports()
	if x == nil {
		return nil, usererr.New("Data can't be brought into services on that server.")
	}
	return x, nil
}

// switching are the services being switched to brought data.
type switchingSet struct {
	sync.Mutex
	m map[string]bool
}

// Switching says whether a service is being switched to its data.
func (m *Manager) Switching(service string) bool {
	m.switching.Lock()
	defer m.switching.Unlock()
	return m.switching.m[service]
}

// SwitchImport starts switching a service to the data brought, in the
// background (activity.ImportFor).
func (m *Manager) SwitchImport(ctx context.Context, service string) error {
	p, in, err := m.installedService(service)
	if err != nil {
		return err
	}
	x, err := m.ImportsOf(service)
	if err != nil {
		return err
	}
	v, err := x.Import(ctx, service)
	if err != nil {
		return err
	}
	if v.Copy == nil || v.Copy.Running || v.Copy.Passes == 0 {
		return usererr.New("Copy your files first: when you switch, Roostack will copy only what's changed since.")
	}
	if !v.Check.Ready() {
		return usererr.New("Check your files again first.")
	}
	host := in.HostOr(m.Lab.NodeID)
	up := node.Capability(m.node(host), node.Node.Updater)
	if up == nil {
		return usererr.New("Roostack can't reach %s's server.", p.Title)
	}
	m.switching.Lock()
	if m.switching.m[service] {
		m.switching.Unlock()
		return usererr.New("%s is being switched already.", p.Title)
	}
	if m.switching.m == nil {
		m.switching.m = map[string]bool{}
	}
	m.switching.m[service] = true
	m.switching.Unlock()
	title := cmp.Or(in.Title, p.Title)
	act := m.Activity.Start(activity.ImportFor(service), "Switching "+title+" to your data")
	go func() {
		ctx := context.Background()
		err := m.switchImport(ctx, act, service, title, host, v, x, up)
		if err != nil {
			m.Log.Error("switching a service to its data", "service", service, "err", err)
		}
		m.switching.Lock()
		delete(m.switching.m, service)
		m.switching.Unlock()
		act.Finish(err)
	}()
	return nil
}

// switchImport is the switch (SwitchImport).
func (m *Manager) switchImport(ctx context.Context, act *activity.Log, service, title, host string, v node.ImportView,
	x node.Imports, up node.Updater) (err error) {
	_, in, err := m.installedService(service)
	if err != nil {
		return err
	}
	before := *in
	after := m.importedInstance(in, v)
	images := []string{}
	if p, err := m.Package(in.Package); err == nil {
		if out, err := generate.Service(m.Lab, p, after); err == nil {
			images = out.Images
		}
	}
	act.Step("Downloading %s's images first, while it keeps running", title)
	id, err := up.StartUpdate(ctx, node.UpdateRequest{Title: "Bringing data into " + title, Images: images})
	if err != nil {
		return err
	}
	defer up.FinishUpdate(context.WithoutCancel(ctx), id)
	if _, err := m.awaitHold(ctx, act, &Update{Service: service, Title: title}, up, id); err != nil {
		return err
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() { // keep the hold
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				up.UpdateStatus(ctx, id)
			}
		}
	}()

	step := func(name, say string, args ...any) ([]string, error) {
		act.Step(say, args...)
		kept, err := m.runSwitchStep(ctx, x, service, name)
		if err != nil {
			act.Printf("%v", err)
		}
		return kept, err
	}
	if _, err := step(node.SwitchStop, "Stopping %s", title); err != nil {
		return err
	}
	committed, started := false, false
	defer func() {
		if err == nil {
			return
		}
		// Back to how it was: the data from before, and the instance.
		if committed {
			act.Step("Putting %s's data from before back", title)
			if _, uerr := m.runSwitchStep(context.WithoutCancel(ctx), x, service, node.SwitchUndo); uerr != nil {
				err = fmt.Errorf("%w; putting its data from before back failed too: %v", err, uerr)
				return
			}
		}
		if started && !instanceEqual(&before, after) {
			if serr := m.saveInstance(&before, "Bringing data into "+title+" didn't work: going back"); serr != nil {
				err = fmt.Errorf("%w; going back failed too: %v", err, serr)
				return
			}
			m.waitApplied(context.WithoutCancel(ctx), m.node(host), time.Now().Add(-time.Second))
		}
		if !before.AwaitingData {
			m.runSwitchStep(context.WithoutCancel(ctx), x, service, node.SwitchStart)
		}
		err = fmt.Errorf("%w; %s has its data from before again", err, title)
	}()
	if _, err := step("copy", "Copying what changed since the last copy"); err != nil {
		return err
	}
	kept, err := step(node.SwitchCommit, "Putting your data in place")
	committed = err == nil || len(kept) > 0
	for _, k := range kept {
		act.Printf("Its data from before is kept at %s until you delete it", k)
	}
	if err != nil {
		return err
	}
	if _, err := step(node.SwitchLoad, "Loading the databases"); err != nil {
		return err
	}
	started = true
	if !instanceEqual(&before, after) {
		act.Step("Starting %s", title)
		if err := m.saveInstance(after, "Bringing data into "+title); err != nil {
			return err
		}
		if err := m.waitApplied(ctx, m.node(host), time.Now().Add(-time.Second)); err != nil {
			return err
		}
	} else if _, err := step(node.SwitchStart, "Starting %s", title); err != nil {
		return err
	}
	act.Step("Checking %s answers", title)
	if err := m.checkHealth(ctx, act, up, service); err != nil {
		return err
	}
	if _, err := m.runSwitchStep(ctx, x, service, node.SwitchFinish); err != nil {
		act.Printf("%v", err)
	}
	act.Step("%s is running with your data", title)
	return nil
}

// switchStepWait bounds one step of the switch (the last copy, a big
// database's load); a variable for tests.
var switchStepWait = 2 * time.Hour

// runSwitchStep does a step of the switch on the service's server and
// waits for it, returning what it set aside.
func (m *Manager) runSwitchStep(ctx context.Context, x node.Imports, service, name string) ([]string, error) {
	if err := x.SwitchImport(ctx, service, name); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(switchStepWait)
	var lastErr error
	for time.Now().Before(deadline) {
		v, err := x.Import(ctx, service)
		switch {
		case err != nil:
			lastErr = err // a moment without the server: try again
		case v.Step != nil && v.Step.Name == name && !v.Step.Running:
			if v.Step.Err != "" {
				return v.Kept, errors.New(v.Step.Err)
			}
			return v.Kept, nil
		}
		if !sleep.For(ctx, cmp.Or(m.waits.statusEvery, statusEvery)) {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("%s took too long (%v)", name, lastErr)
}

// importedInstance is the service's instance once its data is in: no
// longer waiting for it, and reading the libraries matched to its old
// media folders, each at its old path.
func (m *Manager) importedInstance(in *instance.Instance, v node.ImportView) *instance.Instance {
	out := *in
	out.AwaitingData = false
	out.Libraries = slices.Clone(in.Libraries)
	out.LibraryPaths = maps.Clone(in.LibraryPaths)
	for _, l := range v.Plan.Libraries {
		if l.Library == "" {
			continue
		}
		if !slices.Contains(out.Libraries, l.Library) {
			out.Libraries = append(out.Libraries, l.Library)
		}
		if out.LibraryPaths == nil {
			out.LibraryPaths = map[string]string{}
		}
		out.LibraryPaths[l.Library] = l.OldTarget
	}
	slices.Sort(out.Libraries)
	return &out
}

// instanceEqual says whether the switch changes the instance.
func instanceEqual(a, b *instance.Instance) bool {
	return a.AwaitingData == b.AwaitingData && slices.Equal(a.Libraries, b.Libraries) && maps.Equal(a.LibraryPaths, b.LibraryPaths)
}

// saveInstance saves a service's instance and applies.
func (m *Manager) saveInstance(in *instance.Instance, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := in.Save(state.InstanceFile(m.Lab, in.ID())); err != nil {
		return err
	}
	m.startApplyLocked(reason)
	return nil
}
