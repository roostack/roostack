// Package trust says who a connection to a Roostack server comes from, its
// trust class, and what each class may reach (SPEC.md §6.10, §10.2). It is
// the one definition the firewall (internal/firewall builds it,
// roostack-agent renders it as nftables) and Caddy's private check both
// read, so they can't disagree; tests in those packages check their output
// against Classify and May.
//
// The firewall settles Roostack's own WireGuard interfaces first, by
// interface and address, since WireGuard guarantees which peer an address
// on them is; everything else is told apart by address: the home network
// or anyone. Caddy only sees addresses, so its private list is HomeRanges.
//
// The lab's servers are a class of their own, not home (SPEC.md §10.2,
// review M23): a cheap VPS in the lab reaches what's opened for servers
// (the lab API, services' web ports for the controller's Caddy, backup
// stores, the edge listener) and SSH from the controller, but not the UI,
// private routes, home-only ports or SSH from any other server.
package trust

import (
	"net/netip"
	"slices"
	"strings"
)

// Roostack's WireGuard interfaces (made by roostack-agent with wg-quick).
const (
	// LabInterface is the network between a lab's servers (SPEC.md §10.2).
	LabInterface = "roostack"
	// GuestsInterface carries other labs that store backups here (§10.10).
	GuestsInterface = "roostack-guests"
	// RelayInterface is Roostack Cloud's relay, dialled by the lab (§10.5).
	RelayInterface = "roostack-relay"
	// StoreLinks are this lab's links to other labs' storage,
	// roostack-st1…9; nftables matches the name's prefix with "*".
	StoreLinks = "roostack-st*"
)

// Address ranges.
var (
	// CGNATRange is the shared address space (RFC 6598) Tailscale uses,
	// and Roostack's own WireGuard networks are inside.
	CGNATRange = netip.MustParsePrefix("100.64.0.0/10")
	// OverlayRange is Roostack's network, on LabInterface: ServersRange
	// and DevicesRange.
	OverlayRange = netip.MustParsePrefix("100.96.0.0/16")
	// ServersRange holds the lab's servers' addresses (the hub gives them
	// out from 100.96.0.0/24). Never home.
	ServersRange = netip.MustParsePrefix("100.96.0.0/17")
	// DevicesRange is kept for people's own devices (a laptop or phone)
	// joining Roostack's network, a later feature (SPEC.md §10.2): home,
	// so they'll reach what a device at home does. Nothing has an address
	// in it yet.
	DevicesRange = netip.MustParsePrefix("100.96.128.0/17")
	// GuestRange holds the addresses of links between labs (storage for
	// others). Each storing lab picks its own /24 in it at random.
	GuestRange = netip.MustParsePrefix("100.100.0.0/14")
	// RelayRange is Roostack Cloud's relay network (its edges and the labs
	// it relays), given by Cloud.
	RelayRange = netip.MustParsePrefix("100.81.0.0/16")
)

