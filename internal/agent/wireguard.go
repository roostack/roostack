package agent

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/trust"
)

// Roostack's WireGuard interfaces, managed with Debian's wg-quick so they are
// easy to inspect:
//   - roostack: the network between a lab's servers (SPEC.md §10.2);
//   - roostack-guests: other labs that store backups here (§10.10);
//   - roostack-st1…9: this lab's links to other labs' storage;
//   - roostack-relay: Roostack Cloud's relay, dialled by the lab (§10.5);
//   - roostack-people: the lab's VPN, people's own devices (§10.11).
const (
	WireGuardInterface = trust.LabInterface
	GuestsInterface    = trust.GuestsInterface
	RelayInterface     = trust.RelayInterface
	PeopleInterface    = trust.PeopleInterface
	WireGuardPort      = 51820
	GuestsPort         = 51821
	PeoplePort         = 51822
)

// The addresses on those interfaces (internal/trust).
var (
	OverlayRange = trust.OverlayRange
	GuestRange   = trust.GuestRange
	RelayRange   = trust.RelayRange
	DevicesRange = trust.DevicesRange
)

var storeLink = regexp.MustCompile(`^roostack-st[1-9]$`)

// ValidInterface reports whether name is one of Roostack's interfaces.
func ValidInterface(name string) bool {
	return name == WireGuardInterface || name == GuestsInterface || name == RelayInterface || name == PeopleInterface ||
		storeLink.MatchString(name)
}

// rangeFor is the address range an interface's addresses must be in.
func rangeFor(iface string) netip.Prefix {
	switch iface {
	case WireGuardInterface:
		return OverlayRange
	case RelayInterface:
		return RelayRange
	case PeopleInterface:
		return DevicesRange
	}
	return GuestRange
}

func wireGuardConf(iface string) string { return "/etc/wireguard/" + iface + ".conf" }

// WireGuardConf is the lab network's wg-quick file.
var WireGuardConf = wireGuardConf(WireGuardInterface)

// WireGuard is the desired WireGuard interface. The server's private key
// is in the config file, readable by root only, and in the copy the
// roostack service keeps for its backups (WireGuardKeys, saved 0600 as
// backup.WireGuardKeysFile, the roostack user's), so a restored lab keeps
// the keys its peers know.
type WireGuard struct {
	// Interface is which one ("" is roostack).
	Interface string `json:"interface,omitempty"`
	// Address is this server's overlay address with the network's prefix
	// length, e.g. 100.96.0.2/24.
	Address string `json:"address"`
	// ListenPort is the UDP port; 0 lets the kernel choose (servers that
	// only dial out).
	ListenPort int    `json:"listen_port"`
	Peers      []Peer `json:"peers"`
	// JoinKey is a one-time private key from a join code (base64), used
	// until the server has registered its own key. Empty keeps (or
	// creates) the server's own key.
	JoinKey string `json:"join_key,omitempty"`
	// NewKey replaces the server's key with a fresh one (after joining).
	NewKey bool `json:"new_key,omitempty"`
	// SavedKey is the interface's key from a backup (WireGuardKeys): used
	// only if the interface has no key yet, so a lab restored onto a new
	// server keeps the keys its peers know.
	SavedKey string `json:"saved_key,omitempty"`
	// Down keeps the interface's file, and so its key, but stops it: the
	// lab's VPN turned off, so that turning it on again keeps the
	// settings people's devices have.
	Down bool `json:"down,omitempty"`
	// Forward turns on forwarding for packets arriving on this interface
	// (the hub's, between servers; the lab's VPN's, for devices whose
	// traffic goes on). The firewall's forward chain decides what may
	// pass.
	Forward bool `json:"forward,omitempty"`
}

// Peer is another server.
type Peer struct {
	PublicKey  string   `json:"public_key"`
	AllowedIPs []string `json:"allowed_ips"`
	// Endpoint is host:port to dial; empty waits for the peer to connect.
	Endpoint string `json:"endpoint,omitempty"`
	// Keepalive (seconds) keeps NAT mappings open when dialling out.
	Keepalive int `json:"keepalive,omitempty"`
}

