package labnet

import (
	"net"
	"net/netip"
	"slices"
	"strings"
)

// HomeAddress is one of this server's private IPv4 addresses, and the
// network interface it's on.
type HomeAddress struct{ Iface, IP string }

// HomeAddresses are this server's private IPv4 addresses, on its real
// network interfaces (not WireGuard, Podman's or other virtual ones): where
// another server on the same network reaches it (join codes, direct links).
func HomeAddresses() []HomeAddress {
	ifaces, _ := net.Interfaces()
	var out []HomeAddress
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || virtualIface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsPrivate() {
				out = append(out, HomeAddress{Iface: ifc.Name, IP: ipn.IP.String()})
			}
		}
	}
	return out
}

// virtualIface reports interfaces that aren't a way onto the network:
// Roostack's WireGuard links, and container and VM bridges.
func virtualIface(name string) bool {
	for _, p := range []string{"roostack", "wg", "podman", "veth", "docker", "br-", "cni", "virbr", "tun", "tap"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// HomeNetworks are the networks this server's home addresses are on
// (HomeAddresses), such as 192.168.1.0/24: what a device on the lab's VPN
// reaches when it's allowed the home network (SPEC.md §10.11).
func HomeNetworks() []netip.Prefix {
	ifaces, _ := net.Interfaces()
	var out []netip.Prefix
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || virtualIface(ifc.Name) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.To4() == nil || !ipn.IP.IsPrivate() {
				continue
			}
			if p, err := netip.ParsePrefix(ipn.String()); err == nil && !slices.Contains(out, p.Masked()) {
				out = append(out, p.Masked())
			}
		}
	}
	return out
}
