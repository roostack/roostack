package agent

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/trust"
)

// TestTrustClasses: the ruleset lets each source through exactly what
// internal/trust says its class may reach (review §6: one definition of
// who is who, read by the firewall and Caddy). It runs new connections
// from a source of every class against a small evaluator of the generated
// nftables rules, for every port a rule names, SSH, 443, the edge
// listener and a port nothing opens.
func TestTrustClasses(t *testing.T) {
	a := netip.MustParseAddr
	members := []netip.Addr{a("100.96.0.1"), a("100.96.0.2"), a("100.96.0.3")}
	edges := []netip.Addr{a("100.96.0.3")}
	lab := trust.Lab{Members: members, Edges: edges, Controller: a("100.96.0.1")}
	sources := []struct {
		name, iface string
		addr        netip.Addr
	}{
		{"home device", "eth0", a("192.168.1.5")},
		{"home device (IPv6)", "eth0", a("fd00::5")},
		{"Tailscale device", "tailscale0", a("100.64.1.1")},
		{"internet", "eth0", a("203.0.113.9")},
		{"internet (IPv6)", "eth0", a("2001:db8::9")},
		{"a guest's address, elsewhere", "eth0", a("100.100.5.2")},
		{"server", WireGuardInterface, a("100.96.0.2")},
		{"controller", WireGuardInterface, a("100.96.0.1")},
		{"person's device on Roostack's network", WireGuardInterface, a("100.96.128.5")},
		{"outside Roostack's ranges on its interface", WireGuardInterface, a("100.97.0.1")},
		{"IPv6 on Roostack's network", WireGuardInterface, a("fd00::7")},
		{"a server's address, elsewhere", "eth0", a("100.96.0.2")},
		{"edge", WireGuardInterface, a("100.96.0.3")},
		{"joining server", WireGuardInterface, a("100.96.0.9")},
		{"guest lab 1", GuestsInterface, a("100.100.5.2")},
		{"guest lab 2", GuestsInterface, a("100.100.5.3")},
		{"relay edge", RelayInterface, a("100.81.0.1")},
		{"another lab's storage", "roostack-st1", a("100.100.9.1")},
	}
	for _, ssh := range []string{FromLAN, FromAny, FromOff} {
		fw := DefaultFirewall()
		fw.SSHFrom = ssh
		fw.Allow = append(fw.Allow,
			Rule{Port: 8080, Proto: "tcp", For: trust.Home, Reason: "a home-only package port"},
			Rule{Port: 2222, Proto: "tcp", For: trust.Anyone, Reason: "a public package port"},
			Rule{Port: 8471, Proto: "tcp", For: trust.Servers, Reason: "roostack servers"},
			Rule{Port: 8000, Proto: "tcp", For: trust.Servers, Reason: "backup store"},
			Rule{Port: WireGuardPort, Proto: "udp", For: trust.Anyone, Reason: "roostack wireguard"},
			Rule{Port: GuestsPort, Proto: "udp", For: trust.Anyone, Reason: "storage for other labs (wireguard)"},
			Rule{Port: 8472, Proto: "tcp", For: trust.Guests, Reason: "storage for other labs"},
			Rule{Port: 8100, Proto: "tcp", For: trust.Guests, Source: "100.100.5.2", Reason: "storage for g1"},
		)
		fw.Edges = []string{"100.96.0.3"}
		fw.JoinPort, fw.Members = 8471, []string{"100.96.0.1", "100.96.0.2", "100.96.0.3"}
		fw.Controller = "100.96.0.1"
		if err := fw.Validate(); err != nil {
			t.Fatal(err)
		}
		nft := parseRuleset(t, fw.Ruleset())
		rules := slices.Clone(fw.Allow)
		if ssh != FromOff {
			rules = append(rules, Rule{Port: 22, Proto: "tcp", For: trust.Class(ssh)},
				Rule{Port: 22, Proto: "tcp", For: trust.Controller})
		}
		probes := []Rule{{Port: 443, Proto: "tcp"}, {Port: EdgePort, Proto: "tcp"}, {Port: 9999, Proto: "tcp"},
			{Port: 22, Proto: "tcp"}}
		for _, r := range rules {
			probes = append(probes, Rule{Port: r.Port, Proto: r.Proto})
		}
		for _, s := range sources {
			class := trust.Classify(s.iface, s.addr, lab)
			for _, p := range probes {
				wantPort, want := p.Port, false
				switch {
				case class == trust.Joining:
					want = p.Port == fw.JoinPort && p.Proto == "tcp"
				case (class == trust.Relay || class == trust.Edge) && p.Proto == "tcp" && (p.Port == 443 || p.Port == EdgePort):
					wantPort, want = EdgePort, true
				default:
					for _, r := range rules {
						if r.Port == p.Port && r.Proto == p.Proto && trust.May(class, r.For) && (r.Source == "" || r.Source == s.addr.String()) {
							want = true
						}
					}
				}
				port, got := nft.accepts(s.iface, s.addr, p.Proto, p.Port)
				if got != want || (got && port != wantPort) {
					t.Errorf("SSH %s: %s (%s, %s on %s) to %d/%s: accepted %v at %d, want %v at %d", ssh, s.name, class,
						s.addr, s.iface, p.Port, p.Proto, got, port, want, wantPort)
				}
			}
		}
	}
}

