package agent

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/trust"
)

// Firewall is the desired inbound firewall (SPEC.md §6.10). Everything not
// listed is dropped.
type Firewall struct {
	// SSHFrom is "lan" (private ranges only), "any" or "off" (no SSH).
	SSHFrom string `json:"ssh_from"`
	Allow   []Rule `json:"allow"`
	// Forward are the ports one Roostack server may reach on another
	// through this one (the hub), over Roostack's network (TCP only).
	// Nothing else is forwarded to, from or within that network.
	Forward []Rule `json:"forward,omitempty"`
	// Edges are the lab's edges' addresses on Roostack's network (SPEC.md
	// §10.5). Their connections to 443 go to Caddy's edge listener
	// (EdgePort), as do all from Roostack Cloud's relay.
	Edges []string `json:"edges,omitempty"`
	// JoinPort, on the hub, is the lab API's port: the only one reached
	// on Roostack's network by anyone but Members, the lab's servers (a
	// server still joining holds a join code's one-time key for up to an
	// hour; SPEC.md §10.2). Nothing from them is forwarded either.
	JoinPort int      `json:"join_port,omitempty"`
	Members  []string `json:"members,omitempty"`
	// Controller, on another server, is the controller's address on
	// Roostack's network: the one server that reaches SSH over it, unless
	// SSHFrom is off (SPEC.md §10.2). The lab's servers reach nothing else
	// that isn't opened for them (internal/trust).
	Controller string `json:"controller,omitempty"`
	// Closed: public access is closed completely (SPEC.md §6.16). Caddy's
	// web ports take connections from the home network and VPN only, and
	// everyone else's, the lab's edges' and Roostack Cloud's relay's
	// included, are refused at once (a reset) rather than left to time
	// out, so nothing from the internet reaches Caddy.
	Closed bool `json:"closed,omitempty"`
	// SetupPort, while first-run setup lasts, is the setup pages' HTTPS
	// port, open to the home network only (SPEC.md §6.3.2). 0 once setup
	// is done.
	SetupPort int `json:"setup_port,omitempty"`
	// Unchosen: the Owner hasn't chosen the firewall yet (first-run
	// setup), so Roostack's table only keeps SetupPort to the home network
	// and leaves everything else as if it weren't there; without a
	// SetupPort there's no table at all. Nothing else may be set.
	Unchosen bool `json:"unchosen,omitempty"`
	// People is the lab's VPN on this server (SPEC.md §10.11); nil: none.
	People *People `json:"people,omitempty"`
	// PortForwards are services' public ports this server sends on to
	// another of the lab's servers over Roostack's network (SPEC.md
	// §10.5): on the lab's edge, from the internet to the controller; on
	// the controller, from the edge on to the server at home that runs
	// the service. They leave with this server's address there, so the
	// answers come back through it.
	PortForwards []PortForward `json:"port_forwards,omitempty"`
	// ForwardOwned are the interfaces the agent turns forwarding on for,
	// for the lab's VPN or PortForwards, where it was off
	// (vpnForwardingPlan): only answers are forwarded from them, and
	// connections sent on (PortForwards, or other software's: a
	// container's published port). The agent works them out itself; a
	// request can't set them.
	ForwardOwned []string `json:"-"`
}

// People is what the lab's VPN's devices (trust.DevicesRange) reach on
// this server, besides what's open to anyone (Caddy, which decides for
// each device): the rules for their own addresses. They're never home.
type People struct {
	// Port is where devices connect (UDP), on the server they connect to;
	// 0 elsewhere.
	Port int `json:"port,omitempty"`
	// Rules are each opened for some devices: SSH, and a service's
	// home-only port for those whose person may use the service.
	Rules []DeviceRule `json:"rules,omitempty"`
	// Home are devices whose traffic may go on to the home network, and
	// Internet those whose traffic may go on to the internet (public
	// addresses only), leaving from this server's address: on the server
	// they connect to, or Home on the controller when they connect to
	// another (Forwarded). A device may be in both.
	Home     []string `json:"home,omitempty"`
	Internet []string `json:"internet,omitempty"`
	// PassOn, on the server devices connect to when it isn't the
	// controller, are where their traffic goes on to over Roostack's
	// network, keeping their addresses: the lab's address and the home's
	// networks (agent.Links.ToController). Never the lab's servers: only
	// Servers' devices reach those, and only their SSH.
	PassOn []string `json:"pass_on,omitempty"`
	// Forwarded, on the controller, means devices connect to another
	// server and arrive over Roostack's network: Home's traffic goes on
	// from there, and Servers (devices allowed SSH) reach the other
	// servers' SSH through it.
	Forwarded bool     `json:"forwarded,omitempty"`
	Servers   []string `json:"servers,omitempty"`
}

// ForwardsTraffic reports whether devices' traffic goes on from this
// server to the home network or the internet, for which the default
// route's interface must forward too (setVPNForwarding).
func (p *People) ForwardsTraffic() bool { return p != nil && len(p.Home)+len(p.Internet) > 0 }

// DeviceRule opens one port to some of the VPN's devices.
type DeviceRule struct {
	Port    int      `json:"port"`
	Proto   string   `json:"proto"`
	Devices []string `json:"devices"`
	Reason  string   `json:"reason"`
	// From is a well-known port sent on to Port for these devices too,
	// as Rule.From does for the home network: Pi-hole's 53.
	From int `json:"from,omitempty"`
}

