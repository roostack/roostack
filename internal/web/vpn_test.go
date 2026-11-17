package web

import (
	"cmp"
	"context"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/store"
)

// fakeVPN is the manager's VPN.
type fakeVPN struct {
	dns     string
	servers []manager.EdgeServer
	v       config.VPN
	changes []string
	key     string
	seen    map[string]time.Time
}

func (f *fakeVPN) VPN() config.VPN                                   { return f.v }
func (f *fakeVPN) SetVPN(v config.VPN) error                         { f.v = v; return nil }
func (f *fakeVPN) VPNChanged(reason string)                          { f.changes = append(f.changes, reason) }
func (f *fakeVPN) DeviceDNS(context.Context, int64, []string) string { return f.dns }
func (f *fakeVPN) VPNWay(context.Context) manager.VPNWay {
	return manager.VPNWay{Server: "nuc", Endpoint: "link.lab.example.com:51822", PublicKey: f.key, AtHome: true,
		LastConnected: f.seen, HomeNetworks: []string{"192.168.1.0/24"}, Servers: f.servers, Chosen: cmp.Or(f.v.Server, "n-1")}
}

var privateKeyLine = regexp.MustCompile(`PrivateKey = (\S+)`)

// The lab's VPN on Your account (SPEC.md §10.11): the tab appears once the
// Owner turns it on; a Member adds a device, whose settings are shown once
// with only the public key kept; what only Owners may tick is ignored;
// the limit holds; changing what it reaches gives new settings; someone
// else's device can't be touched.
func TestVPNDevices(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	ctx := context.Background()
	fv := &fakeVPN{key: "c2VydmVyLWtleS1zZXJ2ZXIta2V5LXNlcnZlci1rZXk="}
	h.server.VPN = fv
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{IP: "192.168.1.20"}}
	if _, body := h.get("/account"); strings.Contains(body, "VPN devices") {
		t.Error("the tab while the VPN is off")
	}
	if code, _ := h.get("/account/devices"); code != http.StatusNotFound {
		t.Errorf("the page while the VPN is off: %d", code)
	}
	fv.v = config.VPN{On: true, MaxDevices: 2, HomeNetwork: true}
	if _, body := h.get("/account"); !strings.Contains(body, `href="/account/devices"`) {
		t.Error("no tab")
	}
	_, body := h.get("/account/devices/add")
	if !strings.Contains(body, `name="home"`) || strings.Contains(body, `name="internet"`) || strings.Contains(body, `type="radio"`) || strings.Contains(body, `name="ssh"`) ||
		strings.Contains(body, `name="pages"`) || strings.Contains(body, `name="code"`) {
		t.Errorf("a Member's form:\n%s", body)
	}

	// Before the VPN's server has its key: nothing added, and the form keeps
	// what was typed.
	key := fv.key
	fv.key = ""
	if code, body := h.post("/account/devices", url.Values{"name": {"Sam's phone"}, "home": {"yes"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "still starting") || !strings.Contains(body, `value="Sam&#39;s phone"`) {
		t.Errorf("not ready: %d\n%s", code, body)
	}
	fv.key = key

	code, body := h.post("/account/devices", url.Values{"name": {"Sam's  phone"}, "home": {"yes"}, "ssh": {"yes"}, "pages": {"yes"}}, h.srv.URL)
	if code != http.StatusOK || !strings.Contains(body, "Sam&#39;s phone is ready to add") || !strings.Contains(body, `<svg class="qr"`) ||
		!strings.Contains(body, `download="sam-s-phone.conf"`) || !strings.Contains(body, "only see this once") {
		t.Fatalf("added: %d\n%s", code, body)
	}
	for _, want := range []string{"Address = 100.96.128.2/32", "PublicKey = " + fv.key, "Endpoint = link.lab.example.com:51822",
		"AllowedIPs = 192.168.1.20/32, 192.168.1.0/24"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings missing %q", want)
		}
	}
	if strings.Contains(body, "DNS = ") {
		t.Error("a DNS server without one")
	}
	u, _ := h.store.UserByUsername(ctx, "u")
	list, _ := h.store.UserVPNDevices(ctx, u.ID)
	m := privateKeyLine.FindStringSubmatch(body)
	if len(list) != 1 || m == nil {
		t.Fatalf("devices %+v", list)
	}
	if pub, _ := agent.WireGuardPublicKey(html.UnescapeString(m[1])); pub != list[0].PublicKey || list[0].SSH || list[0].Pages || !list[0].Home || list[0].Internet {
		t.Errorf("kept %+v (only the public key, nothing a Member may not tick)", list[0])
	}
	if strings.Contains(fv.changes[len(fv.changes)-1], "added") == false {
		t.Errorf("not applied: %v", fv.changes)
	}
	logs, _ := h.store.RecentAudit(ctx, 1)
	if logs[0].Action != "vpn device added" || !strings.Contains(logs[0].Detail, "Sam's phone, reaching the lab and the home network") {
		t.Errorf("audit %+v", logs[0])
	}

	// The list, and Everything isn't allowed.
	fv.seen = map[string]time.Time{list[0].PublicKey: time.Now()}
	if _, body := h.get("/account/devices"); !strings.Contains(body, "Sam&#39;s phone") || !strings.Contains(body, "Today") ||
		!strings.Contains(body, "You can add 1 more") {
		t.Errorf("the list:\n%s", body)
	}
	// The internet isn't allowed, so asking for it by hand gives the lab only.
	h.post("/account/devices", url.Values{"name": {"Laptop"}, "internet": {"yes"}}, h.srv.URL)
	if all, _ := h.store.VPNDevices(ctx); len(all) != 2 || all[0].Name != "Laptop" || all[0].Internet || all[0].Home {
		t.Errorf("the internet while not allowed: %+v", all)
	}
	if code, body := h.post("/account/devices", url.Values{"name": {"Third"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "as many devices as the lab allows") {
		t.Errorf("past the limit: %d", code)
	}

	// Changing its name keeps its key; what it reaches gives a new one.
	id := itoa(list[0].ID)
	if !strings.Contains(mustGet(t, h, "/account/devices"), `href="/account/devices/`+id+`"`) {
		t.Error("no Change link")
	}
	h.post("/account/devices/"+id, url.Values{"name": {"Sam's old phone"}, "home": {"yes"}}, h.srv.URL)
	d, _ := h.store.VPNDevice(ctx, list[0].ID)
	if d.Name != "Sam's old phone" || d.PublicKey != list[0].PublicKey {
		t.Errorf("renamed: %+v", d)
	}
	if _, body := h.post("/account/devices/"+id, url.Values{"name": {"Sam's old phone"}}, h.srv.URL); !strings.Contains(body, "new settings") {
		t.Error("no new settings")
	}
	if d, _ = h.store.VPNDevice(ctx, list[0].ID); d.PublicKey == list[0].PublicKey || d.Home || d.Internet {
		t.Errorf("the key wasn't replaced: %+v", d)
	}

	// Someone else's device.
	eve := addPerson(t, h.store, "eve", auth.RoleMember)
	theirs, _ := h.store.AddVPNDevice(ctx, store.VPNDevice{UserID: eve, Name: "Eve's", PublicKey: "ZXZlLWtleS1ldmUta2V5LWV2ZS1rZXktZXZlLWtleS0="}, 0)
	if code, _ := h.post("/account/devices/"+itoa(theirs.ID)+"/remove", nil, h.srv.URL); code != http.StatusNotFound {
		t.Errorf("someone else's: %d", code)
	}
	if code, _ := h.post("/account/devices/"+id+"/remove", nil, h.srv.URL); code != http.StatusOK {
		t.Errorf("removing: %d", code)
	}
	if left, _ := h.store.UserVPNDevices(ctx, u.ID); len(left) != 1 {
		t.Errorf("after removing: %+v", left)
	}
}

// An Owner may tick Roostack's pages and SSH, and is asked for their
// two-factor code; a wrong one adds nothing.
func TestVPNDevicesOwner(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	ctx := context.Background()
	fv := &fakeVPN{v: config.VPN{On: true}, key: "c2VydmVyLWtleS1zZXJ2ZXIta2V5LXNlcnZlci1rZXk="}
	h.server.VPN = fv
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{IP: "192.168.1.20"}}
	if _, body := h.get("/account/devices/add"); !strings.Contains(body, `name="ssh"`) || !strings.Contains(body, `name="code"`) {
		t.Errorf("an Owner's form:\n%s", body)
	}
	form := url.Values{"name": {"Laptop"}, "ssh": {"yes"}, "pages": {"yes"}, "code": {"000000"}}
	if code, body := h.post("/account/devices", form, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "code isn&#39;t right") {
		t.Errorf("a wrong code: %d", code)
	}
	u, _ := h.store.UserByUsername(ctx, "u")
	form.Set("code", codeNow(t, u.TOTPSecret, 1))
	if code, body := h.post("/account/devices", form, h.srv.URL); code != http.StatusOK {
		t.Fatalf("with the code: %d\n%s", code, body)
	}
	if list, _ := h.store.UserVPNDevices(ctx, u.ID); len(list) != 1 || !list[0].SSH || !list[0].Pages {
		t.Errorf("an Owner's device: %+v", list)
	}

	// Settings → VPN: the Owner's settings and everyone's devices.
	_, body := h.get("/settings/vpn")
	if !strings.Contains(body, "Laptop") || !strings.Contains(body, "link.lab.example.com:51822") || !strings.Contains(body, "Forward UDP port 51822") {
		t.Errorf("Settings → VPN:\n%s", body)
	}
	h.post("/settings/vpn", url.Values{"on": {"yes"}, "limit": {""}, "internet": {"yes"}}, h.srv.URL)
	if !fv.v.On || !fv.v.NoLimit || !fv.v.Internet || fv.v.HomeNetwork || fv.v.Limit() != 0 {
		t.Errorf("saved %+v", fv.v)
	}
	// Both at once: the home network and the internet, which sends everything.
	fv.v.HomeNetwork = true
	if _, body := h.get("/account/devices/add"); !strings.Contains(body, `name="home"`) || !strings.Contains(body, `name="internet"`) ||
		!strings.Contains(body, "checked disabled") {
		t.Errorf("the form with both allowed:\n%s", body)
	}
	h.store.SetTOTP(ctx, u.ID, u.TOTPSecret, 0) // so that this step's code is accepted again
	form2 := url.Values{"name": {"Tablet"}, "home": {"yes"}, "internet": {"yes"}, "code": {codeNow(t, u.TOTPSecret, 0)}}
	if code, body := h.post("/account/devices", form2, h.srv.URL); !strings.Contains(body, "AllowedIPs = 0.0.0.0/0\n") {
		t.Errorf("both: %d\n%s", code, body)
	}
	if logs, _ := h.store.RecentAudit(ctx, 1); !strings.Contains(logs[0].Detail, "Tablet, reaching the lab, the home network, and the internet") {
		t.Errorf("audit %+v", logs[0])
	}
	if all, _ := h.store.VPNDevices(ctx); len(all) == 2 {
		h.store.DeleteVPNDevice(ctx, all[1].ID) // Tablet
	}
	h.store.SetTOTP(ctx, u.ID, u.TOTPSecret, 0)
	fv.v.HomeNetwork = false
	if code, _ := h.post("/settings/vpn", url.Values{"on": {"yes"}, "limit": {"0"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("a limit of 0: %d", code)
	}
	// With a cloud server in the lab, the Owner chooses where devices
	// connect, and an Owner's device with SSH reaches the lab's servers.
	fv.servers = []manager.EdgeServer{{ID: "n-1", Name: "nuc"}, {ID: "n-2", Name: "linode", Address: "172.105.1.20"}}
	if _, body := h.get("/settings/vpn"); !strings.Contains(body, `value="n-2"`) || !strings.Contains(body, "At 172.105.1.20") ||
		!strings.Contains(body, "link.lab.example.com</code>, once you forward") || !strings.Contains(body, "Give it new settings") {
		t.Errorf("the choice of server:\n%s", body)
	}
	h.post("/settings/vpn", url.Values{"on": {"yes"}, "limit": {"5"}, "server": {"n-2"}}, h.srv.URL)
	if fv.v.Server != "n-2" {
		t.Errorf("chose %q", fv.v.Server)
	}
	list, _ := h.store.VPNDevices(ctx)
	h.store.SetTOTP(ctx, u.ID, u.TOTPSecret, 0) // so that this step's code is accepted again
	form = url.Values{"name": {"Laptop"}, "ssh": {"yes"}, "new": {"yes"}, "code": {codeNow(t, u.TOTPSecret, 0)}}
	if _, body := h.post("/account/devices/"+itoa(list[0].ID), form, h.srv.URL); !strings.Contains(body, "new settings") ||
		!strings.Contains(body, "AllowedIPs = 192.168.1.20/32, 100.96.0.0/17") {
		t.Errorf("new settings:\n%s", body)
	}
	if d, _ := h.store.VPNDevice(ctx, list[0].ID); d.PublicKey == list[0].PublicKey {
		t.Error("the key wasn't replaced")
	}
	h.post("/settings/vpn/devices/"+itoa(list[0].ID)+"/remove", nil, h.srv.URL)
	if list, _ := h.store.VPNDevices(ctx); len(list) != 0 {
		t.Errorf("not removed: %+v", list)
	}
}

func mustGet(t *testing.T, h *harness, path string) string {
	t.Helper()
	_, body := h.get(path)
	return body
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// codeNow is the code steps time steps ahead: logging in used this one's,
// and each code is accepted once.
func codeNow(t *testing.T, secret string, steps int) string {
	t.Helper()
	c, err := authkit.TOTPCode(secret, time.Now().Add(time.Duration(steps)*30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestResolverCard: Settings → Where it is and DNS offers the lab's DNS
// server, saves the Owner's choice, and is the Owner's alone (SPEC.md
// §6.5.2).
func TestResolverCard(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.resolver = manager.Resolver{Service: &manager.DNSService{Name: "pihole", Title: "Pi-hole"}, Addr: "192.168.1.20"}
	_, body := h.get("/settings/home-network")
	if !strings.Contains(body, "DNS for your devices") || !strings.Contains(body, "Now Pi-hole, at <code>192.168.1.20</code>") {
		t.Errorf("card:\n%s", body)
	}
	h.post("/settings/resolver", url.Values{"use": {"address"}, "address": {"9.9.9.9"}, "server": {"n-1"}}, h.srv.URL)
	if c := fm.resolver.Choice; c.Use != "address" || c.Address != "9.9.9.9" || len(c.Servers) != 1 {
		t.Errorf("saved %+v", c)
	}
	h, _ = loggedIn(t, auth.RoleAdmin)
	if code, _ := h.post("/settings/resolver", url.Values{"use": {"provider"}}, h.srv.URL); code == http.StatusOK {
		t.Error("an Admin chose the lab's DNS server")
	}
}
