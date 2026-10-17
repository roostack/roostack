package agent

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

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
	// (the lab's servers, on Roostack's network) or trust.Guests (other
	// labs storing backups here).
	For trust.Class `json:"for"`
	// Source limits a guests' rule to one address (a guest lab reaches
	// only its own store, SPEC.md §10.10).
	Source string `json:"source,omitempty"`
	// Reason is shown in the generated file, e.g. "caddy https".
	Reason string `json:"reason"`
}

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
			errs = append(errs, fmt.Errorf("allow[%d]: for must be %q, %q, %q or %q", i, trust.Anyone, trust.Home,
				trust.Servers, trust.Guests))
		}
		if r.Source != "" {
			if a, err := netip.ParseAddr(r.Source); err != nil || r.For != trust.Guests || !trust.GuestRange.Contains(a) {
				errs = append(errs, fmt.Errorf("allow[%d]: source must be a guest lab's address", i))
			}
		}
		if strings.ContainsFunc(r.Reason, func(c rune) bool { return c < ' ' || c == '"' || c == '\\' }) {
			errs = append(errs, fmt.Errorf("allow[%d]: invalid characters in reason", i))
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
		if r.For == trust.Guests {
			fmt.Fprintf(&b, "\t\t# %s\n", r.Reason)
			fmt.Fprintf(&b, "\t\t%s\n", r.match("accept"))
		}
	}
	for _, iface := range otherLabs {
		fmt.Fprintf(&b, "\t\tiifname %q drop\n", iface)
	}
	f.servers(&b)
	b.WriteString("\t\tiif \"lo\" accept\n")
	b.WriteString("\t\tmeta l4proto { icmp, ipv6-icmp } accept\n")
	// DHCPv6 replies come from the router's link-local address to a port
	// conntrack doesn't pair with the multicast request.
	b.WriteString("\t\tip6 saddr fe80::/10 udp dport 546 accept\n\n")

	rules := slices.DeleteFunc(slices.Clone(f.Allow), func(r Rule) bool { return r.For == trust.Guests || r.For == trust.Servers })
	if f.SSHFrom != FromOff {
		rules = append(rules, Rule{Port: 22, Proto: "tcp", For: trust.Class(f.SSHFrom), Reason: "ssh"})
	}
	slices.SortStableFunc(rules, func(a, b Rule) int {
		if a.Port != b.Port {
			return a.Port - b.Port
		}
		return strings.Compare(a.Proto, b.Proto)
	})
	for _, r := range rules {
		fmt.Fprintf(&b, "\t\t# %s\n", r.Reason)
		if r.Proto == "udp" && r.For == trust.Anyone && (r.Port == WireGuardPort || r.Port == GuestsPort) {
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
		fmt.Fprintf(&b, "\t\tiifname %q oifname %q ip saddr %s tcp dport %d accept\n", WireGuardInterface, WireGuardInterface,
			trust.ServersRange, r.Port)
	}
	fmt.Fprintf(&b, "\t\tiifname %q drop\n", WireGuardInterface)
	fmt.Fprintf(&b, "\t\toifname %q drop\n", WireGuardInterface)
	b.WriteString("\t}\n}\n")
	return b.String()
}

// otherLabs are the interfaces to other labs: guests storing here, and
// this lab's links to their storage (nftables matches the name's prefix).
var otherLabs = []string{GuestsInterface, trust.StoreLinks}

// servers writes what the lab's servers reach on Roostack's network: the
// rules for them, SSH from the controller and ping, nothing else (SPEC.md
// §10.2, review M23). Settled here, before any rule by source, like the
// other labs: the servers' range isn't home anyway. People's devices
// (trust.DevicesRange) go on to the home network's rules; nothing else on
// that interface gets in.
func (f Firewall) servers(b *strings.Builder) {
	from := fmt.Sprintf("iifname %q ip saddr %s ", WireGuardInterface, trust.ServersRange)
	rules := slices.DeleteFunc(slices.Clone(f.Allow), func(r Rule) bool { return r.For != trust.Servers })
	slices.SortStableFunc(rules, func(a, b Rule) int { return a.Port - b.Port })
	for _, r := range rules {
		fmt.Fprintf(b, "\t\t# %s\n", r.Reason)
		fmt.Fprintf(b, "\t\t%s%s dport %d accept\n", from, r.Proto, r.Port)
	}
	if f.Controller != "" && f.SSHFrom != FromOff {
		b.WriteString("\t\t# ssh from the controller\n")
		fmt.Fprintf(b, "\t\tiifname %q ip saddr %s tcp dport 22 accept\n", WireGuardInterface, f.Controller)
	}
	b.WriteString("\t\t# nothing else from the lab's servers; people's devices are home\n")
	fmt.Fprintf(b, "\t\t%smeta l4proto icmp accept\n", from)
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
