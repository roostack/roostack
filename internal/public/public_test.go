package public

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
)

func lookup(ip string, err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return ip, err }
}

// The first lookup that gives a public IPv4 address wins; private,
// carrier-grade NAT and IPv6 answers are passed over.
func TestPortForwardingAddress(t *testing.T) {
	for _, c := range []struct {
		lookups []func(context.Context) (string, error)
		want    string
	}{
		{[]func(context.Context) (string, error){lookup("81.2.69.160", nil)}, "81.2.69.160"},
		{[]func(context.Context) (string, error){lookup("", errors.New("timeout")), lookup("81.2.69.160", nil)}, "81.2.69.160"},
		{[]func(context.Context) (string, error){lookup("192.168.1.1", nil), lookup("81.2.69.161", nil)}, "81.2.69.161"},
		{[]func(context.Context) (string, error){lookup("100.64.3.4", nil)}, ""},
		{[]func(context.Context) (string, error){lookup("2001:db8::1", nil)}, ""},
	} {
		got, err := PortForwarding{Lookups: c.lookups}.Address(context.Background())
		if got != c.want || (c.want == "") != (err != nil) {
			t.Errorf("got %q, %v; want %q", got, err, c.want)
		}
	}
}

func TestIsPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"81.2.69.160": true, "203.0.113.10": true, "::ffff:81.2.69.160": true,
		"192.168.1.20": false, "10.0.2.2": false, "172.16.0.1": false, "127.0.0.1": false,
		"169.254.1.2": false, "100.64.0.1": false, "fd00::1": false, "::1": false,
	} {
		if got := IsPublic(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: public %v, want %v", addr, got, want)
		}
	}
}

// A check link works once, from outside, for its own address. A visit
// from home leaves it working, so the Owner can try again on mobile data.
func TestChecks(t *testing.T) {
	var got []string
	c := &Checks{Record: func(host string, _ time.Time, from string) error {
		got = append(got, host+" "+from)
		return nil
	}}
	token := c.New("photos.example.com")
	if len(token) != 32 || strings.Trim(token, "0123456789abcdef") != "" {
		t.Fatalf("token %q must be 32 hex digits (caddy.CheckPattern)", token)
	}
	for _, v := range []struct {
		token, host, remote string
		want                int
	}{
		{"nope", "photos.example.com", "81.2.69.160", VisitUnknown},
		{token, "vault.example.com", "81.2.69.160", VisitUnknown},
		{token, "photos.example.com", "192.168.1.30", VisitFromHome},
		{token, "photos.example.com", "81.2.69.160", VisitOutside},
		{token, "photos.example.com", "81.2.69.160", VisitUnknown}, // used
	} {
		if r, err := c.Visit(v.token, v.host, v.remote); r != v.want || err != nil {
			t.Errorf("visit %+v: %d %v, want %d", v, r, err, v.want)
		}
	}
	if len(got) != 1 || got[0] != "photos.example.com 81.2.69.160" {
		t.Errorf("recorded %v", got)
	}
}

// A public IPv4 address of its own means a cloud server; home servers have
// private ones (and maybe public IPv6).
func TestNetworkOf(t *testing.T) {
	addrs := func(ss ...string) []netip.Addr {
		var out []netip.Addr
		for _, s := range ss {
			out = append(out, netip.MustParseAddr(s))
		}
		return out
	}
	for _, c := range []struct {
		addrs []netip.Addr
		want  string
	}{
		{addrs("127.0.0.1", "192.168.1.50", "::1", "fe80::1"), config.NetworkHome},
		{addrs("10.0.2.15", "2001:db8::5"), config.NetworkHome},
		{addrs("100.64.1.2"), config.NetworkHome},
		{addrs("127.0.0.1", "172.105.165.39", "2600:3c01::1"), config.NetworkInternet},
		{addrs("172.105.165.39", "192.168.140.2"), config.NetworkInternet},
	} {
		if got := networkOf(c.addrs); got != c.want {
			t.Errorf("%v: %s, want %s", c.addrs, got, c.want)
		}
	}
}
