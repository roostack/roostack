package manager

import (
	"context"
	"fmt"
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLab(func(l *config.Lab) { l.VPN = v }); err != nil {
		return err
	}
	m.startApplyLocked(map[bool]string{true: "Changing the lab's VPN", false: "Turning the lab's VPN off"}[v.On])
	return nil
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
	wg := &agent.WireGuard{Interface: agent.PeopleInterface, Address: VPNServerAddress, ListenPort: agent.PeoplePort,
		Forward: v.HomeNetwork || v.Everything}
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
		switch {
		case dev.Reach == store.ReachEverything && v.Everything:
			d.Everything = append(d.Everything, dev.Address)
		case dev.Reach == store.ReachHome && v.HomeNetwork:
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
	w.HomeAddress = m.Lab.DNS.LANIP
	if a := labnet.HomeAddresses(); w.HomeAddress == "" && len(a) > 0 && !m.Lab.OnInternet() {
		w.HomeAddress = a[0].IP
	}
	l, ok := m.Node.(interface {
		VPNStatus(context.Context) (agent.WireGuardStatus, error)
	})
	if !ok {
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
