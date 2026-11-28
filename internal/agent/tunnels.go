package agent

import (
	"encoding/json"
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

// Tunnel is an IP-in-IP link to another of the lab's servers inside
// Roostack's network, for public ports that keep visitors' addresses
// (PortForward.Keep, SPEC.md §10.5). WireGuard drops anything whose
// source isn't the sending server's address, so a visitor's address can't
// cross Roostack's network as it is; inside the tunnel, WireGuard sees only
// the two servers' addresses. Connections that arrive through a tunnel are
// marked with its number, and their answers go back through it by a
// routing table of its own (docs/ASSUMPTIONS.md Q15).
type Tunnel struct {
	// Local is this server's address on Roostack's network, Remote the
	// other server's.
	Local  string `json:"local"`
	Remote string `json:"remote"`
}

// tunnelBase is the first tunnel's mark and routing table: one for each
// address in Roostack's servers' range, which has 2^15 of them.
const (
	tunnelBase     = 0x52000000
	tunnelMask     = 0xffff8000
	tunnelRulePref = 5200
	tunnelMTU      = 1400 // WireGuard's 1420, less IP-in-IP's 20
	tunnelPrefix   = "roostack-t"
)

// offset is the remote's place in the servers' range.
func (t Tunnel) offset() uint32 {
	a := netip.MustParseAddr(t.Remote).As4()
	b := trust.ServersRange.Addr().As4()
	return (uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])) -
		(uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]))
}

// Name is its interface's, e.g. roostack-t1 to 100.96.0.1 (at most 14
// characters: the servers' range has 2^15 addresses).
func (t Tunnel) Name() string { return fmt.Sprintf("%s%x", tunnelPrefix, t.offset()) }

// Mark is its connections' mark, and its routing table's number.
func (t Tunnel) Mark() uint32 { return tunnelBase + t.offset() }

func (t Tunnel) validate() error {
	l, err1 := netip.ParseAddr(t.Local)
	r, err2 := netip.ParseAddr(t.Remote)
	if err1 != nil || err2 != nil || !l.Is4() || !r.Is4() || !trust.ServersRange.Contains(l) || !trust.ServersRange.Contains(r) || l == r {
		return errors.New("local and remote must be two servers' addresses on Roostack's network")
	}
	return nil
}

// tunnelTo is the tunnel to addr, if there's one.
func (f Firewall) tunnelTo(addr string) (Tunnel, bool) {
	i := slices.IndexFunc(f.Tunnels, func(t Tunnel) bool { return t.Remote == addr })
	if i < 0 {
		return Tunnel{}, false
	}
	return f.Tunnels[i], true
}

// tunnelsFrom are the names of the tunnels to these addresses, as an
// nftables set ("" if none).
func (f Firewall) tunnelsFrom(addrs ...string) string {
	var names []string
	for _, a := range addrs {
		if t, ok := f.tunnelTo(a); ok {
			names = append(names, strconv.Quote(t.Name()))
		}
	}
	if len(names) == 0 {
		return ""
	}
	return "{ " + strings.Join(names, ", ") + " }"
}

// validateTunnels checks the tunnels and what goes through them.
func (f Firewall) validateTunnels() []error {
	var errs []error
	local := ""
	for i, t := range f.Tunnels {
		if err := t.validate(); err != nil {
			errs = append(errs, fmt.Errorf("tunnels[%d]: %w", i, err))
			continue
		}
		if local != "" && t.Local != local {
			errs = append(errs, fmt.Errorf("tunnels[%d]: every tunnel starts at this server's one address", i))
		}
		local = t.Local
		if slices.ContainsFunc(f.Tunnels[:i], func(o Tunnel) bool { return o.Remote == t.Remote }) {
			errs = append(errs, fmt.Errorf("tunnels[%d]: listed twice", i))
		}
	}
	for i, p := range f.PortForwards {
		if !p.Keep {
			continue
		}
		if _, ok := f.tunnelTo(p.To); !ok {
			errs = append(errs, fmt.Errorf("port_forwards[%d]: keeps addresses, but there's no tunnel to %s", i, p.To))
		}
		if p.FromEdge && f.tunnelsFrom(f.Edges...) == "" {
			errs = append(errs, fmt.Errorf("port_forwards[%d]: keeps addresses from the edges, but there's no tunnel from one", i))
		}
	}
	for i, r := range f.Allow {
		if !r.Kept {
			continue
		}
		switch {
		case r.For == trust.Edge && f.tunnelsFrom(f.Edges...) == "",
			r.For == trust.Controller && f.tunnelsFrom(f.Controller) == "":
			errs = append(errs, fmt.Errorf("allow[%d]: kept, but there's no tunnel from %s", i, r.For))
		case r.For != trust.Edge && r.For != trust.Controller:
			errs = append(errs, fmt.Errorf("allow[%d]: only a port from the edges or the controller is kept", i))
		}
	}
	return errs
}