// PortForward sends one public port on to another of the lab's servers
// (Firewall.PortForwards), keeping its number.
type PortForward struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"`
	// To is the server's address on Roostack's network.
	To string `json:"to"`
	// FromEdge: from the lab's edges (Firewall.Edges), on Roostack's
	// network; otherwise from the internet, on any other interface.
	FromEdge bool   `json:"from_edge,omitempty"`
	Reason   string `json:"reason"`
}

// forwardsFromOutside reports whether connections from the internet are
// sent on from here, for which the default route's interface must
// forward (setVPNForwarding).
func (f Firewall) forwardsFromOutside() bool {
	return slices.ContainsFunc(f.PortForwards, func(p PortForward) bool { return !p.FromEdge })
}

// EdgePort is Caddy's listener for visitors through the lab's edges, whose
// sites count everyone as outside (caddy.EdgePort, the same number): a
// PROXY header can't claim a home address there (review H4).
const EdgePort = trust.EdgePort

// Rule allows one inbound port to one trust class (internal/trust).
type Rule struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"` // tcp or udp
	// For is who reaches it: trust.Anyone, trust.Home, trust.Servers
	// (the lab's servers, on Roostack's network), trust.Controller,
	// trust.Edge (the lab's edges, Firewall.Edges, sending on a public
	// port) or trust.Guests (other labs storing backups here).
	For trust.Class `json:"for"`
	// Source limits a guests' rule to one address (a guest lab reaches
	// only its own store, SPEC.md §10.10).
	Source string `json:"source,omitempty"`
	// Reason is shown in the generated file, e.g. "caddy https".
	Reason string `json:"reason"`
	// From is a well-known port (one of OutsidePorts) sent on to Port, for
	// the same people: DNS's 53 for a service's container, which can't
	// listen below 1024 (SPEC.md §8.1, "Well-known ports"). 0: none.
	From int `json:"from,omitempty"`
}

// OutsidePorts are the ports a rule may send on to a service's port
// (Rule.From): kept to this list here, the privilege boundary, whatever
// roostack asks for (pkgspec.OutsidePorts).
var OutsidePorts = []int{53}

// ownPorts are Roostack's own ports from 1024, which a port forward never
// takes.
var ownPorts = []int{EdgePort, WireGuardPort, PeoplePort, GuestsPort, 8471, 8472} // 8471-2: labnet.Port, labnet.GuestAPIPort

// SSHFrom values.
const (
	FromLAN = string(trust.Home)
	FromAny = string(trust.Anyone)
	FromOff = "off"
)

func lanElements(v6 bool) string {
	var out []string
	for _, p := range trust.HomeRanges {
		if p.Addr().Is6() == v6 {
			out = append(out, p.String())
		}
	}
	return strings.Join(out, ", ")
}

// DefaultFirewall is the baseline: web traffic from anywhere (Caddy decides
// what is private), SSH from the LAN only.
func DefaultFirewall() Firewall {
	return Firewall{SSHFrom: FromLAN, Allow: []Rule{
		{Port: 80, Proto: "tcp", For: trust.Anyone, Reason: "caddy http (redirects to https)"},
		{Port: 443, Proto: "tcp", For: trust.Anyone, Reason: "caddy https"},
		{Port: 443, Proto: "udp", For: trust.Anyone, Reason: "caddy http/3"},
	}}
}

