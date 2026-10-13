package labnet

import (
	"net"
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