// keptRules writes the input rules for what arrives through the tunnels
// (Rule.Kept): those ports, and nothing else. Whatever its source says, a
// tunnel's traffic is never home: settled here, before any rule by
// source.
func (f Firewall) keptRules(b *strings.Builder) {
	if len(f.Tunnels) == 0 {
		return
	}
	for _, t := range f.Tunnels {
		fmt.Fprintf(b, "\t\t# tunnel to %s, for public ports that keep visitors' addresses\n", t.Remote)
		// IP-in-IP is protocol 4 (nft's "ipip" is 94, another protocol).
		fmt.Fprintf(b, "\t\tiifname %q ip saddr %s ip protocol 4 accept\n", WireGuardInterface, t.Remote)
	}
	for _, r := range f.Allow {
		if !r.Kept {
			continue
		}
		from := f.tunnelsFrom(f.Edges...)
		if r.For == trust.Controller {
			from = f.tunnelsFrom(f.Controller)
		}
		fmt.Fprintf(b, "\t\t# %s, keeping visitors' addresses\n", r.Reason)
		fmt.Fprintf(b, "\t\tiifname %s %s dport %d accept\n", from, r.Proto, r.Port)
	}
	fmt.Fprintf(b, "\t\tiifname %q drop\n", tunnelPrefix+"*")
}

// keptForwards writes the forward rules for the public ports sent on
// through a tunnel, and their answers; nothing else passes to or from a
// tunnel.
func (f Firewall) keptForwards(b *strings.Builder) {
	if len(f.Tunnels) == 0 {
		return
	}
	for _, p := range f.PortForwards {
		if t, ok := f.tunnelTo(p.To); ok && p.Keep {
			fmt.Fprintf(b, "\t\t# %s: sent on to %s keeping visitors' addresses, and its answers\n", p.Reason, p.To)
			fmt.Fprintf(b, "\t\toifname %q ip daddr %s %s dport %d ct status dnat accept\n", t.Name(), p.To, p.Proto, p.Port)
			fmt.Fprintf(b, "\t\tiifname %q ip saddr %s %s sport %d ct status dnat accept\n", t.Name(), p.To, p.Proto, p.Port)
		}
	}
	fmt.Fprintf(b, "\t\tiifname %q drop\n", tunnelPrefix+"*")
	fmt.Fprintf(b, "\t\toifname %q drop\n", tunnelPrefix+"*")
}

// tunnelMarks writes the chains that send answers back the way their
// connections came: a connection that arrives through a tunnel is marked
// with the tunnel's number, its answers carry the mark, and a routing
// rule sends marked packets to the tunnel's table, whose one route is
// the tunnel (ApplyTunnels). One sent on through a tunnel is marked with
// that tunnel's number on the way. After dstnat, so a connection sent on
// is marked by where it's going.
func (f Firewall) tunnelMarks(b *strings.Builder) {
	if len(f.Tunnels) == 0 {
		return
	}
	ours := fmt.Sprintf("ct mark and %#x == %#x", tunnelMask, tunnelBase)
	var arrive []string
	for _, t := range f.Tunnels {
		arrive = append(arrive, fmt.Sprintf("%q : %#x", t.Name(), t.Mark()))
	}
	var onward []string
	for _, p := range f.PortForwards {
		if t, ok := f.tunnelTo(p.To); ok && p.Keep {
			onward = append(onward, fmt.Sprintf("%s . %s . %d : %#x", p.To, p.Proto, p.Port, t.Mark()))
		}
	}
	b.WriteString("\n\tchain tunnel_marks {\n")
	b.WriteString("\t\ttype filter hook prerouting priority dstnat + 10; policy accept;\n\n")
	b.WriteString("\t\t# connections through a tunnel: marked with its number, and their answers go back through it\n")
	fmt.Fprintf(b, "\t\tiifname %q ct state new ct mark set iifname map { %s }\n", tunnelPrefix+"*", strings.Join(arrive, ", "))
	fmt.Fprintf(b, "\t\tct direction reply %s meta mark set ct mark\n", ours)
	if len(onward) > 0 {
		b.WriteString("\t\t# public ports sent on through a tunnel, keeping visitors' addresses\n")
		fmt.Fprintf(b, "\t\tct direction original ct status dnat meta mark set ip daddr . meta l4proto . th dport map { %s }\n",
			strings.Join(onward, ", "))
	}
	b.WriteString("\t}\n")
	// Answers made on this server (a service's) are routed again once
	// marked.
	b.WriteString("\n\tchain tunnel_answers {\n")
	b.WriteString("\t\ttype route hook output priority mangle; policy accept;\n\n")
	fmt.Fprintf(b, "\t\tct direction reply %s meta mark set ct mark\n", ours)
	b.WriteString("\t}\n")
}