// Validate checks the firewall request. The agent never trusts its input.
func (f Firewall) Validate() error {
	var errs []error
	if f.SetupPort < 0 || f.SetupPort > 65535 {
		errs = append(errs, fmt.Errorf("setup_port: invalid port %d", f.SetupPort))
	}
	if f.Unchosen {
		if f.SSHFrom != "" || len(f.Allow) > 0 || len(f.Forward) > 0 || len(f.Edges) > 0 || f.JoinPort != 0 ||
			len(f.Members) > 0 || f.Controller != "" || f.People != nil {
			errs = append(errs, errors.New("unchosen: only setup_port may be set"))
		}
		return errors.Join(errs...)
	}
	if f.SSHFrom != FromLAN && f.SSHFrom != FromAny && f.SSHFrom != FromOff {
		errs = append(errs, fmt.Errorf("ssh_from must be %q, %q or %q", FromLAN, FromAny, FromOff))
	}
	for i, r := range f.Allow {
		if r.Port < 1 || r.Port > 65535 {
			errs = append(errs, fmt.Errorf("allow[%d]: invalid port %d", i, r.Port))
		}
		if r.Proto != "tcp" && r.Proto != "udp" {
			errs = append(errs, fmt.Errorf("allow[%d]: proto must be tcp or udp", i))
		}
		if !r.For.Ruled() {
			errs = append(errs, fmt.Errorf("allow[%d]: for must be %q, %q, %q, %q, %q or %q", i, trust.Anyone, trust.Home,
				trust.Servers, trust.Guests, trust.Controller, trust.Edge))
		}
		if r.For == trust.Edge && len(f.Edges) == 0 {
			errs = append(errs, fmt.Errorf("allow[%d]: for the edges, but there are none", i))
		}
		if r.Source != "" {
			if a, err := netip.ParseAddr(r.Source); err != nil || r.For != trust.Guests || !trust.GuestRange.Contains(a) {
				errs = append(errs, fmt.Errorf("allow[%d]: source must be a guest lab's address", i))
			}
		}
		if strings.ContainsFunc(r.Reason, func(c rune) bool { return c < ' ' || c == '"' || c == '\\' }) {
			errs = append(errs, fmt.Errorf("allow[%d]: invalid characters in reason", i))
		}
		if r.From != 0 && (!slices.Contains(OutsidePorts, r.From) || r.Port < 1024 || (r.For != trust.Home && r.For != trust.Anyone)) {
			errs = append(errs, fmt.Errorf("allow[%d]: from must be one of %v, sent to a port from 1024 for anyone or the home network", i, OutsidePorts))
		}
	}
	for i, e := range f.Edges {
		if a, err := netip.ParseAddr(e); err != nil || !trust.ServersRange.Contains(a) {
			errs = append(errs, fmt.Errorf("edges[%d]: not a server's address on Roostack's network", i))
		}
	}
	if a, err := netip.ParseAddr(f.Controller); f.Controller != "" && (err != nil || !trust.ServersRange.Contains(a)) {
		errs = append(errs, errors.New("controller: not a server's address on Roostack's network"))
	}
	if f.JoinPort < 0 || f.JoinPort > 65535 || (f.JoinPort == 0 && len(f.Members) > 0) {
		errs = append(errs, errors.New("join_port: invalid port, or members without one"))
	}
	for i, m := range f.Members {
		if a, err := netip.ParseAddr(m); err != nil || !trust.ServersRange.Contains(a) {
			errs = append(errs, fmt.Errorf("members[%d]: not a server's address on Roostack's network", i))
		}
	}
	if p := f.People; p != nil {
		errs = append(errs, p.validate()...)
	}
	for i, p := range f.PortForwards {
		// A service's port (pkgspec: from 1024), never one of Roostack's
		// own.
		if p.Port < 1024 || p.Port > 65535 || slices.Contains(ownPorts, p.Port) || p.Proto != "tcp" && p.Proto != "udp" {
			errs = append(errs, fmt.Errorf("port_forwards[%d]: a service's port from 1024, tcp or udp", i))
		}
		if a, err := netip.ParseAddr(p.To); err != nil || !trust.ServersRange.Contains(a) {
			errs = append(errs, fmt.Errorf("port_forwards[%d]: to must be a server's address on Roostack's network", i))
		}
		if p.FromEdge && len(f.Edges) == 0 {
			errs = append(errs, fmt.Errorf("port_forwards[%d]: from the edges, but there are none", i))
		}
		if strings.ContainsFunc(p.Reason, func(c rune) bool { return c < ' ' || c == '"' || c == '\\' }) {
			errs = append(errs, fmt.Errorf("port_forwards[%d]: invalid characters in reason", i))
		}
	}
	for i, r := range f.Forward {
		if r.Port < 1 || r.Port > 65535 || r.Proto != "tcp" || r.For != trust.Servers || r.Source != "" {
			errs = append(errs, fmt.Errorf("forward[%d]: only a TCP port between Roostack's servers (for %q)", i, trust.Servers))
		}
		if strings.ContainsFunc(r.Reason, func(c rune) bool { return c < ' ' || c == '"' || c == '\\' }) {
			errs = append(errs, fmt.Errorf("forward[%d]: invalid characters in reason", i))
		}
	}
	return errors.Join(errs...)
}

// validate checks the VPN's rules: every address is a device's.
func (p People) validate() []error {
	var errs []error
	devices := func(what string, list []string) {
		for _, d := range list {
			if a, err := netip.ParseAddr(d); err != nil || !trust.DevicesRange.Contains(a) {
				errs = append(errs, fmt.Errorf("people: %s: %q isn't a device's address", what, d))
			}
		}
	}
	if p.Port < 0 || p.Port > 65535 {
		errs = append(errs, fmt.Errorf("people: invalid port %d", p.Port))
	}
	for i, r := range p.Rules {
		if r.Port < 1 || r.Port > 65535 || r.Proto != "tcp" && r.Proto != "udp" || len(r.Devices) == 0 {
			errs = append(errs, fmt.Errorf("people: rules[%d]: a port, tcp or udp, and devices", i))
		}
		if strings.ContainsFunc(r.Reason, func(c rune) bool { return c < ' ' || c == '"' || c == '\\' }) {
			errs = append(errs, fmt.Errorf("people: rules[%d]: invalid characters in reason", i))
		}
		if r.From != 0 && (!slices.Contains(OutsidePorts, r.From) || r.Port < 1024) {
			errs = append(errs, fmt.Errorf("people: rules[%d]: from must be one of %v, sent to a port from 1024", i, OutsidePorts))
		}
		devices(fmt.Sprintf("rules[%d]", i), r.Devices)
	}
	switch {
	case p.Port == 0 && len(p.Internet) > 0, p.Port == 0 && !p.Forwarded && len(p.Home) > 0:
		errs = append(errs, errors.New("people: only the server devices connect to passes their traffic on"))
	case p.Forwarded && (p.Port != 0 || len(p.PassOn) > 0):
		errs = append(errs, errors.New("people: devices can't both connect here and arrive from another server"))
	case len(p.PassOn) > 0 && p.Port == 0:
		errs = append(errs, errors.New("people: only the server devices connect to passes them on"))
	case len(p.Servers) > 0 && !p.Forwarded && p.Port == 0:
		errs = append(errs, errors.New("people: only the server devices connect to, or the controller, passes them on to other servers"))
	}
	for i, a := range p.PassOn {
		if !validPrivate(a) {
			errs = append(errs, fmt.Errorf("people: pass_on[%d]: %q must be a private network", i, a))
		}
	}
	devices("home", p.Home)
	devices("internet", p.Internet)
	devices("servers", p.Servers)
	return errs
}

