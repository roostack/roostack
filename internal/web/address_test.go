package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/labnet"
)

// fakeNetwork makes join codes and remembers their addresses.
type fakeNetwork struct{ endpoints, dials []string }

func (f *fakeNetwork) NewJoinCode(_ context.Context, endpoint, dial string) (labnet.JoinCode, error) {
	f.endpoints = append(f.endpoints, endpoint)
	f.dials = append(f.dials, dial)
	return labnet.JoinCode{Endpoint: endpoint, Listen: dial != "", Addr: netip.MustParseAddr("100.96.0.5")}, nil
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

	_, body := h.get("/servers")
	for _, want := range []string{"Address the new server reaches this one at", "A name from your domain: <code>link.lab.example.com:51820</code>",
		"The public address: <code>81.2.69.160:51820</code>", `name="address_other"`} {
		if !strings.Contains(body, want) {
			t.Errorf("join form missing %q", want)
		}
	}
	if !strings.Contains(body, `action="/settings/servers/join#add-server"`) {
		t.Error("after making a code, the page opens at it")
	}
	_, made := h.post("/settings/servers/join", url.Values{"address": {"link.lab.example.com:51820"}}, h.srv.URL)
	if !strings.Contains(made, "Join code for a new server") || !strings.Contains(made, `class="copy-box"`) ||
		strings.Contains(made, `action="/settings/servers/join`) || !strings.Contains(made, `href="/servers#add-server">Make another code`) {
		t.Error("a new code is shown in place of the form, with Make another code")
	}
	if len(fn.endpoints) != 1 || fn.endpoints[0] != "link.lab.example.com:51820" {
		t.Fatalf("join code for %v", fn.endpoints)
	}
	if v, _ := h.server.Store.Setting(context.Background(), LinkWantedKey); v != "on" {
		t.Error("choosing the name starts its record")
	}
	_, body = h.get("/servers")
	if !strings.Contains(body, `value="link.lab.example.com:51820" x-model="choice" required checked`) {
		t.Error("the last one used is suggested")
	}
	// Another address (a test VM behind QEMU's network).
	h.post("/settings/servers/join", url.Values{"address": {"other"}, "address_other": {"10.0.2.2:61820"}}, h.srv.URL)
	if fn.endpoints[1] != "10.0.2.2:61820" {
		t.Errorf("typed: %v", fn.endpoints)
	}
	if _, body = h.get("/servers"); !strings.Contains(body, `value="10.0.2.2:61820"`) || !strings.Contains(body, `value="other" x-model="choice" checked`) {
		t.Error("a typed address is suggested again as typed")
	}
	if code, body := h.post("/settings/servers/join", url.Values{"address": {"other"}, "address_other": {"nonsense"}}, h.srv.URL); code != 400 ||
		!strings.Contains(body, `id="add-server" aria-labelledby="add-server-title" open>`) {
		t.Errorf("no port: %d, or its dialog isn't open", code)
	}
}

// TestJoinDial: at home, a join code can have this server dial the new
// one (a cloud server, without a port forward); its address is checked,
// and the code says the port must be open there.
func TestJoinDial(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: allDone})
	fn := &fakeNetwork{}
	h.server.Network = fn
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{IP: "192.168.1.20", PublicIP: "81.2.69.160"}}

	if _, body := h.get("/servers"); !strings.Contains(body, "None: this server connects to the new one instead") ||
		!strings.Contains(body, `name="address_dial"`) {
		t.Fatal("no dial choice at home")
	}
	for typed, why := range map[string]string{"192.168.1.30": "isn&#39;t a public address", "nonsense": "public address, such as",
		"203.0.113.7:99999": "isn&#39;t a number"} {
		code, body := h.post("/settings/servers/join", url.Values{"address": {"dial"}, "address_dial": {typed}}, h.srv.URL)
		if code != 400 || !strings.Contains(body, why) || !strings.Contains(body, `value="`+typed+`"`) {
			t.Errorf("%s: %d, want %q and the address kept", typed, code, why)
		}
	}
	_, made := h.post("/settings/servers/join", url.Values{"address": {"dial"}, "address_dial": {"203.0.113.7"}}, h.srv.URL)
	if len(fn.dials) != 1 || fn.dials[0] != "203.0.113.7:51820" || fn.endpoints[0] != "" {
		t.Fatalf("dial %v, endpoints %v", fn.dials, fn.endpoints)
	}
	if !strings.Contains(made, "Join code for a new server") || !strings.Contains(made, "connects to the new one at <code>203.0.113.7:51820</code>") {
		t.Error("the code doesn't say this server will connect")
	}
	h.post("/settings/servers/join", url.Values{"address": {"dial"}, "address_dial": {"vps.example.com:61820"}}, h.srv.URL)
	if fn.dials[1] != "vps.example.com:61820" {
		t.Errorf("a name with its port: %v", fn.dials)
	}
}

