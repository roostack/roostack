// Package firewall builds the host firewall Roostack asks roostack-agent to
// apply (SPEC.md §6.10), and reads who is connected over SSH so the Owner
// isn't cut off by accident.
package firewall

import (
	"bufio"
	"encoding/hex"
	"errors"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/trust"
)

// Roles are what a server does beyond its services that the firewall
// opens a way in for (node.Local works them out from its assignment).
type Roles struct {
	// Hub: Members are the lab's servers' addresses on Roostack's network,
	// and anyone else there is still joining and reaches only the lab API
	// (SPEC.md §10.2).
	Hub     bool
	Members []string
	// Store: the server keeps a backup store for the lab's other servers.
	Store bool
	// ProxyPorts are where the controller passes its servers' backups on
	// to other labs' storage.
	ProxyPorts []int
	// Guests, when GuestAddr is set, are the other labs storing backups
	// here (SPEC.md §10.10).
	GuestAddr string
	Guests    []generate.GuestStorage
	// LinksListen: other servers dial this one directly.
	LinksListen bool
	// SetupPort is the first-run setup pages' port while setup lasts, for
	// the home network only (SPEC.md §6.3.2); 0 otherwise.
	SetupPort int
}

// Desired returns the firewall for the lab: web traffic (Caddy decides
// what is private), SSH as the Owner chose, every enabled package port and
// what the server's roles need, each for its trust class (internal/trust).
// Until the Owner has chosen SSH access, Roostack leaves the firewall
// alone, apart from keeping the setup pages to the home network (an
// Unchosen firewall); ok is false when there's nothing to apply: no
// choice and no setup pages, on a server that isn't the controller.
func Desired(lab config.Lab, services []*generate.Output, roles Roles) (fw agent.Firewall, ok bool) {
	if lab.Firewall.SSH == "" {
		// The controller's table goes once setup is done, since an
		// Unchosen firewall with no setup port deletes it.
		if lab.IsNode() {
			return agent.Firewall{}, false
		}
		return agent.Firewall{Unchosen: true, SetupPort: roles.SetupPort}, true
	}
	fw = agent.DefaultFirewall()
	fw.SetupPort = roles.SetupPort
	defer func() { fw = WithClosed(fw, lab.Public.Pause.Closed != nil) }()
	fw.SSHFrom = map[string]string{config.SSHFromLAN: agent.FromLAN, config.SSHFromAny: agent.FromAny,
		config.SSHOff: agent.FromOff}[lab.Firewall.SSH]
	for _, s := range services {
		for _, hp := range s.HostPorts {
			to := trust.Home
			if hp.Exposure == pkgspec.ExposurePublic {
				to = trust.Anyone
			}
			fw.Allow = append(fw.Allow, agent.Rule{Port: hp.Port, Proto: hp.Protocol, For: to,
				Reason: s.Service + " " + hp.Name})
		}
	}
	// Roostack's network between servers (SPEC.md §10.2).
	if lab.OverlayAddr != "" {
		fw.Allow = append(fw.Allow, agent.Rule{Port: labnet.Port, Proto: "tcp", For: trust.Servers,
			Reason: "roostack servers"})
		if !lab.IsNode() {
			// The controller is the hub other servers dial.
			fw.Allow = append(fw.Allow, agent.Rule{Port: agent.WireGuardPort, Proto: "udp", For: trust.Anyone,
				Reason: "roostack wireguard"})
			// Servers back up to each other's disks through the hub when
			// they can't link directly (SPEC.md §10.10): only the store.
			fw.Forward = append(fw.Forward, agent.Rule{Port: generate.StoragePort, Proto: "tcp", For: trust.Servers,
				Reason: "backups from one server to another's disk"})
			// A server still joining (a join code's one-time key) reaches
			// only the lab API.
			if roles.Hub {
				fw.JoinPort, fw.Members = labnet.Port, roles.Members
			}
		}
	}
	// The lab's edges reach Caddy on its edge listener only (SPEC.md
	// §10.5); Roostack Cloud's relay edges arrive on their own interface,
	// which the agent always handles so.
	for _, e := range lab.EdgeAddrs {
		if p, err := netip.ParsePrefix(e); err == nil && p.IsSingleIP() && agent.OverlayRange.Contains(p.Addr()) {
			fw.Edges = append(fw.Edges, p.Addr().String())
		}
	}
	// Over Roostack's network, only the controller reaches SSH (SPEC.md
	// §10.2): the Owner can go on from it to a cloud server whose SSH is
	// for the home network only.
	if c := Controller(lab); c.IsValid() {
		fw.Controller = c.String()
	}
	// A node's web services are reached by the controller's Caddy.
	if lab.IsNode() {
		for _, s := range services {
			for _, p := range s.Ports {
				fw.Allow = append(fw.Allow, agent.Rule{Port: p, Proto: "tcp", For: trust.Servers,
					Reason: s.Service + " web (via the controller)"})
			}
		}
	}
	if roles.Store {
		for _, port := range []int{generate.StoragePort, generate.StorageAdminPort} {
			fw.Allow = append(fw.Allow, agent.Rule{Port: port, Proto: "tcp", For: trust.Servers, Reason: "backup store"})
		}
	}
	for _, port := range roles.ProxyPorts {
		fw.Allow = append(fw.Allow, agent.Rule{Port: port, Proto: "tcp", For: trust.Servers, Reason: "backups to another lab"})
	}
	// Other labs reach only the guest API and each its own store (SPEC.md
	// §6.10); the agent drops everything else from them.
	if roles.GuestAddr != "" {
		fw.Allow = append(fw.Allow,
			agent.Rule{Port: agent.GuestsPort, Proto: "udp", For: trust.Anyone, Reason: "storage for other labs (wireguard)"},
			agent.Rule{Port: labnet.GuestAPIPort, Proto: "tcp", For: trust.Guests, Reason: "storage for other labs"})
		for _, g := range roles.Guests {
			if g.Addr == "" {
				continue // from an older controller: no way to tell it from other guests
			}
			for _, port := range []int{g.Port, g.Port + 1} {
				fw.Allow = append(fw.Allow, agent.Rule{Port: port, Proto: "tcp", For: trust.Guests,
					Source: g.Addr, Reason: "storage for " + g.User})
			}
		}
	}
	// Other servers dial this one directly for backups to its disk.
	if roles.LinksListen {
		fw.Allow = append(fw.Allow, agent.Rule{Port: agent.WireGuardPort, Proto: "udp", For: trust.Anyone,
			Reason: "roostack direct links between servers"})
	}
	return fw, true
}