// devicesFrom matches the VPN's devices: on its interface, or passed on
// over Roostack's network by the server they connect to. A device's
// address from anywhere else isn't one (trust.Classify).
var devicesFrom = fmt.Sprintf("iifname { %q, %q } ", PeopleInterface, WireGuardInterface)

// people writes what the VPN's devices reach here (People), before any
// rule by source: their own rules, then on to what's open to anyone. On
// their interface, nothing but a device's address gets in.
func (f Firewall) people(b *strings.Builder) {
	fmt.Fprintf(b, "\t\tiifname %q ip saddr != %s drop\n", PeopleInterface, trust.DevicesRange)
	fmt.Fprintf(b, "\t\tiifname %q meta nfproto ipv6 drop\n", PeopleInterface)
	if f.People == nil {
		return
	}
	// Caddy, which lets each device through to what its person may use,
	// even while public access is closed (Closed): devices aren't the
	// public.
	b.WriteString("\t\t# caddy, for the lab's VPN devices\n")
	fmt.Fprintf(b, "\t\t%sip saddr %s tcp dport { 80, 443 } accept\n", devicesFrom, trust.DevicesRange)
	fmt.Fprintf(b, "\t\t%sip saddr %s udp dport 443 accept\n", devicesFrom, trust.DevicesRange)
	for _, r := range f.People.Rules {
		fmt.Fprintf(b, "\t\t# %s, for the lab's VPN devices that may\n", r.Reason)
		fmt.Fprintf(b, "\t\t%sip saddr { %s } %s dport %d accept\n", devicesFrom, strings.Join(r.Devices, ", "), r.Proto, r.Port)
	}
}

// wireguardLimit drops a source's new WireGuard handshakes beyond a few a
// second. WireGuard answers nobody without a key, but checking each
// handshake costs the server some work, so a flood of fake ones is capped
// per address. A handshake initiation is a packet whose first payload byte
// is 1 (@th,64,8: the byte after the 8-byte UDP header); a real peer sends
// one every two minutes, or every 5 seconds while retrying, and once its
// handshake is answered conntrack lets its packets through before this.
func wireguardLimit(b *strings.Builder, port int) {
	fmt.Fprintf(b, "\t\tudp dport %d @th,64,8 1 add @wg_handshakes4 { ip saddr limit rate over 5/second burst 20 packets } drop\n", port)
	fmt.Fprintf(b, "\t\tudp dport %d @th,64,8 1 add @wg_handshakes6 { ip6 saddr limit rate over 5/second burst 20 packets } drop\n", port)
}