// nftRules is a parsed ruleset: its sets and its prerouting and input
// chains, one statement per line.
type nftRules struct {
	sets              map[string][]netip.Prefix
	prerouting, input [][]string
}

// parseRuleset reads the ruleset Firewall.Ruleset writes. It understands
// that subset of nftables and nothing more; anything else fails the test.
func parseRuleset(t *testing.T, rs string) nftRules {
	t.Helper()
	n := nftRules{sets: map[string][]netip.Prefix{}}
	var chain *[][]string
	set := ""
	for line := range strings.Lines(rs) {
		line = strings.TrimSpace(line)
		switch f := strings.Fields(line); {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "type ") ||
			strings.HasPrefix(line, "flags ") || strings.HasPrefix(line, "timeout ") || strings.HasPrefix(line, "size "):
		case len(f) == 3 && f[0] == "set":
			set = f[1]
		case strings.HasPrefix(line, "elements = {"):
			for _, e := range strings.Split(strings.Trim(strings.TrimPrefix(line, "elements = "), "{} "), ", ") {
				n.sets[set] = append(n.sets[set], netip.MustParsePrefix(e))
			}
		case strings.HasPrefix(line, "chain "):
			chain = map[string]*[][]string{"prerouting": &n.prerouting, "input": &n.input}[f[1]]
		case line == "}":
			chain, set = nil, ""
		case chain != nil:
			*chain = append(*chain, tokens(line))
		}
	}
	if len(n.input) == 0 || len(n.sets["lan4"]) == 0 {
		t.Fatalf("couldn't read the ruleset:\n%s", rs)
	}
	return n
}

// tokens splits a statement into words, keeping "{ a, b }" as one.
func tokens(line string) []string {
	var out []string
	for line != "" {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "{") {
			end := strings.Index(line, "}")
			out = append(out, line[:end+1])
			line = line[end+1:]
			continue
		}
		word, rest, _ := strings.Cut(line, " ")
		out = append(out, word)
		line = rest
	}
	return out
}

// accepts runs a new connection through prerouting and input, and
// reports whether it's accepted and at which port.
func (n nftRules) accepts(iface string, src netip.Addr, proto string, port int) (int, bool) {
	for _, st := range n.prerouting {
		if to, ok := n.match(st, iface, src, proto, port); ok && strings.HasPrefix(to, "redirect:") {
			port, _ = strconv.Atoi(strings.TrimPrefix(to, "redirect:"))
			break
		}
	}
	for _, st := range n.input {
		if v, ok := n.match(st, iface, src, proto, port); ok {
			return port, v == "accept"
		}
	}
	return port, false // policy drop
}

// match reports whether a statement matches the connection, and its
// verdict ("accept", "drop", "redirect:<port>").
func (n nftRules) match(st []string, iface string, src netip.Addr, proto string, port int) (string, bool) {
	for i := 0; i < len(st); i++ {
		w := st[i]
		arg := func() string { i++; return st[i] }
		switch w {
		case "accept", "drop":
			return w, true
		case "redirect": // redirect to :N
			i++
			return "redirect:" + strings.TrimPrefix(arg(), ":"), true
		case "ct":
			return "", false // conntrack's state: a new connection is neither established nor invalid
		case "iifname":
			name := strings.Trim(arg(), `"`)
			if pre, ok := strings.CutSuffix(name, "*"); (ok && !strings.HasPrefix(iface, pre)) || (!ok && name != iface) {
				return "", false
			}
		case "iif":
			if strings.Trim(arg(), `"`) != iface {
				return "", false
			}
		case "ip", "ip6":
			if (w == "ip") != src.Unmap().Is4() || arg() != "saddr" {
				return "", false
			}
			neg := st[i+1] == "!="
			if neg {
				i++
			}
			if n.inSet(arg(), src.Unmap()) == neg {
				return "", false
			}
		case "meta":
			switch arg() {
			case "l4proto":
				if !strings.Contains(arg(), "icmp") || !strings.Contains(proto, "icmp") {
					return "", false
				}
			case "nfproto":
				if (arg() == "ipv6") != src.Unmap().Is6() {
					return "", false
				}
			default:
				panic(fmt.Sprintf("meta %v", st))
			}
		case "tcp", "udp":
			if arg() != "dport" {
				panic(fmt.Sprintf("%v", st))
			}
			p, _ := strconv.Atoi(arg())
			if w != proto || p != port {
				return "", false
			}
			if i+1 < len(st) && strings.HasPrefix(st[i+1], "@th") {
				return "", false // the WireGuard handshake limit: not a flood
			}
		default:
			panic(fmt.Sprintf("unknown %q in %v", w, st))
		}
	}
	return "", false
}

// inSet reports whether addr is in "@set", "{ a, b }" or one address or
// range.
func (n nftRules) inSet(v string, addr netip.Addr) bool {
	var ps []netip.Prefix
	switch {
	case strings.HasPrefix(v, "@"):
		ps = n.sets[v[1:]]
	default:
		for _, e := range strings.Split(strings.Trim(v, "{} "), ",") {
			e = strings.TrimSpace(e)
			if !strings.Contains(e, "/") {
				e = netip.MustParseAddr(e).String() + "/" + strconv.Itoa(netip.MustParseAddr(e).BitLen())
			}
			ps = append(ps, netip.MustParsePrefix(e))
		}
	}
	for _, p := range ps {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
