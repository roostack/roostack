package agent

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// MyLab's WireGuard interfaces, managed with Debian's wg-quick so they are
// easy to inspect:
//   - mylab: the network between a lab's machines (SPEC.md §10.2);
//   - mylab-guests: other labs that store backups here (§10.10);
//   - mylab-st1…9: this lab's links to other labs' storage.
const (
	WireGuardInterface = "mylab"
	GuestsInterface    = "mylab-guests"
	WireGuardPort      = 51820
	GuestsPort         = 51821
)

// OverlayRange holds every machine's address on MyLab's network. It is
// inside 100.64.0.0/10, so the firewall and Caddy treat it as private.
var OverlayRange = netip.MustParsePrefix("100.96.0.0/16")

// GuestRange holds the addresses of links between labs (storage for
// others). Each storing lab picks its own /24 in it at random, so a lab
// with links to several can tell them apart.
var GuestRange = netip.MustParsePrefix("100.100.0.0/14")

var storeLink = regexp.MustCompile(`^mylab-st[1-9]$`)

// ValidInterface reports whether name is one of MyLab's interfaces.
func ValidInterface(name string) bool {
	return name == WireGuardInterface || name == GuestsInterface || storeLink.MatchString(name)
}

// rangeFor is the address range an interface's addresses must be in.
func rangeFor(iface string) netip.Prefix {
	if iface == WireGuardInterface {
		return OverlayRange
	}
	return GuestRange
}

func wireGuardConf(iface string) string { return "/etc/wireguard/" + iface + ".conf" }

// WireGuardConf is the lab network's wg-quick file.
var WireGuardConf = wireGuardConf(WireGuardInterface)

// WireGuard is the desired WireGuard interface. The machine's private key
// stays with the agent (in the config file, readable by root only); the
// mylab service only ever sees the public key.
type WireGuard struct {
	// Interface is which one ("" is mylab).
	Interface string `json:"interface,omitempty"`
	// Address is this machine's overlay address with the network's prefix
	// length, e.g. 100.96.0.2/24.
	Address string `json:"address"`
	// ListenPort is the UDP port; 0 lets the kernel choose (machines that
	// only dial out).
	ListenPort int    `json:"listen_port"`
	Peers      []Peer `json:"peers"`
	// JoinKey is a one-time private key from a join code (base64), used
	// until the machine has registered its own key. Empty keeps (or
	// creates) the machine's own key.
	JoinKey string `json:"join_key,omitempty"`
	// NewKey replaces the machine's key with a fresh one (after joining).
	NewKey bool `json:"new_key,omitempty"`
}

// Peer is another machine.
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
	b.WriteString("# Generated by mylab-agent. Do not edit: changes will be overwritten.\n")
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\nAddress = %s\n", privateKey, wg.Address)
	if wg.ListenPort != 0 {
		fmt.Fprintf(&b, "ListenPort = %d\n", wg.ListenPort)
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

func parseKey(s string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != 32 {
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