// Ruleset renders the nftables ruleset. Roostack owns only the "inet roostack"
// table; it is replaced atomically on each apply. A drop in this table is
// final even if other tables accept the packet.
func (f Firewall) Ruleset() string {
	var b strings.Builder
	b.WriteString("# Generated by roostack-agent. Do not edit: changes will be overwritten.\n")
	// Declare then delete, so the file loads whether or not the table exists.
	b.WriteString("table inet roostack\ndelete table inet roostack\n\n")
	if f.Unchosen {
		f.setupOnly(&b)
		return b.String()
	}
	b.WriteString("table inet roostack {\n")
	lanSets(&b)
	// Each source's rate of new WireGuard handshakes (wireguardLimit).
	b.WriteString("\tset wg_handshakes4 {\n\t\ttype ipv4_addr\n\t\tflags dynamic, timeout\n\t\ttimeout 1m\n\t\tsize 65536\n\t}\n\n")
	b.WriteString("\tset wg_handshakes6 {\n\t\ttype ipv6_addr\n\t\tflags dynamic, timeout\n\t\ttimeout 1m\n\t\tsize 65536\n\t}\n\n")
	// Edges reach Caddy only on its edge listener: their connections to
	// 443 are sent there, so whatever a PROXY header says, they're never
	// private, and the listener for everyone else believes no header.
	b.WriteString("\tchain prerouting {\n")
	b.WriteString("\t\ttype nat hook prerouting priority dstnat; policy accept;\n\n")
	fmt.Fprintf(&b, "\t\tiifname %q tcp dport 443 redirect to :%d\n", RelayInterface, EdgePort)
	if len(f.Edges) > 0 {
		fmt.Fprintf(&b, "\t\tiifname %q ip saddr { %s } tcp dport 443 redirect to :%d\n", WireGuardInterface,
			strings.Join(f.Edges, ", "), EdgePort)
	}
	// Well-known ports sent on to a service's (Rule.From), for those who
	// may reach that port: the input chain then sees the service's port.
	for _, r := range f.Allow {
		if r.From == 0 {
			continue
		}
		fmt.Fprintf(&b, "\t\t# %s: %d to %d\n", r.Reason, r.From, r.Port)
		switch r.For {
		case trust.Home:
			fmt.Fprintf(&b, "\t\tip saddr @lan4 %s dport %d redirect to :%d\n", r.Proto, r.From, r.Port)
			fmt.Fprintf(&b, "\t\tip6 saddr @lan6 %s dport %d redirect to :%d\n", r.Proto, r.From, r.Port)
		default:
			fmt.Fprintf(&b, "\t\t%s dport %d redirect to :%d\n", r.Proto, r.From, r.Port)
		}
	}
	if p := f.People; p != nil {
		for _, r := range p.Rules {
			if r.From != 0 {
				fmt.Fprintf(&b, "\t\t# %s: %d to %d, for the lab's VPN devices that may\n", r.Reason, r.From, r.Port)
				fmt.Fprintf(&b, "\t\t%sip saddr { %s } %s dport %d redirect to :%d\n", devicesFrom, strings.Join(r.Devices, ", "), r.Proto, r.From, r.Port)
			}
		}
	}
	// Public ports sent on to another server (PortForwards): from the
	// internet, connections to this server's own addresses, never those
	// on Roostack's interfaces.
	for _, p := range f.PortForwards {
		from := `iifname != "roostack*" fib daddr type local `
		if p.FromEdge {
			from = fmt.Sprintf("iifname %q ip saddr { %s } ", WireGuardInterface, strings.Join(f.Edges, ", "))
		}
		fmt.Fprintf(&b, "\t\t# %s: sent on to %s\n", p.Reason, p.To)
		fmt.Fprintf(&b, "\t\t%s%s dport %d dnat ip to %s\n", from, p.Proto, p.Port, p.To)
	}
	b.WriteString("\t}\n\n")
	b.WriteString("\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority filter; policy drop;\n\n")
	b.WriteString("\t\tct state established,related accept\n")
	b.WriteString("\t\tct state invalid drop\n")
	// Roostack Cloud's relay brings visitors to Caddy, and nothing else
	// (Roostack Cloud's DESIGN §5: the lab accepts only its forwarded ports
	// from the tunnel).
	edgeVerdict := "accept"
	if f.Closed {
		b.WriteString("\t\t# public access closed completely: the relay's and the edges' visitors are refused\n")
		edgeVerdict = "reject with tcp reset"
	}
	fmt.Fprintf(&b, "\t\tiifname %q tcp dport %d %s\n", RelayInterface, EdgePort, edgeVerdict)
	fmt.Fprintf(&b, "\t\tiifname %q drop\n", RelayInterface)
	if len(f.Edges) > 0 {
		fmt.Fprintf(&b, "\t\tiifname %q ip saddr { %s } tcp dport %d %s\n", WireGuardInterface,
			strings.Join(f.Edges, ", "), EdgePort, edgeVerdict)
	}
	// On the hub, a server still joining reaches only the lab API.
	if f.JoinPort != 0 {
		fmt.Fprintf(&b, "\t\t%stcp dport %d accept\n", f.notMember(), f.JoinPort)
		fmt.Fprintf(&b, "\t\t%sdrop\n", f.notMember())
	}
	// Other labs storing backups here reach the guest API and their own
	// stores, and this lab's links to other labs' storage take nothing in
	// (SPEC.md §10.10). Settled here, before any rule by source: their
	// addresses are never home, but nothing on these interfaces should
	// depend on that.
	for _, r := range f.Allow {
		if r.For == trust.Guests {
			fmt.Fprintf(&b, "\t\t# %s\n", r.Reason)
			fmt.Fprintf(&b, "\t\t%s\n", r.match("accept"))
		}
	}
	for _, iface := range otherLabs {
		fmt.Fprintf(&b, "\t\tiifname %q drop\n", iface)
	}
	f.servers(&b)
	f.people(&b)
	b.WriteString("\t\tiif \"lo\" accept\n")
	b.WriteString("\t\tmeta l4proto { icmp, ipv6-icmp } accept\n")
	// DHCPv6 replies come from the router's link-local address to a port
	// conntrack doesn't pair with the multicast request.
	b.WriteString("\t\tip6 saddr fe80::/10 udp dport 546 accept\n\n")

	rules := slices.DeleteFunc(slices.Clone(f.Allow), func(r Rule) bool {
		return r.For == trust.Guests || r.For == trust.Servers || r.For == trust.Controller || r.For == trust.Edge
	})
	if f.SSHFrom != FromOff {
		rules = append(rules, Rule{Port: 22, Proto: "tcp", For: trust.Class(f.SSHFrom), Reason: "ssh"})
	}
	if f.SetupPort != 0 {
		rules = append(rules, Rule{Port: f.SetupPort, Proto: "tcp", For: trust.Home, Reason: setupReason})
	}
	if p := f.People; p != nil && p.Port != 0 {
		rules = append(rules, Rule{Port: p.Port, Proto: "udp", For: trust.Anyone, Reason: "the lab's VPN (wireguard)"})
	}
	slices.SortStableFunc(rules, func(a, b Rule) int {
		if a.Port != b.Port {
			return a.Port - b.Port
		}
		return strings.Compare(a.Proto, b.Proto)
	})
	for _, r := range rules {
		fmt.Fprintf(&b, "\t\t# %s\n", r.Reason)
		if r.Proto == "udp" && r.For == trust.Anyone && (r.Port == WireGuardPort || r.Port == GuestsPort ||
			f.People != nil && r.Port == f.People.Port) {
			wireguardLimit(&b, r.Port)
		}
		switch r.For {
		case trust.Home:
			fmt.Fprintf(&b, "\t\tip saddr @lan4 %s dport %d accept\n", r.Proto, r.Port)
			fmt.Fprintf(&b, "\t\tip6 saddr @lan6 %s dport %d accept\n", r.Proto, r.Port)
		default:
			fmt.Fprintf(&b, "\t\t%s dport %d accept\n", r.Proto, r.Port)
		}
	}
	if f.Closed {
		b.WriteString("\t\t# public access closed completely: everyone else is refused at once\n")
		b.WriteString("\t\ttcp dport { 80, 443 } reject with tcp reset\n")
		b.WriteString("\t\tudp dport 443 reject\n")
	}
	b.WriteString("\t}\n\n")
	// Roostack's network is never a way through this server, except for
	// the ports listed: the hub forwards one server's backups to another's
	// store (SPEC.md §10.10). Other forwarding (e.g. other software's
	// bridges) is left alone.
	b.WriteString("\tchain forward {\n")
	b.WriteString("\t\ttype filter hook forward priority filter; policy accept;\n\n")
	// Nothing passes to or from other labs or Roostack Cloud's relay.
	for _, iface := range append([]string{RelayInterface}, otherLabs...) {
		fmt.Fprintf(&b, "\t\tiifname %q drop\n", iface)
		fmt.Fprintf(&b, "\t\toifname %q drop\n", iface)
	}
	if f.JoinPort != 0 {
		fmt.Fprintf(&b, "\t\t%sdrop\n", f.notMember())
	}
	f.peopleForward(&b)
	fmt.Fprintf(&b, "\t\tiifname %q oifname %q ct state established,related accept\n", WireGuardInterface, WireGuardInterface)
	for _, r := range f.Forward {
		fmt.Fprintf(&b, "\t\t# %s\n", r.Reason)
		fmt.Fprintf(&b, "\t\tiifname %q oifname %q ip saddr %s tcp dport %d accept\n", WireGuardInterface, WireGuardInterface,
			trust.ServersRange, r.Port)
	}
	for _, p := range f.PortForwards {
		fmt.Fprintf(&b, "\t\t# %s: sent on to %s, and its answers\n", p.Reason, p.To)
		fmt.Fprintf(&b, "\t\toifname %q ip daddr %s %s dport %d ct status dnat accept\n", WireGuardInterface, p.To, p.Proto, p.Port)
		fmt.Fprintf(&b, "\t\tiifname %q ip saddr %s %s sport %d ct status dnat accept\n", WireGuardInterface, p.To, p.Proto, p.Port)
	}
	fmt.Fprintf(&b, "\t\tiifname %q drop\n", WireGuardInterface)
	fmt.Fprintf(&b, "\t\toifname %q drop\n", WireGuardInterface)
	if owned := slices.DeleteFunc(slices.Clone(f.ForwardOwned), func(i string) bool { return !ValidIfaceName(i) }); len(owned) > 0 {
		// Forwarding is on there only for the lab's VPN's answers and
		// PortForwards (accepted above): nothing else is routed through
		// this server.
		set := `"` + strings.Join(owned, `", "`) + `"`
		b.WriteString("\t\t# forwarding is on here for the lab's VPN's answers and the ports sent on only\n")
		fmt.Fprintf(&b, "\t\tiifname { %s } ct state established,related accept\n", set)
		// Other software's own forwarding there, started since (a
		// container's published port), is left alone: SPEC.md §10.11.
		fmt.Fprintf(&b, "\t\tiifname { %s } ct status dnat accept\n", set)
		fmt.Fprintf(&b, "\t\tiifname { %s } drop\n", set)
	}
	b.WriteString("\t}\n")
	f.postrouting(&b)
	b.WriteString("}\n")
	return b.String()
}

