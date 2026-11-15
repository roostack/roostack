package manager

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/trust"
	"roostack.cloud/roostack/internal/usererr"
)

// The lab's VPN (SPEC.md §10.11): people's own devices join the lab's
// network from anywhere, each with its own address, and reach only what
// its person may use. lab.yaml has the Owner's settings; the store has
// the devices. Each apply works out what each device reaches (Caddy and
// the firewalls enforce it) and gives the server devices connect to its
// interface.

// PeopleStore is the lab's VPN devices, and who may use each service
// (store.Store).
type PeopleStore interface {
	VPNDevices(ctx context.Context) ([]store.VPNDevice, error)
	CanUse(ctx context.Context, service string, userID int64) (bool, error)
}

// VPNServerAddress is the VPN's server's address on its interface, with
// the devices' range.
const VPNServerAddress = "100.96.128.1/17"

// VPN is the Owner's settings for the lab's VPN.
func (m *Manager) VPN() config.VPN { return m.labNow().VPN }

// SetVPN saves the Owner's settings for the lab's VPN, and applies them:
// the interface on the server devices connect to, and what they reach.
func (m *Manager) SetVPN(v config.VPN) error {
	if v.MaxDevices < 0 {
		return usererr.New("The limit can't be below nothing.")
	}
	if v.Server == m.Lab.NodeID {
		v.Server = ""
	}
	// Only turning it on needs a server devices can reach: turning it off
	// works whatever has changed since (a cloud server that lost its
	// public address, a controller now on the internet).
	if v.On && v.Server != "" {
		if m.Lab.OnInternet() {
			return usererr.New("Your lab's controller is on the internet, so devices connect to it.")
		}
		if !slices.ContainsFunc(m.EdgeServers(context.Background()), func(e EdgeServer) bool { return e.ID == v.Server }) {
			return usererr.New("Devices can connect only to a server the internet can reach: a cloud server in this lab.")
		}
	}
	m.mu.Lock()
	before := m.VPN()
	if err := m.saveLab(func(l *config.Lab) { l.VPN = v }); err != nil {
		m.mu.Unlock()
		return err
	}
	m.startApplyLocked(map[bool]string{true: "Changing the lab's VPN", false: "Turning the lab's VPN off"}[v.On])
	m.mu.Unlock()
	if (before.On && before.Server != "") != (v.On && v.Server != "") || before.Server != v.Server {
		// The controller's link to the server devices connect to carries
		// their range (labnet.Hub.VPNServer).
		go m.syncNetwork()
	}
	return nil
}

// syncNetwork updates the controller's side of Roostack's network, trying
// again every few seconds for a while: moving the VPN off the controller,
// the devices' range can't go to the other server's link until the apply
// has stopped the controller's own VPN interface.
func (m *Manager) syncNetwork() {
	if m.SyncNetwork == nil {
		return
	}
	var err error
	for range syncNetworkTries {
		if err = m.SyncNetwork(context.Background()); err == nil {
			return
		}
		time.Sleep(syncNetworkWait)
	}
	m.Log.Error("updating Roostack's network for the lab's VPN", "err", err)
}

// How often, and how long, syncNetwork tries.
var (
	syncNetworkTries = 40
	syncNetworkWait  = 3 * time.Second
)

// VPNElsewhere is the server devices connect to when it isn't the
// controller, for Roostack's network (labnet.Hub.VPNServer); "" otherwise.
func (m *Manager) VPNElsewhere() string {
	if s := m.VPNServer(); m.VPN().On && s != m.Lab.NodeID {
		return s
	}
	return ""
}