// tunnelsFile keeps the tunnels, which aren't kept by the system, for the
// agent to make again when it starts (RestoreFirewall).
func (h LinuxHost) tunnelsFile() string {
	return filepath.Join(filepath.Dir(h.FirewallFile), "tunnels.json")
}

// ApplyTunnels makes this server's tunnels (Firewall.Tunnels) and their
// routing, removes others it made, and saves them.
func (h LinuxHost) ApplyTunnels(want []Tunnel) error {
	if err := applyTunnels(want); err != nil {
		return err
	}
	data, err := json.Marshal(want)
	if err != nil {
		return err
	}
	if len(want) == 0 {
		if err := os.Remove(h.tunnelsFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return fsutil.WriteFile(h.tunnelsFile(), data, 0o644)
}

// restoreTunnels makes the saved tunnels again (at the agent's start).
func (h LinuxHost) restoreTunnels() error {
	data, err := os.ReadFile(h.tunnelsFile())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var want []Tunnel
	if err := json.Unmarshal(data, &want); err != nil {
		return err
	}
	for _, t := range want {
		if err := t.validate(); err != nil {
			return fmt.Errorf("%s: %w", h.tunnelsFile(), err)
		}
	}
	return applyTunnels(want)
}

func ip(args ...string) error {
	if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func applyTunnels(want []Tunnel) error {
	if len(want) > 0 {
		// Strict reverse-path filtering for the whole server outweighs the
		// tunnels' own setting, and would drop every visitor's packet:
		// their addresses are routed by the default route, not a tunnel.
		if v, err := os.ReadFile("/proc/sys/net/ipv4/conf/all/rp_filter"); err == nil && strings.TrimSpace(string(v)) == "1" {
			return refuse("reverse-path filtering is strict on this server (net.ipv4.conf.all.rp_filter = 1), " +
				"so visitors' connections can't come through Roostack's network keeping their addresses; set it to 2 (loose)")
		}
	}
	// Tunnels made before and not wanted now go; their routes go with them.
	out, err := exec.Command("ip", "-o", "link", "show", "type", "ipip").Output()
	if err != nil {
		return fmt.Errorf("listing tunnels: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimSuffix(f[1], ":"), "@")
		if strings.HasPrefix(name, tunnelPrefix) && !slices.ContainsFunc(want, func(t Tunnel) bool { return t.Name() == name }) {
			if err := ip("link", "del", name); err != nil {
				return err
			}
		}
	}
	// The routing rules, all made again (a moment without them only
	// delays an answer).
	for exec.Command("ip", "rule", "del", "pref", strconv.Itoa(tunnelRulePref)).Run() == nil {
	}
	for _, t := range want {
		name, mark := t.Name(), strconv.FormatUint(uint64(t.Mark()), 10)
		if _, err := os.Stat("/sys/class/net/" + name); err == nil {
			// Made again if it points elsewhere.
			if err := ip("link", "change", name, "type", "ipip", "local", t.Local, "remote", t.Remote, "ttl", "64"); err != nil {
				return err
			}
		} else if err := ip("link", "add", name, "type", "ipip", "local", t.Local, "remote", t.Remote, "ttl", "64"); err != nil {
			return err
		}
		if err := ip("link", "set", name, "mtu", strconv.Itoa(tunnelMTU), "up"); err != nil {
			return err
		}
		// An address of its own: without one, the reverse-path check drops
		// everything that arrives on it.
		if err := ip("addr", "replace", t.Local+"/32", "dev", name, "noprefixroute"); err != nil {
			return err
		}
		// It forwards what's sent on through it (the firewall decides
		// what), and loosely checks sources.
		for key, v := range map[string]string{"forwarding": "1", "rp_filter": "2"} {
			if err := os.WriteFile("/proc/sys/net/ipv4/conf/"+name+"/"+key, []byte(v), 0o644); err != nil {
				return err
			}
		}
		if err := ip("route", "replace", "default", "dev", name, "table", mark); err != nil {
			return err
		}
		if err := ip("rule", "add", "fwmark", mark, "lookup", mark, "pref", strconv.Itoa(tunnelRulePref)); err != nil {
			return err
		}
	}
	return nil
}
