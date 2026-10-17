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
}

// Desired returns the firewall for the lab: web traffic (Caddy decides
// what is private), SSH as the Owner chose, every enabled package port and
// what the server's roles need, each for its trust class (internal/trust).
// ok is false until the Owner has chosen SSH access, and Roostack then
// leaves the firewall alone.
func Desired(lab config.Lab, services []*generate.Output, roles Roles) (fw agent.Firewall, ok bool) {
	if lab.Firewall.SSH == "" {
		return agent.Firewall{}, false
	}
	fw = agent.DefaultFirewall()
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
func CutsOff(choice string, clients []netip.Addr) []netip.Addr {
	var cut []netip.Addr
	for _, c := range clients {
		if choice == config.SSHOff || (choice == config.SSHFromLAN && !trust.IsHome(c)) {
			cut = append(cut, c)
		}
	}
	return cut
}
