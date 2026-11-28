package manager

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// EdgeServer is a server that can be the lab's edge (SPEC.md §10.5): one
// of its other servers with a public address, such as a cloud server.
type EdgeServer struct {
	ID, Name string
	// Address is its public IPv4 address, which public names point at.
	Address string
}

// EdgeServers are the servers that can be the lab's edge: any but the
// controller with a public address, from where the controller dials it
// or where it was last seen connecting from.
func (m *Manager) EdgeServers(ctx context.Context) []EdgeServer {
	infos := m.linkInfos(ctx)
	names := map[string]string{}
	for _, r := range m.others() {
		names[r.Identity().ID] = r.Identity().Name
	}
	var out []EdgeServer
	for id, info := range infos {
		if id == m.Lab.NodeID {
			continue
		}
		if a := edgeAddr(info.Peer); a != "" {
			out = append(out, EdgeServer{ID: id, Name: cmp.Or(names[id], id), Address: a})
		}
	}
	slices.SortFunc(out, func(a, b EdgeServer) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// edgeAddr is a server's address for the lab's edge: its public address
// (publicAddr). A test build (devdirect) may take others.
var edgeAddr = publicAddr

// publicAddr is a server's public IPv4 address, if it has one: where the
// controller dials it (an address, or a name it resolves to), else where
// it last connected from.
func publicAddr(p labnet.Peer) string {
	for _, ep := range []string{p.Dial, p.Seen} {
		host, _, err := net.SplitHostPort(ep)
		if err != nil {
			continue
		}
		if a, err := netip.ParseAddr(host); err == nil {
			if a = a.Unmap(); a.Is4() && public.IsPublic(a) {
				return a.String()
			}
			continue
		}
		if ips, err := net.LookupIP(host); err == nil {
			for _, ip := range ips {
				if a, ok := netip.AddrFromSlice(ip); ok && a.Unmap().Is4() && public.IsPublic(a.Unmap()) {
					return a.Unmap().String()
				}
			}
		}
	}
	return ""
}

// Edge is the lab's edge as chosen ("" for port forwarding at home), and
// its public address ("" if it has none now).
func (m *Manager) Edge(ctx context.Context) (EdgeServer, bool) {
	m.mu.Lock()
	id := m.labNow().Public.Edge
	m.mu.Unlock()
	if id == "" {
		return EdgeServer{}, false
	}
	for _, e := range m.EdgeServers(ctx) {
		if e.ID == id {
			return e, true
		}
	}
	return EdgeServer{ID: id}, true
}

// SetEdge makes server id the lab's edge, or port forwarding at home
// again with "", saves it and applies it: the edge passes the public
// names on, the controller's Caddy believes the edge alone about
// visitors, and public records point at the edge (SPEC.md §10.5).
func (m *Manager) SetEdge(ctx context.Context, id string) error {
	if m.UpdateLab == nil {
		return errors.New("manager: UpdateLab is not set")
	}
	if id != "" && !slices.ContainsFunc(m.EdgeServers(ctx), func(e EdgeServer) bool { return e.ID == id }) {
		return usererr.New("That server can't be the way in: it needs a public address (a cloud server in this lab).")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLab(func(l *config.Lab) { l.Public.Edge = id }); err != nil {
		return err
	}
	m.startApplyLocked("Changing how public visitors reach the lab")
	return nil
}

// edgeFor is what the assignments need about the edge: the edge server's
// ID, and the addresses (as /32 ranges) the controller's Caddy believes
// PROXY headers from. Empty when the lab has no edge in it.
func (m *Manager) edgeFor(infos map[string]linkInfo) (id string, addrs []string) {
	id = m.labNow().Public.Edge
	info, ok := infos[id]
	if id == "" || !ok || info.Addr == "" {
		return "", nil
	}
	return id, []string{info.Addr + "/32"}
}

// edgeForwards are the services' public ports the lab's edge sends on,
// with the same numbers (SPEC.md §10.5): at the edge, to the controller,
// and at the controller, on to the server that runs the service when
// that's another one; or, for a server serving directly over a link of
// its own with the edge, straight to it (lab.Direct). A cloud server
// serving directly takes its own at its own address.
// lab carries the lab's pauses, advisories' too: a paused service's
// ports are home only. A port the edge uses itself, or that another
// service sends on first (by name), isn't sent on, and the log says so.
func (m *Manager) edgeForwards(lab config.Lab, services []state.Service, infos map[string]linkInfo, edge string) (atEdge, atController []agent.PortForward) {
	if edge == "" {
		return nil, nil
	}
	key := func(port int, proto string) string { return strconv.Itoa(port) + "/" + proto }
	taken := map[string]string{}
	for _, s := range services {
		if s.Instance.Host != edge {
			continue
		}
		for _, pc := range generate.ResolvePorts(s.Package, s.Instance) {
			if pc.Enabled {
				taken[key(pc.HostPort, pc.Protocol)] = s.Name()
			}
		}
	}
	for _, s := range services {
		host := s.Instance.Host
		if host == edge || s.Stop != nil || s.Instance.Off != nil || generate.Paused(lab, s.Package, s.Instance) {
			continue
		}
		to := labnet.ControllerAddr.String()
		if host != m.Lab.NodeID {
			if to = infos[host].Addr; to == "" {
				continue // a server not in the lab's network yet
			}
		}
		linked := host != m.Lab.NodeID && lab.ServesDirect(host)
		if linked && directAddr(infos[host].Peer) != "" {
			continue // a cloud server serving directly, at its own address
		}
		ports := generate.ResolvePorts(s.Package, s.Instance)
		for _, name := range slices.Sorted(maps.Keys(ports)) {
			pc := ports[name]
			if !pc.Enabled || pc.Exposure != pkgspec.ExposurePublic || s.Package.Ports[name].OutsidePort != 0 {
				continue
			}
			reason := s.Name() + " " + name
			if other, ok := taken[key(pc.HostPort, pc.Protocol)]; ok {
				m.Log.Warn("a public port isn't sent on through the lab's edge: another service there has it",
					"port", key(pc.HostPort, pc.Protocol), "service", s.Name(), "other", other)
				continue
			}
			taken[key(pc.HostPort, pc.Protocol)] = s.Name()
			// A container that sees clients' own addresses gets visitors'
			// too, through tunnels (agent.Tunnel); for one on its
			// service's network, Podman's port forwarding would hide them
			// anyway, and its answers to UDP wouldn't find the way back.
			c := s.Package.Containers[s.Package.Ports[name].Container]
			keep := c != nil && c.ClientAddresses
			if linked {
				atEdge = append(atEdge, agent.PortForward{Port: pc.HostPort, Proto: pc.Protocol, To: to, Keep: keep, Reason: reason})
				continue
			}
			atEdge = append(atEdge, agent.PortForward{Port: pc.HostPort, Proto: pc.Protocol,
				To: labnet.ControllerAddr.String(), Keep: keep, Reason: reason})
			if host != m.Lab.NodeID {
				atController = append(atController, agent.PortForward{Port: pc.HostPort, Proto: pc.Protocol, To: to,
					FromEdge: true, Keep: keep, Reason: reason})
			}
		}
	}
	return atEdge, atController
}

// forwardTunnels are the tunnels each server needs (by ID, the other
// servers' addresses on Roostack's network) for the public ports that
// keep visitors' addresses: the edge to where it sends them, the
// controller to the edge and to the servers it sends them on to, and each
// of those back to where they come from (SPEC.md §10.5).
func (m *Manager) forwardTunnels(infos map[string]linkInfo, edge string, atEdge, atController []agent.PortForward) map[string][]string {
	controller := labnet.ControllerAddr.String()
	idOf := func(addr string) string {
		if addr == controller {
			return m.Lab.NodeID
		}
		for id, info := range infos {
			if info.Addr == addr {
				return id
			}
		}
		return ""
	}
	out := map[string][]string{}
	add := func(id, remote string) {
		if id != "" && remote != "" && !slices.Contains(out[id], remote) {
			out[id] = append(out[id], remote)
		}
	}
	edgeAddr := infos[edge].Addr
	for _, f := range atEdge {
		if f.Keep {
			add(edge, f.To)
			add(idOf(f.To), edgeAddr)
		}
	}
	for _, f := range atController {
		if f.Keep {
			add(m.Lab.NodeID, edgeAddr)
			add(m.Lab.NodeID, f.To)
			add(idOf(f.To), controller)
		}
	}
	return out
}

// edgeConfig is the edge's Caddy config: the lab's public names (with
// their old ones while those still work), passed on to the controller's
// Caddy on Roostack's network.
// Names a server without a public address serves itself go straight to
// its Caddy over their link (direct, SPEC.md §10.5, "Serving directly").
// The names the way in serves itself (own) aren't passed on.
func edgeConfig(lab config.Lab, services []state.Service, direct map[string][]string, own []string) *caddy.EdgeConfig {
	c := &caddy.EdgeConfig{Upstream: net.JoinHostPort(labnet.ControllerAddr.String(), strconv.Itoa(443))}
	for _, h := range publicHostsOf(lab, services, true) {
		if !slices.Contains(own, h) {
			c.Hosts = append(c.Hosts, h)
		}
	}
	for up, hosts := range direct {
		for _, h := range hosts {
			if slices.Contains(c.Hosts, h) {
				if c.Direct == nil {
					c.Direct = map[string][]string{}
				}
				c.Direct[up] = append(c.Direct[up], h)
			}
		}
	}
	return c
}

// ownNames are the public names server serves itself, serving directly.
func ownNames(lab config.Lab, services []state.Service, server string) []string {
	var out []string
	for _, s := range services {
		if s.Instance.Host != server || !lab.ServesDirect(server) || s.Stop != nil || s.Instance.Off != nil {
			continue
		}
		if proxy, err := generate.ProxySite(lab, s.Package, s.Instance, ""); err == nil {
			for _, h := range proxy.DirectHosts {
				if !slices.Contains(proxy.HomeHosts, h) {
					out = append(out, h)
				}
			}
		}
	}
	return out
}

// edgeDirect are the public names the lab's edge passes straight to the
// servers that serve them, by each server's Caddy on Roostack's network:
// those of servers serving directly without a public address of their
// own, which only do while their link with the edge is in place
// (directProblem). lab has Direct as this apply works it out.
func (m *Manager) edgeDirect(lab config.Lab, services []state.Service, infos map[string]linkInfo) map[string][]string {
	if lab.Public.Edge == "" {
		return nil
	}
	out := map[string][]string{}
	for _, s := range services {
		host := s.Instance.Host
		info, ok := infos[host]
		if !ok || info.Addr == "" || !lab.ServesDirect(host) || directAddr(info.Peer) != "" || s.Stop != nil || s.Instance.Off != nil {
			continue
		}
		proxy, err := generate.ProxySite(lab, s.Package, s.Instance, info.Addr)
		if err != nil {
			continue
		}
		up := net.JoinHostPort(info.Addr, strconv.Itoa(443))
		for _, h := range proxy.DirectHosts {
			if !slices.Contains(proxy.HomeHosts, h) {
				out[up] = append(out[up], h)
			}
		}
	}
	return out
}

// publicHostsOf lists the public names of services, sorted, with each
// one's old name too when old is set. This is where a public address
// choosing its own way in (SPEC.md §10.5, later) would pick the ones for
// the edge.
func publicHostsOf(lab config.Lab, services []state.Service, old bool) []string {
	var hosts []string
	if lab.Monitoring.StatusPage && lab.HasAddress() {
		// The public status page (SPEC.md §6.15).
		hosts = append(hosts, caddy.StatusSubdomain+"."+lab.Domain)
	}
	if lab.DomainPublic() {
		// The domain itself (SPEC.md §5.3).
		hosts = append(hosts, lab.Domain)
	}
	for _, s := range services {
		if lab.Public.Refused[s.Name()] != "" {
			// Roostack Cloud won't carry it (cloudpublic.go).
			continue
		}
		_, routes := generate.Values(lab, s.Package, s.Instance)
		if rc, ok := routes[s.Package.SSORoute()]; ok && rc.Exposure == pkgspec.ExposurePublic && generate.SSOOn(lab, s.Package, s.Instance) {
			// The sign-in page is public while a service using it is
			// (SPEC.md §5.2).
			hosts = append(hosts, lab.SignInHost())
		}
		for _, rc := range routes {
			if rc.Exposure != pkgspec.ExposurePublic {
				continue
			}
			hosts = append(hosts, rc.Host)
			if o := generate.OldHost(lab, rc.Host); old && o != "" {
				hosts = append(hosts, o)
			}
		}
	}
	slices.Sort(hosts)
	return slices.Compact(hosts)
}

// Relay is Roostack Cloud's relay, as the lab sees it: set by the
// controller from the cloud package (nil: no Cloud).
type Relay struct {
	// Set relays hosts, letting in visitors by countries, with the lab's
	// time zone for its statistics and its own domain (whose names are
	// relayed once proven; "" for a roost.ac name) (again: no change if
	// they're the same), Stop stops.
	Set  func(ctx context.Context, hosts []string, countries config.Countries, tz, domain string) error
	Stop func(ctx context.Context) error
	// Pause asks the relay to close its edges for the lab's names while
	// public access is closed completely, or open them again (SPEC.md
	// §6.16).
	Pause func(ctx context.Context, closed bool) error
	// Edges are its edges' relay addresses (for Caddy) and public IPv4
	// addresses (for DNS), when it's on.
	Edges func() (relayAddrs, public []string)
	// Tunnel is a server's own tunnel to the relay, for its assignment
	// (relayservers.go): the address Cloud gave it and the edges, or a
	// placeholder until then. Nil: servers have none.
	Tunnel func(server string) agent.WireGuard
}

// RelayOn says whether public visitors come in through Roostack Cloud's
// relay.
func (m *Manager) RelayOn() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.labNow().Public.Relay
}

// SetRelay turns Roostack Cloud's relay on or off as the lab's way in.
// Cloud is asked first, so a refusal (an account without the relay)
// changes nothing; then it's saved, and the lab applies it (the
// controller's Caddy believes the relay's edges about visitors).
func (m *Manager) SetRelay(ctx context.Context, on bool) error {
	if m.UpdateLab == nil {
		return errors.New("manager: UpdateLab is not set")
	}
	if m.Relay.Set == nil {
		return usererr.New("Roostack Cloud isn't set up on this lab.")
	}
	m.relayMu.Lock()
	defer m.relayMu.Unlock()
	if on {
		services, err := state.LoadServices(m.Lab)
		if err != nil {
			return err
		}
		m.mu.Lock()
		countries := m.labNow().Public.Countries
		m.relayGen++
		m.mu.Unlock()
		if err := m.Relay.Set(ctx, publicHostsOf(m.withStatusPage(), services, false), countries, m.Lab.Timezone, ownDomain(m.Lab)); err != nil {
			return err
		}
	} else if err := m.Relay.Stop(ctx); err != nil {
		m.Log.Warn("stopping Roostack Cloud's relay", "err", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLab(func(l *config.Lab) {
		l.Public.Relay = on
		if on {
			l.Public.Edge = ""
		}
	}); err != nil {
		return err
	}
	m.startApplyLocked("Changing how public visitors reach the lab")
	return nil
}

// RelayGone turns the relay off in the lab's settings without asking
// Roostack Cloud: the link it came with has gone (unlinked here or in
// Cloud), and the relay with it, so the lab stops asking Cloud to update
// it (SPEC.md §10.5). apply false is for Roostack's start, whose own
// apply follows.
func (m *Manager) RelayGone(apply bool) error {
	if m.UpdateLab == nil {
		return errors.New("manager: UpdateLab is not set")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.labNow().Public.Relay {
		return nil
	}
	if err := m.saveLab(func(l *config.Lab) { l.Public.Relay = false }); err != nil {
		return err
	}
	m.relayGen++ // an update on its way is dropped
	m.Log.Info("Roostack Cloud's relay is off: it went with the link to Roostack Cloud")
	if apply {
		m.startApplyLocked("Roostack Cloud's relay went with the link: changing how public visitors reach the lab")
	}
	return nil
}

// ownDomain is the lab's own domain, for the relay ("" when its address
// is a roost.ac name, whose names Cloud runs).
func ownDomain(lab config.Lab) string {
	if lab.RoostAc() {
		return ""
	}
	return lab.Domain
}

// CheckRelay asks Roostack Cloud again, now: it looks for the lab's own
// domain's proof (a "Check now").
func (m *Manager) CheckRelay(ctx context.Context) error {
	if !m.RelayOn() || m.Relay.Set == nil {
		return usererr.New("Public visitors aren't coming in through Roostack Cloud's relay.")
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	m.relayMu.Lock()
	defer m.relayMu.Unlock()
	m.mu.Lock()
	countries := m.labNow().Public.Countries
	m.relayGen++
	m.mu.Unlock()
	return m.Relay.Set(ctx, publicHostsOf(m.withStatusPage(), services, false), countries, m.Lab.Timezone, ownDomain(m.Lab))
}

// Countries is who the relay lets in by the country their address is in.
func (m *Manager) Countries() config.Countries {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.labNow().Public.Countries
}

// SetCountries sets who the relay lets in by country (SPEC.md §10.5).
// While relayed, Cloud is told first, so what it refuses isn't saved;
// otherwise it's kept for when the relay is on. Nothing here changes:
// the edges enforce it.
func (m *Manager) SetCountries(ctx context.Context, c config.Countries) error {
	if m.UpdateLab == nil {
		return errors.New("manager: UpdateLab is not set")
	}
	if len(c.Allow) > 0 && len(c.Block) > 0 {
		return usererr.New("Choose either the countries let in or those kept out, not both.")
	}
	if !c.Limited() {
		c.OpenUntil = time.Time{}
	}
	// Held until they're saved, so a background update (relayFor) sends
	// either the old countries before Cloud hears of these, or these.
	m.relayMu.Lock()
	defer m.relayMu.Unlock()
	if m.RelayOn() && m.Relay.Set != nil {
		services, err := state.LoadServices(m.Lab)
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.relayGen++
		m.mu.Unlock()
		if err := m.Relay.Set(ctx, publicHostsOf(m.withStatusPage(), services, false), c, m.Lab.Timezone, ownDomain(m.Lab)); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLab(func(l *config.Lab) { l.Public.Countries = c }); err != nil {
		return err
	}
	return nil
}

// relayFor is the relay's edges' addresses for the controller's Caddy,
// when the relay is on; it also brings the relayed names in line with
// the public ones (in the background: Cloud may be slow). Callers hold
// m.mu.
//
// The update takes its turn (relayMu) and then reads the countries and
// whether the relay is still on, so it never undoes a newer SetCountries
// or SetRelay(false); it's dropped if a newer update came first.
func (m *Manager) relayFor(services []state.Service) []string {
	if !m.labNow().Public.Relay || m.Relay.Edges == nil {
		return nil
	}
	if set := m.Relay.Set; set != nil {
		m.relayGen++
		gen, hosts, tz, domain := m.relayGen, publicHostsOf(m.withStatusPage(), services, false), m.Lab.Timezone, ownDomain(m.Lab)
		m.relayWG.Add(1)
		go func() {
			defer m.relayWG.Done()
			m.relayMu.Lock()
			defer m.relayMu.Unlock()
			m.mu.Lock()
			p := m.labNow().Public
			current, countries := gen == m.relayGen && p.Relay, p.Countries
			m.mu.Unlock()
			if !current {
				return
			}
			if err := set(context.Background(), hosts, countries, tz, domain); err != nil {
				m.Log.Error("updating Roostack Cloud's relay", "err", err)
			}
		}()
	}
	addrs, _ := m.Relay.Edges()
	var out []string
	for _, a := range addrs {
		out = append(out, a+"/32")
	}
	return out
}
