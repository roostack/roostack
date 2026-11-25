package manager

import (
	"context"
	"errors"
	"maps"
	"net/netip"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// Serving directly (SPEC.md §10.5, "Serving directly"): a server other
// than the controller may serve its routes with its own Caddy, so visitors
// reach it without going through the controller: its public routes when it
// has a public address of its own, and its private routes too when it's on
// the controller's home network. The controller's Caddy still passes all
// its routes on, for devices whose DNS points them at the controller (the
// lab's own DNS server, Pi-hole) and the lab's VPN devices.

// ServeChoice is how visitors reach a server's services: the mode it's set
// to (config.ServeDirect or config.ServeController), whether the Owner
// chose it or it's the default, and, set to serve directly, which of its
// routes it serves itself and why not the others, in a sentence ("" if it
// serves them all).
type ServeChoice struct {
	Mode   string
	Chosen bool
	// Public and Private: it serves its public, or private, routes itself.
	Public, Private bool
	Why             string
}

// ServeChoiceOf is server id's ServeChoice.
func (m *Manager) ServeChoiceOf(ctx context.Context, id string) ServeChoice {
	infos := m.linkInfos(ctx)
	m.mu.Lock()
	lab := m.labNow()
	m.mu.Unlock()
	c := m.serveChoice(ctx, lab, infos, id)
	if why := m.RelayNote(ctx, id); c.Public && why != "" {
		c.Why = strings.TrimSpace(why + " " + c.Why)
	}
	return c
}

func (m *Manager) serveChoice(ctx context.Context, lab config.Lab, infos map[string]linkInfo, id string) ServeChoice {
	addr := directAddr(infos[id].Peer)
	c := ServeChoice{Mode: serveMode(lab, id, addr)}
	_, c.Chosen = lab.Public.Serve[id]
	if c.Mode != config.ServeDirect {
		return c
	}
	pub := directProblem(lab, id, addr, addr == "" && lab.Public.Edge != "" && m.LinkPath(ctx, id, lab.Public.Edge) == PathDirect)
	home := homeProblem(lab, m.homeAddrOf(ctx, lab, infos, id), m.setting(ctx, LocationKey+id))
	c.Public, c.Private = pub == "", home == ""
	switch {
	case c.Public && c.Private:
	case c.Public:
		c.Why = home
	case c.Private:
		c.Why = pub
	case !lab.HasAddress():
		c.Why = pub
	default:
		c.Why = pub + " " + home
	}
	return c
}

// directAddr is a server's address for visitors serving directly: its
// public address (publicAddr). A test build (devdirect) takes any address
// the controller dials it at or saw it connect from, so dev VMs, whose
// addresses are all private, can try it.
var directAddr = publicAddr

// serveMode is how server id serves, with addr its public address: the
// Owner's choice, else config.ServeDefault.
func serveMode(lab config.Lab, id, addr string) string {
	if v, ok := lab.Public.Serve[id]; ok {
		return v
	}
	return config.ServeDefault(addr != "")
}

// directProblem says why server id, set to serve directly, can't serve its
// public routes, in a sentence, or "" if it can: a server with a public
// address of its own does, and with the lab's edge as the way in, so does
// one the edge has a link of their own to (edgeLinked), which passes its
// names straight to it. Through Roostack Cloud's relay, any server does,
// over a tunnel of its own (relayservers.go); with a roost.ac name and no
// relay, every public name goes through the controller.
func directProblem(lab config.Lab, id, addr string, edgeLinked bool) string {
	switch {
	case !lab.HasAddress():
		return "Your lab has no address yet, so there's nothing to serve."
	case lab.Public.Relay:
		return ""
	case lab.Public.Edge != "" && addr == "" && !edgeLinked:
		return "Your lab's edge can't reach it directly yet, so its public services will go through the server that runs the lab. It reaches the edge's WireGuard port, UDP 51820, which may need allowing in the edge's firewall at its provider."
	case lab.Public.Edge != "" && addr == "":
		return ""
	case throughCloud(lab):
		return "Public visitors come in through Roostack Cloud, which passes them on to the server that runs the lab, so this server's public services will go through it too."
	case addr == "":
		return "This server has no public address of its own that Roostack knows, so its public services will go through the server that runs the lab."
	}
	return ""
}

// homeProblem says why a server set to serve directly can't serve its
// private routes, in a sentence, or "" if it can: it must be on the
// controller's home network (homeAddrOf, location being where the Owner
// said it is), and its certificates come from a DNS challenge the
// controller answers through the lab's DNS provider, which Roostack must
// manage the records of (or Caddy's own authority on a test address).
func homeProblem(lab config.Lab, homeAddr, location string) string {
	switch {
	case !lab.HasAddress():
		return "Your lab has no address yet, so there's nothing to serve."
	case homeAddr == "" && lab.OnInternet():
		return "Its private services will go through the server that runs the lab, which is on the internet."
	case homeAddr == "" && location == LocationHome:
		return "Its private services will go through the server that runs the lab, as it isn't on the same home network."
	case homeAddr == "" && location == "":
		return "Its private services will go through the server that runs the lab. If it's on your home network, set Where to At home."
	case homeAddr == "":
		return "Its private services will go through the server that runs the lab, which your devices at home reach."
	case lab.TestAddress():
	case lab.RoostAc():
		return "Its private services will go through the server that runs the lab, as Roostack can't get their certificates through a roost.ac address yet."
	case !lab.DNS.ManageRecords:
		return "Its private services will go through the server that runs the lab, as Roostack doesn't manage your domain's DNS records, which their certificates need."
	}
	return ""
}

// homeAddrs are the controller's own home-network addresses (tests
// replace it).
var homeAddrs = func() []string {
	var out []string
	for _, a := range labnet.HomeAddresses() {
		out = append(out, a.IP)
	}
	return out
}

// homeAddrOf is server id's address on the controller's home network: one
// it reported, on the same /24 as one of the controller's own (where the
// controller is at home), that the controller hasn't got itself (VMs
// behind one NAT all have the same one), with the Owner saying it's At
// home; "" for none. A test build (devdirect) takes the first it reported.
func (m *Manager) homeAddrOf(ctx context.Context, lab config.Lab, infos map[string]linkInfo, id string) string {
	if lab.OnInternet() || m.setting(ctx, LocationKey+id) != LocationHome {
		return ""
	}
	own := homeAddrs()
	if lab.DNS.LANIP != "" {
		own = append(own, lab.DNS.LANIP)
	}
	return homeAddrIn(infos[id].Home, own)
}

// homeAddrIn is the first of addrs on the same /24 as one of own, and not
// one of own itself.
var homeAddrIn = func(addrs, own []string) string {
	for _, a := range addrs {
		ap, err := netip.ParseAddr(a)
		if err != nil || !ap.Is4() || slices.Contains(own, a) {
			continue
		}
		net, _ := ap.Prefix(24)
		for _, o := range own {
			if op, err := netip.ParseAddr(o); err == nil && net.Contains(op) {
				return a
			}
		}
	}
	return ""
}

// directServers are the servers that serve their public routes directly
// now, and those that serve their private routes at home too, each with
// its address there, sorted.
func (m *Manager) directServers(ctx context.Context, infos map[string]linkInfo) (public []string, home map[string]string) {
	lab := m.labNow()
	home = map[string]string{}
	for id := range infos {
		if id == m.Lab.NodeID {
			continue
		}
		c := m.serveChoice(ctx, lab, infos, id)
		if c.Public {
			public = append(public, id)
		}
		if c.Private {
			home[id] = m.homeAddrOf(ctx, lab, infos, id)
		}
	}
	slices.Sort(public)
	return public, home
}

// Direct are the servers serving their public routes directly, and
// DirectHome those serving their private routes at home, each with its
// address there, as the last assignment worked them out.
func (m *Manager) Direct() []string {
	if d := m.direct.Load(); d != nil {
		return *d
	}
	return nil
}

func (m *Manager) DirectHome() map[string]string {
	if d := m.directHome.Load(); d != nil {
		return *d
	}
	return nil
}

// labDirect is the manager's lab with Direct and DirectHome as the last
// assignment worked them out, for what generates a service's files here.
func (m *Manager) labDirect() config.Lab {
	lab := m.Lab
	lab.Direct = m.Direct()
	lab.DirectHome = slices.Sorted(maps.Keys(m.DirectHome()))
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

// DirectAddrs are the names that servers serving directly serve
// themselves, for their DNS records (dnsrecords.Manager.Direct): their
// services' public routes and the names of their public ports, which their
// own firewall takes from anywhere, at the server's public address, and
// their private routes at home at its home address.
func (m *Manager) DirectAddrs() map[string]string {
	m.mu.Lock()
	lab := m.labNow()
	m.mu.Unlock()
	all, _ := m.directNames(lab)
	return all
}

// homeNames are the names servers serve themselves on the home network,
// each with the server's home address, which the lab's own DNS server
// answers them with (labDNSConfig); lab is the lab's settings now.
func (m *Manager) homeNames(lab config.Lab) map[string]string {
	_, home := m.directNames(lab)
	return home
}

// directNames are DirectAddrs, and of them those served at home, for lab
// (the lab's settings now: callers hold m.mu or have read it).
func (m *Manager) directNames(lab config.Lab) (all, atHome map[string]string) {
	direct, home := m.Direct(), m.DirectHome()
	if len(direct) == 0 && len(home) == 0 {
		return nil, nil
	}
	infos := m.linkInfos(context.Background())
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		m.Log.Error("listing the addresses servers serve directly", "err", err)
		return nil, nil
	}
	lab.Direct, lab.DirectHome = direct, slices.Sorted(maps.Keys(home))
	all, atHome = map[string]string{}, map[string]string{}
	for _, s := range services {
		host := s.Instance.Host
		addr := directAddr(infos[host].Peer)
		if !lab.ServesDirect(host) && !lab.ServesHome(host) || s.Stop != nil || s.Instance.Off != nil {
			continue
		}
		if proxy, err := generate.ProxySite(lab, s.Package, s.Instance, infos[host].Addr); err == nil {
			for _, h := range proxy.DirectHosts {
				switch {
				case slices.Contains(proxy.HomeHosts, h):
					all[h], atHome[h] = home[host], home[host]
				case addr != "" && !lab.Public.Relay:
					// Through the relay, public names point at its edges.
					all[h] = addr
				}
			}
		}
		if addr != "" && lab.ServesDirect(host) && generate.PublicPorts(lab, s.Package, s.Instance) {
			if h := generate.PortHost(lab, s.Package, s.Instance); h != "" {
				all[h] = addr
			}
		}
	}
	return all, atHome
}

// WalledOn are the walled addresses server serves itself while it serves
// directly, each with its service's name: the only ones its Roostack may
// ask the sign-in page about for its Caddy's walls (SPEC.md §5.2).
func (m *Manager) WalledOn(server string) map[string]string {
	if _, home := m.DirectHome()[server]; server == "" || server == m.Lab.NodeID || !slices.Contains(m.Direct(), server) && !home {
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

// HomeHostsOn are the names of server's private routes that it serves
// itself at home: the only ones it may have DNS challenges set for, for
// its Caddy's certificates.
func (m *Manager) HomeHostsOn(server string) []string {
	if _, home := m.DirectHome()[server]; server == "" || server == m.Lab.NodeID || !home {
		return nil
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		return nil
	}
	lab := m.labDirect()
	var out []string
	for _, s := range services {
		if s.Instance.Host != server || s.Stop != nil || s.Instance.Off != nil {
			continue
		}
		if proxy, err := generate.ProxySite(lab, s.Package, s.Instance, ""); err == nil {
			out = append(out, proxy.HomeHosts...)
		}
	}
	return out
}

// edgeLinked are the servers the lab's edge has links of their own to, or
// should: set to serve directly, without a public address of their own, so
// that the edge passes their public names straight to them (SPEC.md
// §10.5, "Serving directly"). None without an edge, or through Roostack
// Cloud.
func (m *Manager) edgeLinked(ctx context.Context) []string {
	lab := m.labNow()
	edge := lab.Public.Edge
	if edge == "" || throughCloud(lab) || !lab.HasAddress() {
		return nil
	}
	var out []string
	for id, info := range m.linkInfos(ctx) {
		addr := directAddr(info.Peer)
		if id != edge && id != m.Lab.NodeID && addr == "" && serveMode(lab, id, addr) == config.ServeDirect {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}
