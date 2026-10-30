package manager

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/sleep"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/words"
)

// Public addresses wait for setup (SPEC.md §8.1): a service installed
// with install hooks to run is instance.SettingUp, which keeps its
// addresses and ports private wherever that's worked out (generate's
// routes and ports, so Caddy, the firewall, the edge, Roostack Cloud's
// relay and DNS records), until the server running it says its hooks
// have succeeded. Some apps belong to whoever reaches them first until
// they're set up (ProjectSend's /setup, Zipline's first administrator).

// AuditSetUp is the audit entry Roostack makes when a service's install
// hooks have succeeded and the public addresses it was waiting for open.
const AuditSetUp = "setup"

// setUpEvery is how often WatchSetUps asks the servers about services
// waiting for their setup.
const setUpEvery = 10 * time.Second

// SetUpDone is called by the controller's own server (node.Local) when a
// service's install hooks have succeeded: its addresses can be public.
func (m *Manager) SetUpDone(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishSetUpLocked(context.Background(), name)
}

// WatchSetUps asks the lab's servers about each service waiting for its
// setup every few seconds, until ctx ends, and opens its addresses once
// its install hooks have succeeded. The controller's own server says so
// at once (SetUpDone); this finds out for the others, and for a setup
// that was lost.
func (m *Manager) WatchSetUps(ctx context.Context) {
	for sleep.For(ctx, setUpEvery) {
		m.CheckSetUps(ctx)
	}
}

// CheckSetUps asks the server of each service waiting for its setup how
// it's going: succeeded opens its addresses; running, waiting to start,
// or failed (waiting for Try again) keeps it private. A server with no
// setup for it at all lost it (the controller restarted before handing
// it over, or it moved): Roostack will ask for it again, once.
func (m *Manager) CheckSetUps(ctx context.Context) {
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		m.Log.Error("listing services waiting for their setup", "err", err)
		return
	}
	for _, s := range services {
		name := s.Name()
		if !s.Instance.SettingUp || s.Instance.Waiting || m.setUpInFlight(name) {
			continue
		}
		n := m.node(s.Instance.Host)
		if n == nil {
			continue
		}
		st := n.InstallStatus(ctx, name)
		if st.Running || st.Pending || st.Failed != "" {
			continue
		}
		m.mu.Lock()
		switch {
		case m.setUpInFlightLocked(name):
			// Asked for again meanwhile: the answer was about the last one.
		case st.Done:
			m.finishSetUpLocked(ctx, name)
		case !m.setUpAsked[name]:
			if m.setUpAsked == nil {
				m.setUpAsked = map[string]bool{}
			}
			m.setUpAsked[name] = true
			m.Log.Warn("a service's setup was lost; asking for it again", "service", name)
			m.setUp = append(m.setUp, name)
			m.startApplyLocked("Setting up " + name + " again: its setup didn't start")
		}
		m.mu.Unlock()
	}
}

// setUpInFlight says whether a service's setup is on its way to its
// server: the next assignment, or one not yet delivered, asks for it, so
// what the server says now is about an earlier one.
func (m *Manager) setUpInFlight(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.setUpInFlightLocked(name)
}

// setUpWaiter is a server that may not have its assignment's set-ups yet
// (node.Remote).
type setUpWaiter interface {
	SetUpWaiting(service string) bool
}

func (m *Manager) setUpInFlightLocked(name string) bool {
	if slices.Contains(m.setUp, name) {
		return true
	}
	for _, r := range m.others() {
		if w, ok := r.(setUpWaiter); ok && w.SetUpWaiting(name) {
			return true
		}
	}
	return false
}

// finishSetUpLocked records that a service's install hooks have
// succeeded: its addresses take the exposure chosen for them, said in the
// audit log when that makes any public. Callers hold mu.
func (m *Manager) finishSetUpLocked(ctx context.Context, name string) {
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		m.Log.Error("finishing a service's setup", "service", name, "err", err)
		return
	}
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name })
	if i < 0 || !services[i].Instance.SettingUp {
		return
	}
	s := services[i]
	opens := afterSetup(m.labNow(), s)
	s.Instance.SettingUp = false
	if err := s.Instance.Save(state.InstanceFile(m.Lab, name)); err != nil {
		m.Log.Error("finishing a service's setup", "service", name, "err", err)
		return
	}
	delete(m.setUpAsked, name)
	if len(opens) == 0 {
		return // nothing was waiting for it
	}
	m.audit(ctx, AuditSetUp, name+" finished setting up, so "+words.And(opens)+
		map[bool]string{true: " is", false: " are"}[len(opens) == 1]+" public now")
	m.startApplyLocked(s.Title() + " finished setting up: making " + words.And(opens) + " public")
}

// afterSetup lists what a service waiting for its setup will make public
// once it's done: its addresses, and its ports as "port 2222".
func afterSetup(lab config.Lab, s state.Service) []string {
	if !lab.HasAddress() {
		return nil // no addresses yet (SPEC.md §6.6)
	}
	_, routes := generate.Values(lab, s.Package, s.Instance)
	var out []string
	for _, rn := range slices.Sorted(maps.Keys(routes)) {
		if routes[rn].AfterSetup {
			out = append(out, routes[rn].Host)
		}
	}
	ports := generate.ResolvePorts(s.Package, s.Instance)
	for _, pn := range slices.Sorted(maps.Keys(ports)) {
		if pc := ports[pn]; pc.AfterSetup && pc.Enabled {
			out = append(out, "port "+strconv.Itoa(pc.HostPort))
		}
	}
	return out
}