// peopleForward writes what the VPN's devices may pass through to: the
// home network for People.Home, anywhere for People.Internet, and the
// answers; nothing else goes to or from their interface (SPEC.md
// §10.11).
func (f Firewall) peopleForward(b *strings.Builder) {
	// Never to link-local addresses, where a cloud server's provider keeps
	// its metadata service (169.254.169.254): it would answer a device as
	// it answers the server.
	fmt.Fprintf(b, "\t\tiifname %q ip daddr %s drop\n", PeopleInterface, linkLocal)
	if p := f.People; p != nil && len(p.PassOn) > 0 {
		b.WriteString("\t\t# the lab's VPN devices, passed on to the controller over Roostack's network\n")
		fmt.Fprintf(b, "\t\tiifname %q oifname %q ip saddr %s ip daddr { %s } accept\n", PeopleInterface, WireGuardInterface,
			trust.DevicesRange, strings.Join(p.PassOn, ", "))
		fmt.Fprintf(b, "\t\tiifname %q oifname %q ip daddr %s ct state established,related accept\n", WireGuardInterface, PeopleInterface,
			trust.DevicesRange)
	}
	if p := f.People; p != nil && p.Forwarded {
		// Devices connect to another server, which passes them on to the
		// controller over Roostack's network.
		if len(p.Home) > 0 {
			b.WriteString("\t\t# the lab's VPN devices that reach the home network, from the server they connect to\n")
			fmt.Fprintf(b, "\t\tiifname %q oifname != { %q, %q } ip saddr { %s } ip daddr @lan4 accept\n", WireGuardInterface,
				PeopleInterface, WireGuardInterface, strings.Join(p.Home, ", "))
			fmt.Fprintf(b, "\t\toifname %q ip daddr %s ct state established,related accept\n", WireGuardInterface, trust.DevicesRange)
		}
	}
	if p := f.People; p != nil && len(p.Servers) > 0 {
		from := PeopleInterface
		if p.Forwarded {
			from = WireGuardInterface
		}
		b.WriteString("\t\t# the lab's VPN devices allowed SSH, to the lab's other servers\n")
		fmt.Fprintf(b, "\t\tiifname %q oifname %q ip saddr { %s } ip daddr %s tcp dport 22 accept\n", from,
			WireGuardInterface, strings.Join(p.Servers, ", "), trust.ServersRange)
	}
	if p := f.People; p != nil && p.Port != 0 {
		if len(p.Internet) > 0 {
			// The internet, not this server's own private networks (a
			// cloud provider's, or the home's, which is Home's).
			b.WriteString("\t\t# the lab's VPN devices whose internet traffic leaves from here\n")
			fmt.Fprintf(b, "\t\tiifname %q oifname != { %q, %q } ip saddr { %s } ip daddr != @lan4 accept\n", PeopleInterface, PeopleInterface,
				WireGuardInterface, strings.Join(p.Internet, ", "))
		}
		if len(p.Home) > 0 {
			b.WriteString("\t\t# the lab's VPN devices that reach the home network\n")
			fmt.Fprintf(b, "\t\tiifname %q oifname != { %q, %q } ip saddr { %s } ip daddr @lan4 accept\n", PeopleInterface,
				PeopleInterface, WireGuardInterface, strings.Join(p.Home, ", "))
		}
		fmt.Fprintf(b, "\t\toifname %q ct state established,related accept\n", PeopleInterface)
	}
	fmt.Fprintf(b, "\t\tiifname %q drop\n", PeopleInterface)
	fmt.Fprintf(b, "\t\toifname %q drop\n", PeopleInterface)
}

