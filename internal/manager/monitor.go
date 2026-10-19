package manager

import (
	"context"
	"net"
	"strconv"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/state"
)

// CaddyLocal is where the controller reaches its own Caddy, which serves
// every service's address in the lab (SPEC.md §6.15): asked from this
// server, a private address is allowed in, and DNS and the router don't
// come into it.
const CaddyLocal = "127.0.0.1:443"

// MonitorChecks are the checks Roostack makes by itself (SPEC.md §6.15):
// each route of each service, through this server's Caddy, and each other
// server's lab API. A service that's stopped, starting, being set up,
// updated or restored, or whose server isn't answering, is paused.
func (m *Manager) MonitorChecks(ctx context.Context) []monitor.Check {
	var out []monitor.Check
	servers, _ := m.Servers()
	unreachable := map[string]bool{} // server names
	for _, s := range servers {
		if s.This {
			continue
		}
		if !s.Reachable {
			unreachable[s.Name] = true
		}
		if r, ok := m.remote(s.ID); ok && r.Address() != "" {
			out = append(out, monitor.Check{Key: "server:" + s.ID, Kind: monitor.KindServer, Name: s.Name,
				Address: "over the lab network", Server: s.Name, TCP: net.JoinHostPort(r.Address(), strconv.Itoa(labnet.Port)), Alert: true})
		}
	}
	if !m.Lab.HasAddress() {
		return out
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		m.Log.Error("listing services to monitor", "err", err)
		return out
	}
	states := m.ServiceStates(ctx)
	for _, s := range services {
		name := s.Name()
		gen, err := generate.Service(m.Lab, s.Package, s.Instance)
		if err != nil {
			continue
		}
		st := states[name]
		paused := m.monitorPaused(ctx, name, st, unreachable[st.Server])
		for _, rm := range gen.Monitors {
			out = append(out, monitor.Check{
				Key: "service:" + name + ":" + rm.Route, Kind: monitor.KindService, Name: s.Title(), Address: rm.Host,
				Service: name, Server: st.Server,
				URL: "https://" + rm.Host + rm.Path, Dial: CaddyLocal, Statuses: rm.Statuses,
				Paused: paused, Alert: true, Public: rm.Public,
			})
		}
	}
	return out
}

// monitorPaused says why a service isn't checked now, or "".
func (m *Manager) monitorPaused(ctx context.Context, name string, st ServiceState, serverDown bool) string {
	switch {
	case serverDown:
		return "its server isn't answering"
	case m.Updating(name) != nil:
		return "it's being updated"
	case m.Restoring(name):
		return "it's being restored"
	}
	if n := m.NodeFor(name); n != nil {
		if hs := n.InstallStatus(ctx, name); hs.Running || hs.Pending {
			return "it's being set up"
		}
	}
	switch st.Status {
	case "running", "failed":
		return ""
	case "starting":
		return "it's starting"
	case "stopped":
		return "it's stopped"
	}
	return "Roostack can't tell whether it's running"
}

// remote is another server by ID.
func (m *Manager) remote(id string) (RemoteServer, bool) {
	for _, r := range m.others() {
		if r.Identity().ID == id {
			return r, true
		}
	}
	return nil, false
}

// StatusPageOn says whether the public status page is on (SPEC.md §6.15).
func (m *Manager) StatusPageOn() bool {
	if p := m.statusPage.Load(); p != nil {
		return *p
	}
	return m.Lab.Monitoring.StatusPage
}

// SetStatusPage turns the public status page on or off: lab.yaml keeps
// it, and the apply serves it (Caddy) and makes its name public (DNS
// records, edges, Roostack Cloud's relay) or stops.
func (m *Manager) SetStatusPage(on bool) error {
	if on && !m.Lab.HasAddress() {
		return formErrorf("The status page needs your lab to have an address first.")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UpdateLab != nil {
		if err := m.UpdateLab(func(l *config.Lab) { l.Monitoring.StatusPage = on }); err != nil {
			return err
		}
	}
	m.statusPage.Store(&on)
	m.applyReason = "Turning the public status page " + map[bool]string{true: "on", false: "off"}[on]
	m.startApplyLocked()
	return nil
}

// withStatusPage is the lab's settings with the status page's switch as
// last set.
func (m *Manager) withStatusPage() config.Lab {
	lab := m.Lab
	lab.Monitoring.StatusPage = m.StatusPageOn()
	return lab
}