// WireGuardStatus is the interface as configured.
type WireGuardStatus struct {
	Configured bool   `json:"configured"`
	PublicKey  string `json:"public_key"`
	Address    string `json:"address"`
	// Handshakes is when each peer (by public key) last connected, as a
	// Unix time (0: never).
	Handshakes map[string]int64 `json:"handshakes,omitempty"`
	// Endpoints is where each peer last connected from (host:port).
	Endpoints map[string]string `json:"endpoints,omitempty"`
}

// Iface is the interface's name.
func (wg WireGuard) Iface() string {
	if wg.Interface == "" {
		return WireGuardInterface
	}
	return wg.Interface
}

// parseHandshakes reads `wg show <iface> latest-handshakes`.
func parseHandshakes(out string) map[string]int64 {
	hs := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			if t, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				hs[f[0]] = t
			}
		}
	}
	return hs
}

var endpointHost = regexp.MustCompile(`^[a-zA-Z0-9.-]{1,253}$`)

// Validate checks the request. Every value ends up in a config file, so
// nothing free-form gets through.
func (wg WireGuard) Validate() error {
	var errs []error
	if !ValidInterface(wg.Iface()) {
		errs = append(errs, fmt.Errorf("invalid interface %q", wg.Interface))
	}
	allowed := rangeFor(wg.Iface())
	if p, err := netip.ParsePrefix(wg.Address); err != nil || !allowed.Contains(p.Addr()) || p.Bits() < allowed.Bits() {
		errs = append(errs, fmt.Errorf("address %q must be inside %s", wg.Address, allowed))
	}
	if wg.Forward && wg.Iface() != WireGuardInterface && wg.Iface() != PeopleInterface {
		errs = append(errs, errors.New("only Roostack's network and the lab's VPN forward"))
	}
	if wg.ListenPort < 0 || wg.ListenPort > 65535 {
		errs = append(errs, fmt.Errorf("invalid listen port %d", wg.ListenPort))
	}
	if wg.JoinKey != "" {
		if _, err := parseKey(wg.JoinKey); err != nil {
			errs = append(errs, fmt.Errorf("join key: %w", err))
		}
		if wg.NewKey {
			errs = append(errs, errors.New("join_key and new_key can't both be set"))
		}
	}
	if wg.SavedKey != "" {
		if _, err := parseKey(wg.SavedKey); err != nil {
			errs = append(errs, fmt.Errorf("saved key: %w", err))
		}
	}
	for i, p := range wg.Peers {
		if _, err := parseKey(p.PublicKey); err != nil {
			errs = append(errs, fmt.Errorf("peers[%d]: public key: %w", i, err))
		}
		if len(p.AllowedIPs) == 0 {
			errs = append(errs, fmt.Errorf("peers[%d]: no allowed IPs", i))
		}
		for _, a := range p.AllowedIPs {
			if pr, err := netip.ParsePrefix(a); err != nil || !allowed.Contains(pr.Addr()) || pr.Bits() < allowed.Bits() {
				errs = append(errs, fmt.Errorf("peers[%d]: allowed IP %q must be inside %s", i, a, allowed))
			}
		}
		if p.Endpoint != "" {
			host, port, ok := strings.Cut(p.Endpoint, ":")
			n, err := strconv.Atoi(port)
			if !ok || err != nil || n < 1 || n > 65535 || !endpointHost.MatchString(host) {
				errs = append(errs, fmt.Errorf("peers[%d]: endpoint %q must be host:port", i, p.Endpoint))
			}
		}
		if p.Keepalive < 0 || p.Keepalive > 3600 {
			errs = append(errs, fmt.Errorf("peers[%d]: invalid keepalive", i))
		}
	}
	return errors.Join(errs...)
}

