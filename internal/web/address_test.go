package web

import (
	"context"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/labnet"
)

// fakeNetwork makes join codes and remembers their addresses.
type fakeNetwork struct{ endpoints []string }

func (f *fakeNetwork) NewJoinCode(_ context.Context, endpoint string) (labnet.JoinCode, error) {
	f.endpoints = append(f.endpoints, endpoint)
	return labnet.JoinCode{Endpoint: endpoint, Addr: netip.MustParseAddr("100.96.0.5")}, nil
}

// Adding a server offers the addresses Roostack knows (a name from the
// domain, the home's public address) and another one; the last one used
// is suggested; choosing the name starts its DNS record.
func TestJoinAddressChoice(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: allDone})
	fn := &fakeNetwork{}
	h.server.Network = fn
	fd := &fakeDNS{st: dnsrecords.Status{IP: "192.168.1.20", PublicIP: "81.2.69.160"}}
	h.server.DNS = fd

	_, body := h.get("/settings/servers")
	for _, want := range []string{"Address the new server reaches this one at", "A name from your domain: <code>link.lab.example.com:51820</code>",
		"The public address: <code>81.2.69.160:51820</code>", `name="address_other"`} {
		if !strings.Contains(body, want) {
			t.Errorf("join form missing %q", want)
		}
	}
	h.post("/settings/servers/join", url.Values{"address": {"link.lab.example.com:51820"}}, h.srv.URL)
	if len(fn.endpoints) != 1 || fn.endpoints[0] != "link.lab.example.com:51820" {
		t.Fatalf("join code for %v", fn.endpoints)
	}
	if v, _ := h.server.Store.Setting(context.Background(), LinkWantedKey); v != "on" {
		t.Error("choosing the name starts its record")
	}
	_, body = h.get("/settings/servers")
	if !strings.Contains(body, `value="link.lab.example.com:51820" x-model="choice" required checked`) {
		t.Error("the last one used is suggested")
	}
	// Another address (a test VM behind QEMU's network).
	h.post("/settings/servers/join", url.Values{"address": {"other"}, "address_other": {"10.0.2.2:61820"}}, h.srv.URL)
	if fn.endpoints[1] != "10.0.2.2:61820" {
		t.Errorf("typed: %v", fn.endpoints)
	}
	if _, body = h.get("/settings/servers"); !strings.Contains(body, `value="10.0.2.2:61820"`) || !strings.Contains(body, `value="other" x-model="choice" checked`) {
		t.Error("a typed address is suggested again as typed")
	}
	if code, _ := h.post("/settings/servers/join", url.Values{"address": {"other"}, "address_other": {"nonsense"}}, h.srv.URL); code != 400 {
		t.Errorf("no port: %d", code)
	}
}
