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
}

// Rule allows one inbound port.
type Rule struct {
	Port  int    `json:"port"`
	Proto string `json:"proto"` // tcp or udp
	From  string `json:"from"`  // lan or any
	// Iface limits the rule to traffic arriving on an interface; only
	// Roostack's WireGuard interface is allowed.
	Iface string `json:"iface,omitempty"`
	// Reason is shown in the generated file, e.g. "caddy https".
	Reason string `json:"reason"`
}

// Source values.
const (
	FromLAN = "lan"
	FromAny = "any"
	FromOff = "off" // SSHFrom only
)

// LANRanges are the "home network" (and VPN) sources. They match
// caddy.PrivateRanges (docs/ASSUMPTIONS.md C5).
var LANRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
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
		if strings.ContainsFunc(r.Reason, func(c rune) bool { return c < ' ' || c == '"' || c == '\\' }) {
			errs = append(errs, fmt.Errorf("allow[%d]: invalid characters in reason", i))
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
	b.WriteString("\tchain input {\n")
	b.WriteString("\t\ttype filter hook input priority filter; policy drop;\n\n")
	b.WriteString("\t\tct state established,related accept\n")
	b.WriteString("\t\tct state invalid drop\n")
	b.WriteString("\t\tiif \"lo\" accept\n")
	b.WriteString("\t\tmeta l4proto { icmp, ipv6-icmp } accept\n")
	// DHCPv6 replies come from the router's link-local address to a port
	// conntrack doesn't pair with the multicast request.
	b.WriteString("\t\tip6 saddr fe80::/10 udp dport 546 accept\n\n")

	rules := slices.Clone(f.Allow)
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
			fmt.Fprintf(&b, "\t\tiifname %q %s dport %d accept\n", r.Iface, r.Proto, r.Port)
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