// HomeRanges are the "home network" (and VPN) sources: the private ranges,
// and the CGNAT range (Tailscale, people's devices on Roostack's network)
// except other labs (GuestRange), Roostack Cloud's relay (RelayRange) and
// the lab's servers (ServersRange), which are never home (SPEC.md §6.10).
// The firewall's set and Caddy's private list are both made from it
// (docs/ASSUMPTIONS.md C5).
var HomeRanges = slices.Concat(
	[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16")},
	Without(CGNATRange, GuestRange, RelayRange, ServersRange),
	[]netip.Prefix{netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10")},
)

// EdgePort is Caddy's listener for visitors through the lab's edges and
// Roostack Cloud's relay, whose sites count everyone as outside: a PROXY
// header can't claim a home address there (SPEC.md §10.5, review H4). The
// firewall sends their connections to 443 here.
const EdgePort = 8474

// Class is who a connection comes from. The ones a firewall rule can name
// (Rule.For in roostack-agent) are Anyone, Home, Servers and Guests; the
// others are fixed: the relay and edges reach EdgePort, a server still
// joining reaches the lab API, store links reach nothing.
type Class string

// Classes.
const (
	// Anyone is anywhere, the internet included.
	Anyone Class = "any"
	// Home is the home network or a VPN (HomeRanges), and people's devices
	// on Roostack's network (DevicesRange).
	Home Class = "lan"
	// Servers are the lab's servers, on LabInterface (ServersRange).
	Servers Class = "servers"
	// Controller is the server that runs the lab, on LabInterface: a
	// server that also reaches SSH, so the Owner can go on from it to a
	// cloud server whose SSH is for the home network only.
	Controller Class = "controller"
	// Guests are other labs storing backups here, on GuestsInterface.
	Guests Class = "guests"
	// Joining is a server joining the lab with a join code's one-time key,
	// on the hub (SPEC.md §10.2).
	Joining Class = "joining"
	// Edge is one of the lab's edges, on LabInterface (SPEC.md §10.5).
	Edge Class = "edge"
	// Relay is Roostack Cloud's relay, on RelayInterface.
	Relay Class = "relay"
	// StoreLink is the other end of this lab's link to another lab's
	// storage.
	StoreLink Class = "store-link"
	// Nobody is a source no class covers: dropped.
	Nobody Class = ""
)

// Ruled reports whether a firewall rule may name c.
func (c Class) Ruled() bool {
	return c == Anyone || c == Home || c == Servers || c == Guests
}

// Lab is what Classify needs to know about the lab's servers.
type Lab struct {
	// Controller is the controller's address on Roostack's network, seen
	// from another server; invalid on the controller itself.
	Controller netip.Addr
	// Members are the lab's servers' addresses, on the hub; nil elsewhere
	// (where only the lab's servers are peers).
	Members []netip.Addr
	// Edges are the lab's edges' addresses on Roostack's network.
	Edges []netip.Addr
}

// Classify says who a connection arriving on iface from addr is.
func Classify(iface string, addr netip.Addr, lab Lab) Class {
	addr = addr.Unmap()
	switch {
	case iface == RelayInterface:
		return Relay
	case iface == GuestsInterface:
		return Guests
	case strings.HasPrefix(iface, strings.TrimSuffix(StoreLinks, "*")):
		return StoreLink
	case iface == LabInterface:
		switch {
		case DevicesRange.Contains(addr):
			return Home
		case !ServersRange.Contains(addr):
			return Nobody
		case slices.Contains(lab.Edges, addr):
			return Edge
		case lab.Members != nil && !slices.Contains(lab.Members, addr):
			return Joining
		case addr == lab.Controller:
			return Controller
		}
		return Servers
	case IsHome(addr):
		return Home
	}
	return Anyone
}

// May reports whether a connection from class c gets through a firewall
// rule for class rule: its own class, and the home network everything
// open to anyone. An edge and the controller are servers too. Servers
// reach only what's opened for servers, not even what's open to anyone:
// over Roostack's network nothing else is theirs (SSH from the controller
// is a rule of its own).
func May(c, rule Class) bool {
	switch {
	case c == rule:
		return true
	case c == Home && rule == Anyone:
		return true
	case (c == Edge || c == Controller) && rule == Servers:
		return true
	}
	return false
}

// IsHome reports whether addr is on the home network (or a VPN), as the
// firewall and Caddy see it.
func IsHome(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range HomeRanges {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// Without returns the ranges covering p except holes, as few as it can.
func Without(p netip.Prefix, holes ...netip.Prefix) []netip.Prefix {
	p = p.Masked()
	split := false
	for _, h := range holes {
		switch {
		case h.Bits() <= p.Bits() && h.Contains(p.Addr()):
			return nil // the hole covers p
		case h.Bits() > p.Bits() && p.Contains(h.Addr()):
			split = true
		}
	}
	if !split {
		return []netip.Prefix{p}
	}
	// Halve p and look at each half.
	lo := netip.PrefixFrom(p.Addr(), p.Bits()+1)
	hi := netip.PrefixFrom(lastAddr(lo).Next(), p.Bits()+1)
	return append(Without(lo, holes...), Without(hi, holes...)...)
}

// lastAddr is p's last address.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}