// Config renders the wg-quick file for a private key.
func (wg WireGuard) Config(privateKey string) string {
	var b strings.Builder
	b.WriteString("# Generated by roostack-agent. Do not edit: changes will be overwritten.\n")
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\nAddress = %s\n", privateKey, wg.Address)
	if wg.ListenPort != 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", wg.ListenPort)
	}
	if wg.Forward {
		// This interface only, not the whole machine (and again at boot).
		b.WriteString(forwardUp + "\n")
	}
	for _, p := range wg.Peers {
		fmt.Fprintf(&b, "\n[Peer]\nPublicKey = %s\nAllowedIPs = %s\n", p.PublicKey, strings.Join(p.AllowedIPs, ", "))
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
		if p.Keepalive != 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.Keepalive)
		}
	}
	return b.String()
}

// NewWireGuardKey returns a new private key (base64, as wg uses).
func NewWireGuardKey() (string, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.Bytes()), nil
}

// WireGuardPublicKey derives the public key from a private key.
func WireGuardPublicKey(private string) (string, error) {
	raw, err := parseKey(private)
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// parseKey decodes a WireGuard key: 32 bytes, base64, written exactly as
// base64 writes them. The decoder skips newlines, which would otherwise
// reach a wg-quick file inside a key.
func parseKey(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != 32 || base64.StdEncoding.EncodeToString(raw) != s {
		return nil, errors.New("must be 32 bytes, base64")
	}
	return raw, nil
}

// confKey reads the private key from a wg-quick file, or "".
func confKey(conf string) string {
	for _, line := range strings.Split(conf, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "PrivateKey" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// confAddress reads the interface address from a wg-quick file, or "".
func confAddress(conf string) string {
	for _, line := range strings.Split(conf, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "Address" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// WireGuardKeys reads the private keys of Roostack's WireGuard interfaces
// that have one, by interface, so they can be backed up with the rest of
// the lab (SPEC.md §10.3).
func WireGuardKeys() (map[string]string, error) {
	files, err := filepath.Glob("/etc/wireguard/roostack*.conf")
	if err != nil {
		return nil, err
	}
	keys := map[string]string{}
	for _, f := range files {
		iface := strings.TrimSuffix(filepath.Base(f), ".conf")
		if !ValidInterface(iface) {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		if k := confKey(string(data)); k != "" {
			keys[iface] = k
		}
	}
	return keys, nil
}

// Links are a server's direct links to other servers on Roostack's
// network, for backups to their disks (SPEC.md §10.10). They're set apart
// from its link to the controller: in the interface's file each is marked
// with LinkMarker, and only marked peers change.
type Links struct {
	// Listen: other servers dial this one directly, on WireGuardPort.
	Listen bool   `json:"listen"`
	Peers  []Peer `json:"peers"`
	// ToController are more addresses sent to the controller over its link
	// (besides the lab's servers', which it carries already): on the server
	// the lab's VPN's devices connect to, the lab's address and the home's
	// networks, which devices reach through the controller (SPEC.md
	// §10.11).
	ToController []string `json:"to_controller,omitempty"`
	// PassesOn: this is the server the lab's VPN's devices connect to, and
	// it passes them on to the controller, so Roostack's network's
	// interface forwards the controller's answers back to them: now, and
	// whenever it comes up (a line in its file, marked with passesOnMark).
	PassesOn bool `json:"passes_on,omitempty"`
}

// forwardUp is the wg-quick line that turns forwarding on for an
// interface as it comes up (this interface only, not the whole machine),
// and passesOnMark marks the one Links.PassesOn adds.
const (
	forwardUp    = "PostUp = echo 1 > /proc/sys/net/ipv4/conf/%i/forwarding"
	passesOnMark = "# roostack vpn"
)

// LinkMarker comes before each direct link's [Peer] section.
const LinkMarker = "# roostack link"

// Validate checks the request: each link is one other server (a single
// address on Roostack's network).
func (l Links) Validate() error {
	var errs []error
	lab := rangeFor(WireGuardInterface)
	seen := map[string]bool{}
	for i, p := range l.Peers {
		if _, err := parseKey(p.PublicKey); err != nil {
			errs = append(errs, fmt.Errorf("peers[%d]: public key: %w", i, err))
		}
		if seen[p.PublicKey] {
			errs = append(errs, fmt.Errorf("peers[%d]: listed twice", i))
		}
		seen[p.PublicKey] = true
		if len(p.AllowedIPs) != 1 {
			errs = append(errs, fmt.Errorf("peers[%d]: a link is one server's address", i))
		}
		for _, a := range p.AllowedIPs {
			if pr, err := netip.ParsePrefix(a); err != nil || !lab.Contains(pr.Addr()) || pr.Bits() != 32 {
				errs = append(errs, fmt.Errorf("peers[%d]: allowed IP %q must be one address inside %s", i, a, lab))
			}
		}
		if p.Endpoint != "" {
			host, port, ok := strings.Cut(p.Endpoint, ":")
			n, err := strconv.Atoi(port)
			if !ok || err != nil || n < 1 || n > 65535 || !endpointHost.MatchString(host) {
				errs = append(errs, fmt.Errorf("peers[%d]: endpoint %q must be host:port", i, p.Endpoint))
			}
		}
		if p.Keepalive < 0 || p.Keepalive > 3600 {
			errs = append(errs, fmt.Errorf("peers[%d]: invalid keepalive", i))
		}
	}
	for i, a := range l.ToController {
		if !validPrivate(a) && a != trust.DevicesRange.String() {
			errs = append(errs, fmt.Errorf("to_controller[%d]: %q must be a private network or the lab's VPN", i, a))
		}
	}
	return errors.Join(errs...)
}

// withLinks rewrites a wg-quick file with these links: the marked peers
// are replaced, everything else stays as it was, and ListenPort is set (or
// removed) as asked. A link may not reuse the key of a peer that isn't
// one (the link to the controller).
func (l Links) apply(conf string) (string, error) {
	var keep []string // sections, as text
	var section []string
	marked := false
	flush := func() {
		if len(section) > 0 && !marked {
			keep = append(keep, strings.Join(section, "\n"))
		}
		section, marked = nil, false
	}
	pending := false // the marker line was just seen
	for _, line := range strings.Split(conf, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == LinkMarker:
			flush()
			pending = true
			continue
		case strings.HasPrefix(t, "["):
			if !pending {
				flush()
			}
			marked, pending = pending, false
		}
		if strings.HasPrefix(t, "ListenPort") && strings.Contains(t, "=") || strings.HasSuffix(t, passesOnMark) {
			continue
		}
		section = append(section, line)
	}
	flush()
	out := strings.TrimRight(strings.Join(keep, "\n"), "\n")
	out = l.controllerAllowed(out)
	for _, p := range l.Peers {
		if strings.Contains(out, "PublicKey = "+p.PublicKey) {
			return "", errors.New("a link can't take over another peer's key")
		}
	}
	if l.PassesOn {
		out = strings.Replace(out, "[Interface]\n", "[Interface]\n"+forwardUp+" "+passesOnMark+"\n", 1)
	}
	if l.Listen {
		out = strings.Replace(out, "[Interface]\n", fmt.Sprintf("[Interface]\nListenPort = %d\n", WireGuardPort), 1)
	}
	var b strings.Builder
	b.WriteString(out + "\n")
	for _, p := range l.Peers {
		fmt.Fprintf(&b, "\n%s\n[Peer]\nPublicKey = %s\nAllowedIPs = %s\n", LinkMarker, p.PublicKey, strings.Join(p.AllowedIPs, ", "))
		if p.Endpoint != "" {
			fmt.Fprintf(&b, "Endpoint = %s\n", p.Endpoint)
		}
		if p.Keepalive != 0 {
			fmt.Fprintf(&b, "PersistentKeepalive = %d\n", p.Keepalive)
		}
	}
	return b.String(), nil
}

// parseEndpoints reads `wg show <iface> endpoints`: where each peer last
// connected from (host:port), by public key.
func parseEndpoints(out string) map[string]string {
	eps := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] != "(none)" {
			eps[f[0]] = f[1]
		}
	}
	return eps
}

// WireGuardKeys reads Roostack's interfaces' private keys.
func (h LinuxHost) WireGuardKeys() (map[string]string, error) { return WireGuardKeys() }

// ApplyWireGuard writes an interface's wg-quick file and brings it up. A
// running interface takes new peers without dropping connections (wg
// syncconf); a new address or key restarts it.
func (h LinuxHost) ApplyWireGuard(wg WireGuard) (string, error) {
	iface := wg.Iface()
	file := wireGuardConf(iface)
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Not "no key yet": a new key would cut off everyone who knows
		// this one.
		return "", fmt.Errorf("reading %s: %w", file, err)
	}
	key := confKey(string(old))
	switch {
	case wg.JoinKey != "":
		key = wg.JoinKey
	case key == "" && wg.SavedKey != "" && !wg.NewKey:
		key = wg.SavedKey
	case key == "" || wg.NewKey:
		var err error
		if key, err = NewWireGuardKey(); err != nil {
			return "", err
		}
	}
	pub, err := WireGuardPublicKey(key)
	if err != nil {
		return "", err
	}
	conf := wg.Config(key)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return "", err
	}
	if err := fsutil.WritePrivate(file, []byte(conf)); err != nil {
		return "", err
	}
	unit := "wg-quick@" + iface
	if wg.Down {
		if out, err := exec.Command("systemctl", "disable", "--now", unit).CombinedOutput(); err != nil {
			return "", fmt.Errorf("stopping %s: %w: %s", unit, err, out)
		}
		return pub, nil
	}
	if out, err := exec.Command("systemctl", "enable", unit).CombinedOutput(); err != nil {
		return "", fmt.Errorf("enabling %s: %w: %s", unit, err, out)
	}
	// By the running interface, not the file it was started from: an
	// earlier apply may have written the file and then failed to restart.
	if sameInterface(iface, pub, wg.Address) {
		if err := syncConf(iface); err != nil {
			return "", err
		}
		// wg syncconf leaves routes alone, which wg-quick sets only as the
		// interface comes up.
		if err := syncRoutes(iface); err != nil {
			return "", err
		}
	} else if out, err := exec.Command("systemctl", "restart", unit).CombinedOutput(); err != nil {
		return "", fmt.Errorf("starting %s: %w: %s", unit, err, out)
	}
	return pub, setForwarding(iface, wg.Forward)
}

// uplinkSaved is where an interface's own forwarding setting is kept while
// the lab's VPN turns it on: in the agent's own folder in /run
// (RuntimeDirectory), which the agent's restarts keep and a reboot clears,
// as it clears the setting.
var uplinkSaved = "/run/roostack-agent/forwarding-"

// procNet is where the kernel's IPv4 settings for each interface are.
var procNet = "/proc/sys/net/ipv4/conf/"

// forwardsEverywhere reports whether forwarding is on for the whole
// machine (net.ipv4.ip_forward), which Roostack never sets: something else
// on this server routes, such as Docker or a host for virtual machines.
func forwardsEverywhere() bool {
	v, err := os.ReadFile(procNet + "all/forwarding")
	return err == nil && strings.TrimSpace(string(v)) == "1"
}

// vpnForwardingPlan says which interfaces the lab's VPN's traffic comes
// back in on, which need their own forwarding on: an interface only
// forwards what arrives on it if it's on (docs/ASSUMPTIONS.md C15). They're
// the default route's interfaces and those on the home's networks while
// devices' traffic goes on to the internet or the home network (uplinks).
// Own are those whose forwarding was off before Roostack turned it on: on
// them, the firewall lets only answers be forwarded
// (Firewall.ForwardOwned), so the server doesn't become a router for
// anything else there. None are while something else has turned
// forwarding on for the whole machine: its forwarding is left alone.
func vpnForwardingPlan(uplinks bool) (want, own []string) {
	if uplinks {
		want = defaultRouteIfaces()
		for _, iface := range homeIfaces() {
			if !slices.Contains(want, iface) {
				want = append(want, iface)
			}
		}
	}
	return want, ownedOf(want)
}

// ownedOf are the interfaces of want whose forwarding was off before
// Roostack turned it on (vpnForwardingPlan).
func ownedOf(want []string) (own []string) {
	if forwardsEverywhere() {
		return nil
	}
	for _, iface := range want {
		v, err := os.ReadFile(uplinkSaved + iface)
		if err != nil {
			v, err = os.ReadFile(procNet + iface + "/forwarding")
		}
		if err == nil && strings.TrimSpace(string(v)) == "0" {
			own = append(own, iface)
		}
	}
	return own
}

// restoreVPNForwarding puts forwarding back as it was on the interfaces
// Roostack turned it on for that aren't in want any more. Not where
// something else has since turned forwarding on for the whole machine:
// turning it off there would break that.
func restoreVPNForwarding(want []string) error {
	saved, _ := filepath.Glob(uplinkSaved + "*")
	for _, f := range saved {
		iface := strings.TrimPrefix(f, uplinkSaved)
		if slices.Contains(want, iface) || !ValidIfaceName(iface) {
			continue
		}
		if !forwardsEverywhere() {
			v, err := os.ReadFile(f)
			if err == nil {
				err = os.WriteFile(procNet+iface+"/forwarding", v, 0o644)
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// setVPNForwarding turns forwarding on for want (vpnForwardingPlan),
// keeping each one's own setting first. One interface that has gone
// doesn't stop the others.
func setVPNForwarding(want []string) error {
	var errs []error
	for _, iface := range want {
		proc := procNet + iface + "/forwarding"
		v, err := os.ReadFile(proc)
		if errors.Is(err, os.ErrNotExist) {
			continue // unplugged since the plan
		}
		if err == nil {
			if _, statErr := os.Stat(uplinkSaved + iface); errors.Is(statErr, os.ErrNotExist) {
				err = os.WriteFile(uplinkSaved+iface, v, 0o600)
			}
		}
		if err == nil {
			err = os.WriteFile(proc, []byte("1\n"), 0o644)
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// homeIfaces are the interfaces with a private IPv4 address, apart from
// Roostack's WireGuard and container and VM bridges: the home's networks
// (as labnet.HomeAddresses finds them).
func homeIfaces() []string {
	ifaces, _ := net.Interfaces()
	var out []string
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || !ValidIfaceName(ifc.Name) ||
			slices.ContainsFunc([]string{"roostack", "wg", "podman", "veth", "docker", "br-", "cni", "virbr", "tun", "tap"},
				func(p string) bool { return strings.HasPrefix(ifc.Name, p) }) {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsPrivate() {
				out = append(out, ifc.Name)
				break
			}
		}
	}
	return out
}

// defaultRouteIfaces are the interfaces of the IPv4 default routes, from
// /proc/net/route.
func defaultRouteIfaces() []string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) > 2 && f[1] == "00000000" && ValidIfaceName(f[0]) && !slices.Contains(out, f[0]) {
			out = append(out, f[0])
		}
	}
	return out
}

// ValidIfaceName reports whether name can be a network interface's name
// that's safe as a path under /proc and inside quotes in the firewall's
// file: a VLAN's (eth0.100) is, one with a quote or a space isn't.
func ValidIfaceName(name string) bool {
	if name == "" || len(name) > 15 || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsFunc(name, func(c rune) bool {
		return c <= ' ' || c >= 0x7f || strings.ContainsRune(`/"\:;{}`, c)
	})
}

// sameInterface reports whether the running interface has this key and
// address, so that new peers can be given to it as it runs.
func sameInterface(iface, publicKey, address string) bool {
	want, err := netip.ParsePrefix(address)
	if err != nil {
		return false
	}
	pub, err := exec.Command("wg", "show", iface, "public-key").Output()
	if err != nil || strings.TrimSpace(string(pub)) != publicKey {
		return false
	}
	out, err := exec.Command("ip", "-4", "-o", "addr", "show", "dev", iface).Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if i := slices.Index(f, "inet"); i >= 0 && i+1 < len(f) {
			if have, err := netip.ParsePrefix(f[i+1]); err == nil && have == want {
				return true
			}
		}
	}
	return false
}

// syncConf applies an interface's file to the running interface, without
// taking it down.
func syncConf(iface string) error {
	// wg syncconf takes the file without wg-quick's extra keys.
	strip, err := exec.Command("wg-quick", "strip", iface).Output()
	if err != nil {
		return fmt.Errorf("wg-quick strip: %w", err)
	}
	cmd := exec.Command("wg", "syncconf", iface, "/dev/stdin")
	cmd.Stdin = strings.NewReader(string(strip))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wg syncconf: %w: %s", err, out)
	}
	return nil
}

// ApplyLinks sets this server's direct links to other servers (Links),
// keeping its link to the controller.
func (h LinuxHost) ApplyLinks(l Links) error {
	file := wireGuardConf(WireGuardInterface)
	old, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("this server isn't in a lab")
	}
	if err != nil {
		return err
	}
	conf, err := l.apply(string(old))
	if err != nil {
		return err
	}
	// Before anything is written: wg-quick routes these as the interface
	// comes up, without asking, so a file that names a network this server
	// already routes elsewhere would take it over at the next restart.
	for _, a := range l.ToController {
		if p, err := netip.ParsePrefix(a); err == nil {
			if via := routedElsewhere(p, WireGuardInterface); via != "" {
				return alreadyRouted(p, via, WireGuardInterface)
			}
		}
	}
	_, statErr := os.Stat("/sys/class/net/" + WireGuardInterface)
	up := statErr == nil
	if conf != string(old) {
		if err := fsutil.WritePrivate(file, []byte(conf)); err != nil {
			return err
		}
		if up {
			if err := syncConf(WireGuardInterface); err != nil {
				return err
			}
			if err := syncRoutes(WireGuardInterface); err != nil {
				return err
			}
		}
	}
	if !up {
		return nil // it comes up with them
	}
	// Passing the VPN's devices on: forwarding now too, and off again once
	// this server no longer does (never the hub's own, which has no mark).
	switch {
	case l.PassesOn:
		return setForwarding(WireGuardInterface, true)
	case strings.Contains(string(old), passesOnMark):
		return setForwarding(WireGuardInterface, false)
	}
	return nil
}

// routedElsewhere says how this server already routes exactly p through
// another interface ("" if it doesn't).
func routedElsewhere(p netip.Prefix, iface string) string {
	out, _ := exec.Command("ip", "-4", "route", "show", "exact", p.Masked().String()).Output()
	var other []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(line); len(f) > 0 && !slices.Contains(f, iface) {
			other = append(other, strings.TrimSpace(line))
		}
	}
	return strings.Join(other, "; ")
}