// postrouting gives what goes on from this server its address: the VPN's
// devices whose traffic goes on, so the home network's devices and the
// internet answer them through it, and the public ports it sends on to
// another server (PortForwards), whose answers come back through it.
func (f Firewall) postrouting(b *strings.Builder) {
	p := f.People
	if !p.ForwardsTraffic() && len(f.PortForwards) == 0 {
		return
	}
	b.WriteString("\n\tchain postrouting {\n")
	b.WriteString("\t\ttype nat hook postrouting priority srcnat; policy accept;\n\n")
	if p.ForwardsTraffic() {
		from := PeopleInterface
		if p.Forwarded {
			from = WireGuardInterface
		}
		fmt.Fprintf(b, "\t\tiifname %q oifname != { %q, %q } ip saddr %s masquerade\n", from, PeopleInterface,
			WireGuardInterface, trust.DevicesRange)
	}
	for _, pf := range f.PortForwards {
		fmt.Fprintf(b, "\t\toifname %q ip daddr %s %s dport %d ct status dnat masquerade\n", WireGuardInterface, pf.To, pf.Proto, pf.Port)
	}
	b.WriteString("\t}\n")
}

// linkLocal is IPv4's link-local range (RFC 3927).
const linkLocal = "169.254.0.0/16"

// setupReason labels the setup pages' rule.
const setupReason = "first-run setup pages, until the Owner has finished setup"

// lanSets writes the home network's address sets, lan4 and lan6.
func lanSets(b *strings.Builder) {
	b.WriteString("\tset lan4 {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\telements = { " + lanElements(false) + " }\n\t}\n\n")
	b.WriteString("\tset lan6 {\n\t\ttype ipv6_addr\n\t\tflags interval\n\t\telements = { " + lanElements(true) + " }\n\t}\n\n")
}

// setupOnly writes the table for before the Owner has chosen the firewall
// (Unchosen): the setup pages' port for the home network only, everything
// else accepted as without Roostack's table (SPEC.md §6.3.2). With no
// setup port, the table is just deleted.
func (f Firewall) setupOnly(b *strings.Builder) {
	if f.SetupPort == 0 {
		return
	}
	b.WriteString("table inet roostack {\n")
	lanSets(b)
	b.WriteString("\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority filter; policy accept;\n\n")
	fmt.Fprintf(b, "\t\t# %s\n", setupReason)
	fmt.Fprintf(b, "\t\tip saddr @lan4 tcp dport %d accept\n", f.SetupPort)
	fmt.Fprintf(b, "\t\tip6 saddr @lan6 tcp dport %d accept\n", f.SetupPort)
	fmt.Fprintf(b, "\t\ttcp dport %d drop\n", f.SetupPort)
	b.WriteString("\t}\n}\n")
}

// otherLabs are the interfaces to other labs: guests storing here, and
// this lab's links to their storage (nftables matches the name's prefix).
var otherLabs = []string{GuestsInterface, trust.StoreLinks}

