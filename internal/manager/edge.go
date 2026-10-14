package manager

import (
	"cmp"
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/state"
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
		if a := publicAddr(info.Peer); a != "" {
			out = append(out, EdgeServer{ID: id, Name: cmp.Or(names[id], id), Address: a})
		}
	}
	slices.SortFunc(out, func(a, b EdgeServer) int { return strings.Compare(a.Name, b.Name) })
	return out
}

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
	id := m.publicLocked().Edge
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
		return formErrorf("That server can't be the way in: it needs a public address (a cloud server in this lab).")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.UpdateLab(func(l *config.Lab) { l.Public.Edge = id }); err != nil {
		return err
	}
	m.publicLocked().Edge = id
	m.applyReason = "Changing how public visitors reach the lab"
	m.startApplyLocked()
	return nil
}

// edgeFor is what the assignments need about the edge: the edge server's
// ID, and the addresses (as /32 ranges) the controller's Caddy believes
// PROXY headers from. Empty when the lab has no edge in it.
func (m *Manager) edgeFor(infos map[string]linkInfo) (id string, addrs []string) {
	id = m.publicLocked().Edge
	info, ok := infos[id]
	if id == "" || !ok || info.Addr == "" {
		return "", nil
	}
	return id, []string{info.Addr + "/32"}
}

// edgeConfig is the edge's Caddy config: the lab's public names (with
// their old ones while those still work), passed on to the controller's
// Caddy on Roostack's network.
func edgeConfig(lab config.Lab, services []state.Service) *caddy.EdgeConfig {
	return &caddy.EdgeConfig{Hosts: publicHostsOf(lab, services, true),
		Upstream: net.JoinHostPort(labnet.ControllerAddr.String(), strconv.Itoa(443))}
}

// publicHostsOf lists the public names of services, sorted, with each
// one's old name too when old is set. This is where a public address
// choosing its own way in (SPEC.md §10.5, later) would pick the ones for
// the edge.
func publicHostsOf(lab config.Lab, services []state.Service, old bool) []string {
	var hosts []string
	for _, s := range services {
		_, routes := generate.Values(lab, s.Package, s.Instance)
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
	// Edges are its edges' relay addresses (for Caddy) and public IPv4
	// addresses (for DNS), when it's on.
	Edges func() (relayAddrs, public []string)
}

// RelayOn says whether public visitors come in through Roostack Cloud's
// relay.
func (m *Manager) RelayOn() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.publicLocked().Relay
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
		return formErrorf("Roostack Cloud isn't set up on this lab.")
	}
	m.relayMu.Lock()
	defer m.relayMu.Unlock()
	if on {
		services, err := state.LoadServices(m.Lab)
		if err != nil {
			return err
		}
		m.mu.Lock()
		countries := m.publicLocked().Countries
		m.relayGen++
		m.mu.Unlock()
		if err := m.Relay.Set(ctx, publicHostsOf(m.Lab, services, false), countries, m.Lab.Timezone, ownDomain(m.Lab)); err != nil {
			return err
		}
	} else if err := m.Relay.Stop(ctx); err != nil {
		m.Log.Warn("stopping Roostack Cloud's relay", "err", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.UpdateLab(func(l *config.Lab) {
		l.Public.Relay = on
		if on {
			l.Public.Edge = ""
		}
	}); err != nil {
		return err
	}
	p := m.publicLocked()
	p.Relay = on
	if on {
		p.Edge = ""
	}
	m.applyReason = "Changing how public visitors reach the lab"
	m.startApplyLocked()
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
		return formErrorf("Public visitors don't come in through Roostack Cloud's relay.")
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	m.relayMu.Lock()
	defer m.relayMu.Unlock()
	m.mu.Lock()
	countries := m.publicLocked().Countries
	m.relayGen++
	m.mu.Unlock()
	return m.Relay.Set(ctx, publicHostsOf(m.Lab, services, false), countries, m.Lab.Timezone, ownDomain(m.Lab))
}

// Countries is who the relay lets in by the country their address is in.
func (m *Manager) Countries() config.Countries {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.publicLocked().Countries
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
		return formErrorf("Choose either the countries let in or those kept out, not both.")
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
		if err := m.Relay.Set(ctx, publicHostsOf(m.Lab, services, false), c, m.Lab.Timezone, ownDomain(m.Lab)); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.UpdateLab(func(l *config.Lab) { l.Public.Countries = c }); err != nil {
		return err
	}
	m.publicLocked().Countries = c
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
	if !m.publicLocked().Relay || m.Relay.Edges == nil {
		return nil
	}
	if set := m.Relay.Set; set != nil {
		m.relayGen++
		gen, hosts, tz, domain := m.relayGen, publicHostsOf(m.Lab, services, false), m.Lab.Timezone, ownDomain(m.Lab)
		m.relayWG.Add(1)
		go func() {
			defer m.relayWG.Done()
			m.relayMu.Lock()
			defer m.relayMu.Unlock()
			m.mu.Lock()
			p := m.publicLocked()
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
