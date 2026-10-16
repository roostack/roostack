package agent

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
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
}

// EdgePort is Caddy's listener for visitors through the lab's edges, whose
// sites count everyone as outside (caddy.EdgePort, the same number): a
// PROXY header can't claim a home address there (review H4).
const EdgePort = 8474

// Rule allows one inbound port.
type Rule struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"` // tcp or udp
	From  string `json:"from"`  // lan or any
	// Iface limits the rule to traffic arriving on an interface; only
	// Roostack's WireGuard interfaces are allowed.
	Iface string `json:"iface,omitempty"`
	// Source limits an interface's rule to one address on it (a guest
	// lab reaches only its own store, SPEC.md §10.10).
	Source string `json:"source,omitempty"`
	// Reason is shown in the generated file, e.g. "caddy https".
	Reason string `json:"reason"`
}

// Source values.
const (
	FromLAN = "lan"
	FromAny = "any"
	FromOff = "off" // SSHFrom only
)

// CGNATRange is the shared address space (RFC 6598) Tailscale uses, and
// Roostack's own WireGuard networks are inside.
var CGNATRange = netip.MustParsePrefix("100.64.0.0/10")

// LANRanges are the "home network" (and VPN) sources: the private ranges,
// and the CGNAT range (Tailscale, Roostack's network) except other labs
// (GuestRange) and Roostack Cloud's relay (RelayRange), which are never
// home (SPEC.md §6.10). They match caddy.PrivateRanges (docs/ASSUMPTIONS.md
// C5).
var LANRanges = slices.Concat(
	[]netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16")},
	Without(CGNATRange, GuestRange, RelayRange),
	[]netip.Prefix{netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10")},
)

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

// IsLAN reports whether addr is on the home network (or a VPN) as the
// firewall sees it.
func IsLAN(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range LANRanges {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func lanElements(v6 bool) string {
	var out []string
	for _, p := range LANRanges {
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
		{Port: 80, Proto: "tcp", From: FromAny, Reason: "caddy http (redirects to https)"},
		{Port: 443, Proto: "tcp", From: FromAny, Reason: "caddy https"},
		{Port: 443, Proto: "udp", From: FromAny, Reason: "caddy http/3"},
	}}
}

// Validate checks the firewall request. The agent never trusts its input.
func (f Firewall) Validate() error {
	var errs []error
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
		if r.From != FromLAN && r.From != FromAny && !(r.Iface != "" && r.From == "") {
			errs = append(errs, fmt.Errorf("allow[%d]: from must be %q or %q", i, FromLAN, FromAny))
		}
		if r.Iface != "" && !ValidInterface(r.Iface) {
			errs = append(errs, fmt.Errorf("allow[%d]: iface must be one of Roostack's WireGuard interfaces", i))
		}
		if r.Source != "" {
			if a, err := netip.ParseAddr(r.Source); err != nil || r.Iface == "" || !rangeFor(r.Iface).Contains(a) {
				errs = append(errs, fmt.Errorf("allow[%d]: source must be an address on the rule's interface", i))
			}
		}
		if strings.ContainsFunc(r.Reason, func(c rune) bool { return c < ' ' || c == '"' || c == '\\' }) {
			errs = append(errs, fmt.Errorf("allow[%d]: invalid characters in reason", i))
		}
	}
	for i, e := range f.Edges {
		if a, err := netip.ParseAddr(e); err != nil || !OverlayRange.Contains(a) {
			errs = append(errs, fmt.Errorf("edges[%d]: not an address on Roostack's network", i))
		}
	}
	if f.JoinPort < 0 || f.JoinPort > 65535 || (f.JoinPort == 0 && len(f.Members) > 0) {
		errs = append(errs, errors.New("join_port: invalid port, or members without one"))
	}
	for i, m := range f.Members {
		if a, err := netip.ParseAddr(m); err != nil || !OverlayRange.Contains(a) {
			errs = append(errs, fmt.Errorf("members[%d]: not an address on Roostack's network", i))
		}
	}
	for i, r := range f.Forward {
		if r.Port < 1 || r.Port > 65535 || r.Proto != "tcp" || r.Iface != WireGuardInterface || r.From != "" {
			errs = append(errs, fmt.Errorf("forward[%d]: only a TCP port between Roostack's servers (iface %q)", i, WireGuardInterface))
		}
		if strings.ContainsFunc(r.Reason, func(c rune) bool { return c < ' ' || c == '"' || c == '\\' }) {
			errs = append(errs, fmt.Errorf("forward[%d]: invalid characters in reason", i))
		}
	}
	return errors.Join(errs...)
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
	b.WriteString("table inet roostack {\n")
	b.WriteString("\tset lan4 {\n\t\ttype ipv4_addr\n\t\tflags interval\n\t\telements = { " + lanElements(false) + " }\n\t}\n\n")
	b.WriteString("\tset lan6 {\n\t\ttype ipv6_addr\n\t\tflags interval\n\t\telements = { " + lanElements(true) + " }\n\t}\n\n")
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
	b.WriteString("\t}\n\n")
	b.WriteString("\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority filter; policy drop;\n\n")
	b.WriteString("\t\tct state established,related accept\n")
	b.WriteString("\t\tct state invalid drop\n")
	// Roostack Cloud's relay brings visitors to Caddy, and nothing else
	// (Roostack Cloud's DESIGN §5: the lab accepts only its forwarded ports
	// from the tunnel).
	fmt.Fprintf(&b, "\t\tiifname %q tcp dport %d accept\n", RelayInterface, EdgePort)
	fmt.Fprintf(&b, "\t\tiifname %q drop\n", RelayInterface)
	if len(f.Edges) > 0 {
		fmt.Fprintf(&b, "\t\tiifname %q ip saddr { %s } tcp dport %d accept\n", WireGuardInterface,
			strings.Join(f.Edges, ", "), EdgePort)
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
		if r.Iface != "" && r.Iface != WireGuardInterface {
			fmt.Fprintf(&b, "\t\t# %s\n", r.Reason)
			fmt.Fprintf(&b, "\t\t%s\n", r.match("accept"))
		}
	}
	for _, iface := range otherLabs {
		fmt.Fprintf(&b, "\t\tiifname %q drop\n", iface)
	}
	b.WriteString("\t\tiif \"lo\" accept\n")
	b.WriteString("\t\tmeta l4proto { icmp, ipv6-icmp } accept\n")
	// DHCPv6 replies come from the router's link-local address to a port
	// conntrack doesn't pair with the multicast request.
	b.WriteString("\t\tip6 saddr fe80::/10 udp dport 546 accept\n\n")

	rules := slices.DeleteFunc(slices.Clone(f.Allow), func(r Rule) bool {
		return r.Iface != "" && r.Iface != WireGuardInterface
	})
	if f.SSHFrom != FromOff {
		rules = append(rules, Rule{Port: 22, Proto: "tcp", From: f.SSHFrom, Reason: "ssh"})
	}
	slices.SortStableFunc(rules, func(a, b Rule) int {
		if a.Port != b.Port {
			return a.Port - b.Port
		}
		return strings.Compare(a.Proto, b.Proto)
	})
	for _, r := range rules {
		fmt.Fprintf(&b, "\t\t# %s\n", r.Reason)
		if r.Proto == "udp" && r.Iface == "" && (r.Port == WireGuardPort || r.Port == GuestsPort) {
			wireguardLimit(&b, r.Port)
		}
		switch {
		case r.Iface != "":
			fmt.Fprintf(&b, "\t\t%s\n", r.match("accept"))
		case r.From == FromLAN:
			fmt.Fprintf(&b, "\t\tip saddr @lan4 %s dport %d accept\n", r.Proto, r.Port)
			fmt.Fprintf(&b, "\t\tip6 saddr @lan6 %s dport %d accept\n", r.Proto, r.Port)
		default:
			fmt.Fprintf(&b, "\t\t%s dport %d accept\n", r.Proto, r.Port)
		}
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
	fmt.Fprintf(&b, "\t\tiifname %q oifname %q ct state established,related accept\n", WireGuardInterface, WireGuardInterface)
	for _, r := range f.Forward {
		fmt.Fprintf(&b, "\t\t# %s\n", r.Reason)
		fmt.Fprintf(&b, "\t\tiifname %q oifname %q tcp dport %d accept\n", WireGuardInterface, WireGuardInterface, r.Port)
	}
	fmt.Fprintf(&b, "\t\tiifname %q drop\n", WireGuardInterface)
	fmt.Fprintf(&b, "\t\toifname %q drop\n", WireGuardInterface)
	b.WriteString("\t}\n}\n")
	return b.String()
}

// otherLabs are the interfaces to other labs: guests storing here, and
// this lab's links to their storage (nftables matches the name's prefix).
var otherLabs = []string{GuestsInterface, "roostack-st*"}

// match renders an interface rule's nftables match and verdict.
func (r Rule) match(verdict string) string {
	from := ""
	if a, err := netip.ParseAddr(r.Source); err == nil {
		from = fmt.Sprintf("ip saddr %s ", a)
		if a.Is6() {
			from = fmt.Sprintf("ip6 saddr %s ", a)
		}
	}
	return fmt.Sprintf("iifname %q %s%s dport %d %s", r.Iface, from, r.Proto, r.Port, verdict)
}

// notMember matches traffic on Roostack's network from anyone but the
// lab's servers (Members).
func (f Firewall) notMember() string {
	if len(f.Members) == 0 {
		return fmt.Sprintf("iifname %q ", WireGuardInterface)
	}
	return fmt.Sprintf("iifname %q ip saddr != { %s } ", WireGuardInterface, strings.Join(f.Members, ", "))
}