// WithClosed is fw with public access closed completely, or not (SPEC.md
// §6.16): Caddy's web ports for the home network only, and everyone else
// refused (agent.Firewall.Closed). A server applies it on its own as soon
// as a closing arrives, to the firewall it last applied, rather than wait
// for the apply's other changes (node.Local).
func WithClosed(fw agent.Firewall, closed bool) agent.Firewall {
	if fw.Unchosen {
		return fw
	}
	fw.Closed = closed
	fw.Allow = slices.Clone(fw.Allow)
	for i, r := range fw.Allow {
		if web := r.Port == 80 && r.Proto == "tcp" || r.Port == 443; web && (r.For == trust.Anyone || r.For == trust.Home) {
			fw.Allow[i].For = map[bool]trust.Class{true: trust.Home, false: trust.Anyone}[closed]
		}
	}
	return fw
}

// Controller is the controller's address on Roostack's network, seen from
// another server; invalid on the controller, or a server in no lab.
func Controller(lab config.Lab) netip.Addr {
	if lab.OverlayAddr == "" || !lab.IsNode() {
		return netip.Addr{}
	}
	return labnet.ControllerAddr
}

// Files read, overridable in tests.
var (
	ProcTCP  = "/proc/net/tcp"
	ProcTCP6 = "/proc/net/tcp6"
)

// SSHClients returns the addresses connected to this server's SSH server
// (port 22) right now, from the kernel's TCP table.
func SSHClients() ([]netip.Addr, error) {
	var addrs []netip.Addr
	for _, f := range []string{ProcTCP, ProcTCP6} {
		a, err := sshClients(f)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		addrs = append(addrs, a...)
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int { return a.Compare(b) })
	return slices.Compact(addrs), nil
}

const (
	sshPort     = 22
	established = "01"
)

func sshClients(path string) ([]netip.Addr, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var addrs []netip.Addr
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		// sl local_address rem_address st ...
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 || fields[3] != established {
			continue
		}
		_, localPort, err := parseAddr(fields[1])
		if err != nil || localPort != sshPort {
			continue
		}
		remote, _, err := parseAddr(fields[2])
		if err != nil {
			continue
		}
		addrs = append(addrs, remote.Unmap())
	}
	return addrs, sc.Err()
}

// parseAddr decodes /proc/net/tcp's "0100007F:0016": the address in
// host byte order, 32-bit words at a time, then the port in hex.
func parseAddr(s string) (netip.Addr, int, error) {
	hexAddr, hexPort, ok := strings.Cut(s, ":")
	if !ok {
		return netip.Addr{}, 0, errors.New("bad address")
	}
	port, err := strconv.ParseUint(hexPort, 16, 16)
	if err != nil {
		return netip.Addr{}, 0, err
	}
	raw, err := hex.DecodeString(hexAddr)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.Addr{}, 0, errors.New("bad address")
	}
	// Each 32-bit word is little-endian (x86 and arm64).
	for i := 0; i < len(raw); i += 4 {
		raw[i], raw[i+1], raw[i+2], raw[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	addr, _ := netip.AddrFromSlice(raw)
	return addr, int(port), nil
}

// CutsOff reports which of the connected SSH clients a choice would block.
// Of the lab's servers, only the controller reaches SSH, whatever the
// choice but "Nobody" (Controller).
func CutsOff(choice string, clients []netip.Addr, controller netip.Addr) []netip.Addr {
	var cut []netip.Addr
	for _, c := range clients {
		c = c.Unmap()
		switch {
		case choice == config.SSHOff,
			trust.ServersRange.Contains(c) && c != controller,
			choice == config.SSHFromLAN && !trust.IsHome(c) && !trust.ServersRange.Contains(c):
			cut = append(cut, c)
		}
	}
	return cut
}
