package agent

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/trust"
)

// Roostack's WireGuard interfaces, managed with Debian's wg-quick so they are
// easy to inspect:
//   - roostack: the network between a lab's servers (SPEC.md §10.2);
//   - roostack-guests: other labs that store backups here (§10.10);
//   - roostack-st1…9: this lab's links to other labs' storage;
//   - roostack-relay: Roostack Cloud's relay, dialled by the lab (§10.5).
const (
	WireGuardInterface = trust.LabInterface
	GuestsInterface    = trust.GuestsInterface
	RelayInterface     = trust.RelayInterface
	WireGuardPort      = 51820
	GuestsPort         = 51821
)

// The addresses on those interfaces (internal/trust).
var (
	OverlayRange = trust.OverlayRange
	GuestRange   = trust.GuestRange
	RelayRange   = trust.RelayRange
)

var storeLink = regexp.MustCompile(`^roostack-st[1-9]$`)

// ValidInterface reports whether name is one of Roostack's interfaces.
func ValidInterface(name string) bool {
	return name == WireGuardInterface || name == GuestsInterface || name == RelayInterface || storeLink.MatchString(name)
}

// rangeFor is the address range an interface's addresses must be in.
func rangeFor(iface string) netip.Prefix {
	switch iface {
	case WireGuardInterface:
		return OverlayRange
	case RelayInterface:
		return RelayRange
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
	// Forward turns on forwarding for packets arriving on this interface
	// (the hub's, between servers; only on Roostack's network). The
	// firewall's forward chain decides what may pass.
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
	if wg.Forward && wg.Iface() != WireGuardInterface {
		errs = append(errs, errors.New("only Roostack's network forwards"))
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
		b.WriteString("PostUp = echo 1 > /proc/sys/net/ipv4/conf/%i/forwarding\n")
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
}

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
		if strings.HasPrefix(t, "ListenPort") && strings.Contains(t, "=") {
			continue
		}
		section = append(section, line)
	}
	flush()
	out := strings.TrimRight(strings.Join(keep, "\n"), "\n")
	for _, p := range l.Peers {
		if strings.Contains(out, "PublicKey = "+p.PublicKey) {
			return "", errors.New("a link can't take over another peer's key")
		}
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
