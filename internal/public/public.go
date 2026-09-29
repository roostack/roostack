// Package public is how services become reachable from the internet
// (SPEC.md §6.11): the "entry" public visitors come in through, the home's
// public address, and the check that a public address really works from
// outside.
//
// Only port forwarding exists so far. A cloud server in the lab, and
// later Roostack Cloud, will be other entries: public records point at
// whichever address the entry gives.
package public

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// Entry is how public visitors reach the lab.
type Entry interface {
	// Kind names it for people, e.g. "port forwarding".
	Kind() string
	// Address is the public IPv4 address visitors connect to, which
	// public DNS records point at.
	Address(ctx context.Context) (string, error)
}

// PortForwarding is the home router forwarding ports 80 and 443 to this
// server: visitors connect to the home's public address.
type PortForwarding struct {
	// Lookups find the home's public address, tried in turn (tests
	// replace them). Nil uses OpenDNS, then Google.
	Lookups []func(ctx context.Context) (string, error)
}

// Kind names the entry.
func (PortForwarding) Kind() string { return "port forwarding" }

// Address asks public DNS servers what address this server's queries come
// from: its home's public address. DNS rather than an HTTP "what's my IP"
// site, which would see every check (SPEC.md §6.11).
func (pf PortForwarding) Address(ctx context.Context) (string, error) {
	lookups := pf.Lookups
	if lookups == nil {
		lookups = []func(context.Context) (string, error){OpenDNS, Google}
	}
	var errs []error
	for _, lookup := range lookups {
		ip, err := lookup(ctx)
		if err == nil {
			err = checkPublic(ip)
		}
		if err == nil {
			return ip, nil
		}
		errs = append(errs, err)
	}
	return "", fmt.Errorf("couldn't find your home's public address: %w", errors.Join(errs...))
}

// checkPublic makes sure an answer is a public IPv4 address.
func checkPublic(s string) error {
	a, err := netip.ParseAddr(s)
	switch {
	case err != nil:
		return fmt.Errorf("%q isn't an address", s)
	case !a.Is4():
		return fmt.Errorf("%s isn't an IPv4 address", s)
	case !IsPublic(a):
		return fmt.Errorf("%s isn't a public address", s)
	}
	return nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// IsPublic says whether a is an internet address, not a home, loopback,
// link-local or carrier-grade NAT one: a visitor from it came from
// outside.
func IsPublic(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !cgnat.Contains(a)
}

// OpenDNS asks OpenDNS's resolver for myip.opendns.com, which it answers
// with the address the query came from.
func OpenDNS(ctx context.Context) (string, error) {
	addrs, err := resolverAt("208.67.222.222:53").LookupHost(ctx, "myip.opendns.com")
	if err != nil {
		return "", fmt.Errorf("OpenDNS: %w", err)
	}
	return first(addrs, "OpenDNS")
}

// Google asks Google's name server for o-o.myaddr.l.google.com, a TXT
// record holding the address the query came from.
func Google(ctx context.Context) (string, error) {
	txt, err := resolverAt("216.239.32.10:53").LookupTXT(ctx, "o-o.myaddr.l.google.com")
	if err != nil {
		return "", fmt.Errorf("Google: %w", err)
	}
	return first(txt, "Google")
}

func first(answers []string, who string) (string, error) {
	for _, a := range answers {
		if ip, err := netip.ParseAddr(a); err == nil && ip.Is4() {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("%s gave no IPv4 address (%v)", who, answers)
}

// resolverAt queries one DNS server directly, not the home router.
func resolverAt(server string) *net.Resolver {
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: 5 * time.Second}
		return d.DialContext(ctx, "udp", server)
	}}
}

// CrowdSecUp says whether CrowdSec's local API answers its health check
// (no key needed) at addr, e.g. 127.0.0.1:8473.
func CrowdSecUp(ctx context.Context, addr string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