// VPNChanged applies a change to the VPN's devices (one added, changed,
// or removed), or to who may use what, which changes what they reach.
func (m *Manager) VPNChanged(reason string) {
	if !m.VPN().On {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startApplyLocked(reason)
}

// VPNServer is the server devices connect to: the Owner's choice while
// it's in the lab, otherwise the controller.
func (m *Manager) VPNServer() string {
	v := m.VPN()
	if v.Server != "" && slices.ContainsFunc(m.others(), func(r RemoteServer) bool { return r.Identity().ID == v.Server }) {
		return v.Server
	}
	return m.Lab.NodeID
}

// vpnDevices works out what each device reaches (config.Devices) and the
// VPN's interface with them as peers, for an apply; none while the VPN is
// off.
func (m *Manager) vpnDevices(ctx context.Context, services []state.Service) (config.Devices, *agent.WireGuard) {
	v := m.VPN()
	if !v.On || m.People == nil {
		return config.Devices{}, nil
	}
	d, wg := m.vpnReach(ctx, v, services)
	if m.VPNElsewhere() != "" {
		// Devices connect to another server, which passes them on to the
		// controller over Roostack's network (SPEC.md §10.11): the lab's
		// address, and the home's networks for those allowed them. Those
		// allowed SSH reach the lab's servers by a rule of their own
		// (agent.People.Servers), so the servers aren't passed on to.
		d.Elsewhere = true
		d.PassOn = m.vpnPassOn(d)
	}
	return d, wg
}

// vpnPassOn are where the server devices connect to sends their traffic
// on to the controller: the lab's address, and the home's networks while
// any device may reach them.
func (m *Manager) vpnPassOn(d config.Devices) []string {
	var out []string
	if a := m.vpnHomeAddress(); a != "" {
		out = append(out, a+"/32")
	}
	if len(d.Home) > 0 {
		for _, p := range labnet.HomeNetworks() {
			out = append(out, p.String())
		}
	}
	return out
}

// vpnHomeAddress is the controller's home address, where the lab's names
// point at home (its own in lab.yaml, or its first).
func (m *Manager) vpnHomeAddress() string {
	if m.Lab.DNS.LANIP != "" {
		return m.Lab.DNS.LANIP
	}
	if a := labnet.HomeAddresses(); len(a) > 0 {
		return a[0].IP
	}
	return ""
}

// vpnToController is what a server's link to the controller carries for
// the lab's VPN (agent.Links.ToController): on the server devices connect
// to, where they go on to (Devices.PassOn, apart from anything on that
// server's own networks); on the others, the devices' range, so that answers to devices
// reaching them go back through the controller.
func (m *Manager) vpnToController(d config.Devices, id string, own []string) []string {
	if len(d.All) == 0 && !d.Elsewhere {
		return nil
	}
	if id != m.VPNElsewhere() {
		return []string{trust.DevicesRange.String()}
	}
	var out []string
	for _, s := range d.PassOn {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			continue
		}
		if slices.ContainsFunc(own, func(a string) bool { ip, err := netip.ParseAddr(a); return err == nil && p.Contains(ip) }) {
			m.Log.Warn("the lab's VPN: not passing this on to the controller, as it's the other server's own network too", "network", s)
			continue
		}
		out = append(out, s)
	}
	return out
}

// vpnReach is vpnDevices' devices and interface.
func (m *Manager) vpnReach(ctx context.Context, v config.VPN, services []state.Service) (config.Devices, *agent.WireGuard) {
	wg := &agent.WireGuard{Interface: agent.PeopleInterface, Address: VPNServerAddress, ListenPort: agent.PeoplePort,
		Forward: v.HomeNetwork || v.Internet || m.VPNElsewhere() != ""}
	list, err := m.People.VPNDevices(ctx)
	if err != nil {
		m.Log.Error("listing the lab's VPN devices", "err", err)
		return config.Devices{}, wg
	}
	var d config.Devices
	canUse := map[[2]any]bool{}
	for _, dev := range list {
		wg.Peers = append(wg.Peers, agent.Peer{PublicKey: dev.PublicKey, AllowedIPs: []string{dev.Address + "/32"}})
		d.All = append(d.All, dev.Address)
		// What only some may tick: Roostack's pages for Owners and Admins,
		// SSH for Owners, kept so even if their role changes.
		if dev.Pages && (dev.Role == auth.RoleOwner || dev.Role == auth.RoleAdmin) {
			d.Pages = append(d.Pages, dev.Address)
		}
		if dev.SSH && dev.Role == auth.RoleOwner {
			d.SSH = append(d.SSH, dev.Address)
		}
		// Either or both, while the Owner allows each.
		if dev.Internet && v.Internet {
			d.Internet = append(d.Internet, dev.Address)
		}
		if dev.Home && v.HomeNetwork {
			d.Home = append(d.Home, dev.Address)
		}
		for _, s := range services {
			// Who may use a service is chosen only with single sign-on
			// (the maintainer, 2026-11-11): one without it is every
			// device's, as it's everyone's at home.
			key := [2]any{s.Name(), dev.UserID}
			ok, seen := canUse[key]
			if !seen && !generate.SSOOn(m.Lab, s.Package, s.Instance) {
				ok, seen = true, true
				canUse[key] = true
			}
			if !seen {
				ok, err = m.People.CanUse(ctx, s.Name(), dev.UserID)
				if err != nil {
					m.Log.Error("who may use a service, for the lab's VPN", "service", s.Name(), "err", err)
				}
				canUse[key] = ok
			}
			if ok {
				if d.Services == nil {
					d.Services = map[string][]string{}
				}
				d.Services[s.Name()] = append(d.Services[s.Name()], dev.Address)
			}
		}
	}
	return d, wg
}