func alreadyRouted(p netip.Prefix, via, iface string) error {
	return fmt.Errorf("%s is already routed elsewhere on this server (%s), so Roostack won't route it through %s", p, via, iface)
}

// setForwarding turns forwarding of packets arriving on an interface on
// or off now (wg-quick's PostUp does it at boot).
func setForwarding(iface string, on bool) error {
	v := "0\n"
	if on {
		v = "1\n"
	}
	return os.WriteFile(procNet+iface+"/forwarding", []byte(v), 0o644)
}

// RemoveWireGuard stops and disables an interface and deletes its file.
func (h LinuxHost) RemoveWireGuard(iface string) error {
	unit := "wg-quick@" + iface
	if out, err := exec.Command("systemctl", "disable", "--now", unit).CombinedOutput(); err != nil {
		return fmt.Errorf("stopping %s: %w: %s", unit, err, out)
	}
	if err := os.Remove(wireGuardConf(iface)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// WireGuardStatus reads an interface's wg-quick file and when each peer
// last connected.
func (h LinuxHost) WireGuardStatus(iface string) (WireGuardStatus, error) {
	file := wireGuardConf(iface)
	conf, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return WireGuardStatus{}, nil
	}
	if err != nil {
		return WireGuardStatus{}, err
	}
	pub, err := WireGuardPublicKey(confKey(string(conf)))
	if err != nil {
		return WireGuardStatus{}, fmt.Errorf("%s: %w", file, err)
	}
	st := WireGuardStatus{Configured: true, PublicKey: pub, Address: confAddress(string(conf)), Handshakes: map[string]int64{}}
	if out, err := exec.Command("wg", "show", iface, "latest-handshakes").Output(); err == nil {
		st.Handshakes = parseHandshakes(string(out))
	}
	if out, err := exec.Command("wg", "show", iface, "endpoints").Output(); err == nil {
		st.Endpoints = parseEndpoints(string(out))
	}
	return st, nil
}

// controllerAllowed sets the addresses the link to the controller carries
// (the peer that isn't a direct link, in conf without them): the lab's
// servers' it had (the controller's, or their network's), and
// ToController.
func (l Links) controllerAllowed(conf string) string {
	lines := strings.Split(conf, "\n")
	for i, line := range lines {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "AllowedIPs" {
			continue
		}
		var keep []string
		for _, a := range strings.Split(v, ",") {
			a = strings.TrimSpace(a)
			if p, err := netip.ParsePrefix(a); err == nil && trust.ServersRange.Contains(p.Addr()) && p.Bits() >= trust.ServersRange.Bits() {
				keep = append(keep, a)
			}
		}
		lines[i] = "AllowedIPs = " + strings.Join(append(keep, l.ToController...), ", ")
	}
	return strings.Join(lines, "\n")
}

// validPrivate reports whether a prefix is inside the home network's
// private ranges, never everything.
func validPrivate(s string) bool {
	p, err := netip.ParsePrefix(s)
	if err != nil || p != p.Masked() || !p.Addr().Is4() || p.Bits() < 8 {
		return false
	}
	ranges := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16")}
	for _, r := range ranges {
		if r.Bits() <= p.Bits() && r.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// syncRoutes routes the addresses an interface's peers carry outside its
// own network through it, and takes away routes through it to anything
// they no longer carry (the kernel's route to its own network stays).
func syncRoutes(iface string) error {
	conf, err := os.ReadFile(wireGuardConf(iface))
	if err != nil {
		return err
	}
	own, _ := netip.ParsePrefix(confAddress(string(conf)))
	want := map[netip.Prefix]bool{}
	for _, line := range strings.Split(string(conf), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "AllowedIPs" {
			continue
		}
		for _, a := range strings.Split(v, ",") {
			p, err := netip.ParsePrefix(strings.TrimSpace(a))
			if err != nil || !p.Addr().Is4() || own.IsValid() && own.Bits() <= p.Bits() && own.Masked().Contains(p.Addr()) {
				continue
			}
			want[p.Masked()] = true
		}
	}
	out, err := exec.Command("ip", "-4", "route", "show", "dev", iface).Output()
	if err != nil {
		return fmt.Errorf("listing %s's routes: %w", iface, err)
	}
	have := map[netip.Prefix]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 || strings.Contains(line, "proto kernel") {
			continue
		}
		p, err := netip.ParsePrefix(f[0])
		if err != nil {
			a, err2 := netip.ParseAddr(f[0])
			if err2 != nil {
				continue
			}
			p = netip.PrefixFrom(a, 32)
		}
		have[p] = true
		if !want[p] {
			if out, err := exec.Command("ip", "route", "del", p.String(), "dev", iface).CombinedOutput(); err != nil {
				return fmt.Errorf("removing the route to %s: %w: %s", p, err, out)
			}
		}
	}
	for p := range want {
		if !have[p] {
			// Never take over a route through another interface, such as a
			// cloud server's own network matching the home's.
			if via := routedElsewhere(p, iface); via != "" {
				return alreadyRouted(p, via, iface)
			}
			if out, err := exec.Command("ip", "route", "add", p.String(), "dev", iface).CombinedOutput(); err != nil {
				return fmt.Errorf("routing %s through %s: %w: %s", p, iface, err, out)
			}
		}
	}
	return nil
}
