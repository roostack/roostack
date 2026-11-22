package manager

import (
	"context"
	"errors"
	"slices"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// Serving directly (SPEC.md §10.5, "Serving directly"): a server other
// than the controller may serve its public routes with its own Caddy, so
// visitors reach it without going through the controller. Its private
// routes still go through the controller, which home devices reach.

// ServeChoice is how visitors reach a server's public services: the mode
// it's set to (config.ServeDirect or config.ServeController), whether the
// Owner chose it or it's the default, and why it can't serve directly
// now, in a sentence ("" if it can, or isn't set to).
type ServeChoice struct {
	Mode   string
	Chosen bool
	// Why says why a server set to serve directly doesn't now.
	Why string
}

// ServeChoiceOf is server id's ServeChoice.
func (m *Manager) ServeChoiceOf(ctx context.Context, id string) ServeChoice {
	infos := m.linkInfos(ctx)
	m.mu.Lock()
	lab := m.labNow()
	m.mu.Unlock()
	addr := directAddr(infos[id].Peer)
	c := ServeChoice{Mode: config.ServeDefault(addr != "")}
	if v, ok := lab.Public.Serve[id]; ok {
		c.Mode, c.Chosen = v, true
	}
	if c.Mode == config.ServeDirect {
		c.Why = directProblem(lab, addr)
	}
	return c
}

// directAddr is a server's address for visitors serving directly: its
// public address (publicAddr). A test build (devdirect) takes any address
// the controller dials it at or saw it connect from, so dev VMs, whose
// addresses are all private, can try it.
var directAddr = publicAddr

// directProblem says why a server set to serve directly can't, in a
// sentence, or "" if it can. Only a server with a public address of its
// own serves directly for now; with an edge or Roostack Cloud as the way
// in, every public name goes through the controller.
func directProblem(lab config.Lab, addr string) string {
	switch {
	case !lab.HasAddress():
		return "Your lab has no address yet, so there's nothing to serve."
	case lab.Public.Edge != "":
		return "Public visitors come in through your lab's edge, which passes them on to the server that runs the lab, so this server's public services will go through it too."
	case throughCloud(lab):
		return "Public visitors come in through Roostack Cloud, which passes them on to the server that runs the lab, so this server's public services will go through it too."
	case addr == "":
		return "This server has no public address of its own that Roostack knows, so visitors can't reach it directly. Its public services will go through the server that runs the lab."
	}
	return ""
}

// directServers are the servers that serve their public routes directly
// now, in the order of infos' IDs.
func (m *Manager) directServers(infos map[string]linkInfo) []string {
	lab := m.labNow()
	var out []string
	for id, info := range infos {
		if id == m.Lab.NodeID {
			continue
		}
		addr := directAddr(info.Peer)
		mode := config.ServeDefault(addr != "")
		if v, ok := lab.Public.Serve[id]; ok {
			mode = v
		}
		if mode == config.ServeDirect && directProblem(lab, addr) == "" {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// Direct are the servers serving their public routes directly, as the
// last assignment worked them out.
func (m *Manager) Direct() []string {
	if d := m.direct.Load(); d != nil {
		return *d
	}
	return nil
}

// labDirect is the manager's lab with Direct as the last assignment
// worked it out, for what generates a service's files here.
func (m *Manager) labDirect() config.Lab {
	lab := m.Lab
	lab.Direct = m.Direct()
	return lab
}

// SetServe sets how visitors reach server id's public services, saves it
// and applies it: its own Caddy, or the controller's (SPEC.md §10.5).
func (m *Manager) SetServe(ctx context.Context, id, mode string) error {
	if m.UpdateLab == nil {
		return errors.New("manager: UpdateLab is not set")
	}
	if mode != config.ServeDirect && mode != config.ServeController {
		return usererr.New("Choose how visitors reach this server's public services.")
	}
	if id == m.Lab.NodeID || !slices.ContainsFunc(m.others(), func(r RemoteServer) bool { return r.Identity().ID == id }) {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLab(func(l *config.Lab) {
		if l.Public.Serve == nil {
			l.Public.Serve = map[string]string{}
		}
		l.Public.Serve[id] = mode
	}); err != nil {
		return err
	}
	if mode == config.ServeDirect {
		m.startApplyLocked("Letting a server serve its public services itself")
	} else {
		m.startApplyLocked("Passing a server's public services through the server that runs the lab")
	}
	return nil
}

// DirectAddrs are the public addresses that servers serving directly
// serve themselves, each with that server's public address, for their DNS
// records (dnsrecords.Manager.Direct): their services' public routes, and
// the names of their public ports, which their own firewall takes from
// anywhere.
func (m *Manager) DirectAddrs() map[string]string {
	direct := m.Direct()
	if len(direct) == 0 {
		return nil
	}
	infos := m.linkInfos(context.Background())
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		m.Log.Error("listing the addresses servers serve directly", "err", err)
		return nil
	}
	m.mu.Lock()
	lab := m.labNow()
	m.mu.Unlock()
	lab.Direct = direct
	out := map[string]string{}
	for _, s := range services {
		host := s.Instance.Host
		addr := directAddr(infos[host].Peer)
		if !lab.ServesDirect(host) || addr == "" || s.Stop != nil || s.Instance.Off != nil {
			continue
		}
		if proxy, err := generate.ProxySite(lab, s.Package, s.Instance, infos[host].Addr); err == nil {
			for _, h := range proxy.DirectHosts {
				out[h] = addr
			}
		}
		if generate.PublicPorts(lab, s.Package, s.Instance) {
			if h := generate.PortHost(lab, s.Package, s.Instance); h != "" {
				out[h] = addr
			}
		}
	}
	return out
}

// WalledOn are the walled addresses server serves itself while it serves
// directly, each with its service's name: the only ones its Roostack may
// ask the sign-in page about for its Caddy's walls (SPEC.md §5.2).
func (m *Manager) WalledOn(server string) map[string]string {
	if server == "" || server == m.Lab.NodeID || !slices.Contains(m.Direct(), server) {
		return nil
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		return nil
	}
	lab := m.labDirect()
	out := map[string]string{}
	for _, s := range services {
		p, in := s.Package, s.Instance
		if in.Host != server || !generate.SSOOn(lab, p, in) || p.SSO.ForwardAuth == nil {
			continue
		}
		proxy, err := generate.ProxySite(lab, p, in, "")
		if err != nil {
			continue
		}
		_, routes := generate.Values(lab, p, in)
		if h := routes[p.SSO.ForwardAuth.Route].Host; slices.Contains(proxy.DirectHosts, h) {
			out[h] = in.ID()
		}
	}
	return out
}
