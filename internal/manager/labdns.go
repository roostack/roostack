package manager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labdns"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
)

// Roostack's own DNS server (SPEC.md §6.5.3): one server at home answers
// the lab's names with the home address of the server that serves each,
// and passes everything else on. It runs as roostack-dns on that server
// while the names file is there (labdns), and each apply writes the file
// there and deletes it elsewhere (node.Local.applyLabDNS).

// ownResolver is r with Roostack's own DNS server in use: on the
// controller while it's at home, else on a server at home that the Owner
// chose (by default the first by name).
func (m *Manager) ownResolver(r Resolver, services []state.Service, infos map[string]linkInfo) Resolver {
	r.Own = true
	if !m.Lab.OnInternet() {
		r.OwnServer, r.OwnServerName, r.Addr = m.Lab.NodeID, m.Node.Identity().Name, m.vpnHomeAddress()
		r.Addrs6 = labnet.HomeAddresses6()
	} else {
		r.Homes = m.dnsHomes(infos)
		if len(r.Homes) == 0 {
			r.Why = "none of the lab's servers is on a home network"
			return r
		}
		h := r.Homes[0]
		if i := slices.IndexFunc(r.Homes, func(h DNSHome) bool { return h.ID == r.Choice.Server }); i >= 0 {
			h = r.Homes[i]
		}
		r.OwnServer, r.OwnServerName, r.Addr = h.ID, h.Name, h.Addr
	}
	// A DNS service on that server already takes the home network's
	// port 53.
	for _, s := range services {
		host := cmp.Or(s.Instance.Host, m.Lab.NodeID)
		if s.Package.DNSServer == "" || host != r.OwnServer || s.Stop != nil || s.Instance.Off != nil {
			continue
		}
		if generate.ResolvePorts(s.Package, s.Instance)[s.Package.DNSServer].Enabled {
			r.Why = fmt.Sprintf("%s already answers DNS on %s", cmp.Or(s.Instance.Title, s.Package.Title), r.OwnServerName)
			r.OwnServer, r.Addr = "", ""
			return r
		}
	}
	if r.Addr == "" {
		r.Why = "its server has no home address"
		return r
	}
	r.ServerAddr = net.JoinHostPort(r.Addr, strconv.Itoa(labdns.Port))
	return r
}

// dnsHomes are the lab's servers at home (a home address of their own,
// as they reported it), by name.
func (m *Manager) dnsHomes(infos map[string]linkInfo) []DNSHome {
	var out []DNSHome
	for _, o := range m.others() {
		id := o.Identity()
		if home := infos[id.ID].Home; len(home) > 0 {
			out = append(out, DNSHome{ID: id.ID, Name: id.Name, Addr: home[0]})
		}
	}
	slices.SortFunc(out, func(a, b DNSHome) int { return cmp.Or(strings.Compare(a.Name, b.Name), strings.Compare(a.ID, b.ID)) })
	return out
}

// labDNSConfig is what Roostack's own DNS server answers: each of the
// lab's names with the controller's home address while it's at home. A
// home node serving names directly will answer its own (direct serving's
// phase 2); with the controller in the cloud, the public records already
// point at it, so every name is passed on.
func labDNSConfig(lab config.Lab, services []state.Service, homeAddr, fallback string) *labdns.Config {
	c := &labdns.Config{Domain: lab.Domain, Fallback: fallback}
	if homeAddr == "" || lab.OnInternet() {
		return c
	}
	c.CheckAddr = homeAddr
	c.Names = map[string]string{}
	for _, n := range labDNSNames(lab, services) {
		c.Names[n] = homeAddr
	}
	return c
}

// labDNSNames are the names the lab serves, as Caddy's sites have them:
// Roostack's pages, the sign-in page, the domain itself while it's
// served, the status page, every service's addresses and its ports'
// name, and each of those at the old address while it works (SPEC.md
// §6.6). Names it leaves out are passed on, and the public wildcard
// still points them home.
func labDNSNames(lab config.Lab, services []state.Service) []string {
	if !lab.HasAddress() {
		return nil
	}
	names := []string{lab.UIHost(), lab.SignInHost()}
	if lab.ServesDomain() {
		names = append(names, lab.Domain)
	}
	if lab.Monitoring.StatusPage {
		names = append(names, caddy.StatusSubdomain+"."+lab.Domain)
	}
	for _, g := range lab.GlancesSites {
		names = append(names, g.Host)
	}
	for _, s := range services {
		_, routes := generate.Values(lab, s.Package, s.Instance)
		for _, rc := range routes {
			names = append(names, rc.Host)
		}
		if h := generate.PortHost(lab, s.Package, s.Instance); h != "" {
			names = append(names, h)
		}
	}
	for _, n := range slices.Clone(names) {
		if o := generate.OldHost(lab, n); o != "" {
			names = append(names, o)
		}
	}
	names = slices.DeleteFunc(names, func(n string) bool { return n == "" })
	slices.Sort(names)
	return slices.Compact(names)
}

// labDNS is the server running Roostack's own DNS server, nil while
// none does.
func (m *Manager) labDNS(ctx context.Context) (node.LabDNS, Resolver) {
	r := m.Resolver(ctx)
	if !r.Own || r.OwnServer == "" {
		return nil, r
	}
	if r.OwnServer == m.Lab.NodeID {
		return node.Capability(m.Node, node.Node.LabDNS), r
	}
	for _, o := range m.others() {
		if o.Identity().ID == r.OwnServer {
			return node.Capability[node.LabDNS](o, node.Node.LabDNS), r
		}
	}
	return nil, r
}

// ErrNoLabDNS: Roostack's own DNS server isn't the lab's.
var ErrNoLabDNS = errors.New("Roostack's own DNS server isn't in use")

// LabDNSStatus is how Roostack's own DNS server is doing, for Settings.
func (m *Manager) LabDNSStatus(ctx context.Context) (labdns.Status, error) {
	d, _ := m.labDNS(ctx)
	if d == nil {
		return labdns.Status{}, ErrNoLabDNS
	}
	return d.LabDNSStatus(ctx)
}

// LabDNSCheck says whether Roostack's own DNS server was asked for the
// check's name with token in the last minute: whether the device that
// looked it up uses the lab's DNS (Your account's check).
func (m *Manager) LabDNSCheck(ctx context.Context, token string) (bool, error) {
	d, _ := m.labDNS(ctx)
	if d == nil {
		return false, ErrNoLabDNS
	}
	return d.LabDNSSeen(ctx, labdns.CheckName(token, m.Lab.Domain))
}

// LabDNSInUse says whether Roostack's own DNS server is the lab's, and
// running somewhere (the check is shown only then).
func (m *Manager) LabDNSInUse(ctx context.Context) bool {
	d, _ := m.labDNS(ctx)
	return d != nil
}
