// Package firewall builds the host firewall MyLab asks mylab-agent to
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

	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/pkgspec"
)

// Desired returns the firewall for the lab: web traffic (Caddy decides
// what is private), SSH as the Owner chose, and every enabled package port.
// ok is false until the Owner has chosen SSH access, and MyLab then leaves
// the firewall alone.
func Desired(lab config.Lab, services []*generate.Output) (fw agent.Firewall, ok bool) {
	if lab.Firewall.SSH == "" {
		return agent.Firewall{}, false
	}
	fw = agent.DefaultFirewall()
	fw.SSHFrom = map[string]string{config.SSHFromLAN: agent.FromLAN, config.SSHFromAny: agent.FromAny,
		config.SSHOff: agent.FromOff}[lab.Firewall.SSH]
	for _, s := range services {
		for _, hp := range s.HostPorts {
			from := agent.FromLAN
			if hp.Exposure == pkgspec.ExposurePublic {
				from = agent.FromAny
			}
			fw.Allow = append(fw.Allow, agent.Rule{Port: hp.Port, Proto: hp.Protocol, From: from,
				Reason: s.Service + " " + hp.Name})
		}
	}
	return fw, true
}

// Files read, overridable in tests.
var (
	ProcTCP  = "/proc/net/tcp"
	ProcTCP6 = "/proc/net/tcp6"
)

// SSHClients returns the addresses connected to this machine's SSH server
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
		if choice == config.SSHOff || (choice == config.SSHFromLAN && !agent.IsLAN(c)) {
			cut = append(cut, c)
		}
	}
	return cut
}