// A fresh server joins an existing lab from its first-run page, after the
// setup code: a bad code or a failure says so and changes nothing; on
// success the page says where to manage it.
func TestSetupJoin(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	var joined []string
	fail := error(nil)
	h.server.JoinLab = func(_ context.Context, c labnet.JoinCode, name string) (string, error) {
		if fail != nil {
			return "", fail
		}
		joined = append(joined, c.LabName+" as "+name)
		return c.Addr.String(), nil
	}
	key, _ := agent.NewWireGuardKey()
	pub, _ := agent.WireGuardPublicKey(key)
	code := labnet.JoinCode{Endpoint: "192.168.1.20:51820", ControllerKey: pub, ControllerID: "n-0000000001",
		ControllerAddr: netip.MustParseAddr("100.96.0.1"), Addr: netip.MustParseAddr("100.96.0.4"), PrefixBits: 24,
		JoinKey: key, CA: "f", Token: "t", LabName: "The Smiths' lab", UI: "https://roost.lab.example.com"}.String()

	if h.post("/setup/join", url.Values{"code": {code}, "name": {"nas"}}, h.srv.URL); len(joined) != 0 {
		t.Fatal("not without the setup code")
	}
	h.post("/setup/code", url.Values{"token": {"the-setup-code"}}, h.srv.URL)
	if _, body := h.get("/setup"); !strings.Contains(body, `action="/setup/join"`) || !strings.Contains(body, "Add it to a lab you already have") {
		t.Fatal("the choice is offered")
	}
	if code, body := h.post("/setup/join", url.Values{"code": {"nonsense"}, "name": {"nas"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "isn&#39;t a Roostack join code") || !strings.Contains(body, `value="join" x-model="choice" checked`) {
		t.Errorf("bad code: %d", code)
	}
	fail = errors.New("couldn't reach The Smiths' lab at 192.168.1.20:51820")
	if code, body := h.post("/setup/join", url.Values{"code": {code}, "name": {"nas"}}, h.srv.URL); code != http.StatusBadGateway ||
		!strings.Contains(body, "Nothing was changed here") {
		t.Errorf("failure: %d", code)
	}
	fail = nil
	_, body := h.post("/setup/join", url.Values{"code": {code}, "name": {"nas"}}, h.srv.URL)
	for _, want := range []string{"Joined The Smiths&#39; lab", "<code>100.96.0.4</code>", `href="https://roost.lab.example.com/servers"`} {
		if !strings.Contains(body, want) {
			t.Errorf("joined page missing %q", want)
		}
	}
	if len(joined) != 1 || joined[0] != "The Smiths' lab as nas" {
		t.Errorf("joined %v", joined)
	}
}

// A server that's part of a lab has one page: which lab, and where to
// manage it.
func TestNodePage(t *testing.T) {
	var fence *backup.Fence
	h, err := NodePage("nas", "The Smiths' lab", "https://roost.lab.example.com", func() *backup.Fence { return fence })
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{"<h1>nas</h1>", "part of <strong>The Smiths&#39; lab</strong>", `href="https://roost.lab.example.com/servers"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("node page missing %q:\n%s", want, body)
		}
	}
	if resp, err := http.Get(srv.URL + "/static/css/app.css"); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("styles: %v %v", resp, err)
	}
	// Removed from the lab while it was off: it says so.
	fence = &backup.Fence{Reason: backup.FenceRemoved}
	resp, _ = http.Get(srv.URL + "/")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "This server was removed from its lab") {
		t.Error("a removed server's page should say so")
	}
}