// servers writes what the lab's servers reach on Roostack's network: the
// rules for them, SSH from the controller and ping, nothing else (SPEC.md
// §10.2, review M23). Settled here, before any rule by source, like the
// other labs: the servers' range isn't home anyway. People's devices
// (trust.DevicesRange) go on to their own rules (people); nothing else on
// that interface gets in.
func (f Firewall) servers(b *strings.Builder) {
	from := fmt.Sprintf("iifname %q ip saddr %s ", WireGuardInterface, trust.ServersRange)
	rules := slices.DeleteFunc(slices.Clone(f.Allow), func(r Rule) bool { return r.For != trust.Servers })
	slices.SortStableFunc(rules, func(a, b Rule) int { return a.Port - b.Port })
	for _, r := range rules {
		fmt.Fprintf(b, "\t\t# %s\n", r.Reason)
		fmt.Fprintf(b, "\t\t%s%s dport %d accept\n", from, r.Proto, r.Port)
	}
	// What only the controller reaches, such as a live view's Glances.
	for _, r := range f.Allow {
		if r.For == trust.Controller && f.Controller != "" {
			fmt.Fprintf(b, "\t\t# %s\n", r.Reason)
			fmt.Fprintf(b, "\t\tiifname %q ip saddr %s %s dport %d accept\n", WireGuardInterface, f.Controller, r.Proto, r.Port)
		}
	}
	// Public ports the lab's edges send on (PortForwards).
	for _, r := range f.Allow {
		if r.For == trust.Edge && len(f.Edges) > 0 {
			fmt.Fprintf(b, "\t\t# %s, sent on by the lab's edge\n", r.Reason)
			fmt.Fprintf(b, "\t\tiifname %q ip saddr { %s } %s dport %d accept\n", WireGuardInterface, strings.Join(f.Edges, ", "), r.Proto, r.Port)
		}
	}
	if f.Controller != "" && f.SSHFrom != FromOff {
		b.WriteString("\t\t# ssh from the controller\n")
		fmt.Fprintf(b, "\t\tiifname %q ip saddr %s tcp dport 22 accept\n", WireGuardInterface, f.Controller)
	}
	fmt.Fprintf(b, "\t\t%smeta l4proto icmp accept\n", from)
	if f.People == nil {
		// No device has anything opened here, so none gets in, whatever
		// the other end of the link lets through.
		b.WriteString("\t\t# nothing else from the lab's servers\n")
		fmt.Fprintf(b, "\t\tiifname %q drop\n", WireGuardInterface)
		return
	}
	b.WriteString("\t\t# nothing else from the lab's servers; people's devices go on to their own rules\n")
	fmt.Fprintf(b, "\t\tiifname %q ip saddr != %s drop\n", WireGuardInterface, trust.DevicesRange)
	fmt.Fprintf(b, "\t\tiifname %q meta nfproto ipv6 drop\n", WireGuardInterface)
}

// match renders a guests' rule's nftables match and verdict.
func (r Rule) match(verdict string) string {
	from := ""
	if a, err := netip.ParseAddr(r.Source); err == nil {
		from = fmt.Sprintf("ip saddr %s ", a)
		if a.Is6() {
			from = fmt.Sprintf("ip6 saddr %s ", a)
		}
	}
	return fmt.Sprintf("iifname %q %s%s dport %d %s", GuestsInterface, from, r.Proto, r.Port, verdict)
}

// notMember matches traffic on Roostack's network from a server's address
// that isn't one of the lab's servers (Members).
func (f Firewall) notMember() string {
	from := fmt.Sprintf("iifname %q ip saddr %s ", WireGuardInterface, trust.ServersRange)
	if len(f.Members) == 0 {
		return from
	}
	return fmt.Sprintf("%sip saddr != { %s } ", from, strings.Join(f.Members, ", "))
}

// AllowWireGuardJoin adds one rule at the top of Roostack's input chain:
// UDP to WireGuardPort. The next ApplyFirewall replaces the whole table,
// so it lasts until then. Without Roostack's table (the Owner hasn't
// chosen its firewall yet) nothing blocks the port anyway.
func (h LinuxHost) AllowWireGuardJoin() error {
	if exec.Command("nft", "list", "table", "inet", "roostack").Run() != nil {
		return nil
	}
	out, err := exec.Command("nft", "insert", "rule", "inet", "roostack", "input", "udp", "dport", strconv.Itoa(WireGuardPort),
		"accept", "comment", `"roostack: joining a lab"`).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft: %w: %s", err, out)
	}
	return nil
}

// VPNForwardingPlan is vpnForwardingPlan.
func (h LinuxHost) VPNForwardingPlan(uplinks bool) (want, own []string) {
	return vpnForwardingPlan(uplinks)
}

// VPNForwardingOff puts forwarding back as it was on the interfaces the
// lab's VPN no longer needs (restoreVPNForwarding), and VPNForwardingOn
// turns it on for those it needs (setVPNForwarding).
func (h LinuxHost) VPNForwardingOff(want []string) error { return restoreVPNForwarding(want) }
func (h LinuxHost) VPNForwardingOn(want []string) error  { return setVPNForwarding(want) }

// ApplyFirewall checks the ruleset with `nft -c`, loads it, then saves it.
func (h LinuxHost) ApplyFirewall(ruleset string) error {
	tmp, err := os.CreateTemp("", "roostack-firewall-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(ruleset); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if out, err := exec.Command("nft", "-c", "-f", tmp.Name()).CombinedOutput(); err != nil {
		return fmt.Errorf("nft check: %w: %s", err, out)
	}
	if out, err := exec.Command("nft", "-f", tmp.Name()).CombinedOutput(); err != nil {
		return fmt.Errorf("nft apply: %w: %s", err, out)
	}
	if err := os.MkdirAll(filepath.Dir(h.FirewallFile), 0o755); err != nil {
		return err
	}
	return fsutil.WriteFile(h.FirewallFile, []byte(ruleset), 0o644)
}

// RestoreFirewall re-applies the saved ruleset at agent start, if any.
func (h LinuxHost) RestoreFirewall() error {
	if _, err := os.Stat(h.FirewallFile); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if out, err := exec.Command("nft", "-f", h.FirewallFile).CombinedOutput(); err != nil {
		return fmt.Errorf("nft restore: %w: %s", err, out)
	}
	return nil
}