// VPNWay is how devices reach the lab's VPN, for their settings and its
// pages.
type VPNWay struct {
	// Server is the server devices connect to, by name; Endpoint where
	// (host:port), and PublicKey its key there ("" until it's set up).
	Server, Endpoint, PublicKey string
	// AtHome: Endpoint is the home's address, which needs a port forward.
	AtHome bool
	// Servers are the servers devices may connect to, for the Owner's
	// choice: the controller first, then those the internet can reach.
	Servers []EdgeServer
	// Chosen is the one they connect to, by ID.
	Chosen string
	// LastConnected is when each device last connected, by public key.
	LastConnected map[string]time.Time
	// HomeNetworks are the home's networks, for devices allowed them.
	HomeNetworks []string
	// HomeAddress is this server's home address (the one set in lab.yaml,
	// or its first), where the lab's names point at home before the DNS
	// records manager has said.
	HomeAddress string
}

// VPNWay says how devices reach the lab's VPN now.
func (m *Manager) VPNWay(ctx context.Context) VPNWay {
	w := VPNWay{Server: m.Node.Identity().Name, AtHome: !m.Lab.OnInternet(),
		Endpoint: fmt.Sprintf("%s.%s:%d", caddy.LinkSubdomain, m.Lab.Domain, agent.PeoplePort)}
	for _, p := range labnet.HomeNetworks() {
		w.HomeNetworks = append(w.HomeNetworks, p.String())
	}
	if !m.Lab.OnInternet() {
		w.HomeAddress = m.vpnHomeAddress()
		w.Servers = append(w.Servers, EdgeServer{ID: m.Lab.NodeID, Name: m.Node.Identity().Name})
		w.Servers = append(w.Servers, m.EdgeServers(ctx)...)
	}
	w.Chosen = m.VPNServer()
	var srv any = m.Node
	if id := m.VPNElsewhere(); id != "" {
		// A cloud server in the lab, at its public address.
		w.AtHome, w.Endpoint = false, ""
		for _, r := range m.others() {
			if r.Identity().ID == id {
				srv, w.Server = r, r.Identity().Name
			}
		}
		for _, e := range m.EdgeServers(ctx) {
			if e.ID == id {
				w.Endpoint = fmt.Sprintf("%s:%d", e.Address, agent.PeoplePort)
			}
		}
	}
	l, ok := srv.(interface {
		VPNStatus(context.Context) (agent.WireGuardStatus, error)
	})
	if !ok || w.Endpoint == "" {
		return w
	}
	st, err := l.VPNStatus(ctx)
	if err != nil || !st.Configured {
		return w
	}
	w.PublicKey = st.PublicKey
	w.LastConnected = map[string]time.Time{}
	for key, t := range st.Handshakes {
		if t > 0 {
			w.LastConnected[key] = time.Unix(t, 0)
		}
	}
	return w
}
