package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/testwait"
)

// TestPublicEdge: at home, the Owner chooses how visitors come in: port
// forwarding, or through a cloud server in the lab; records then say they
// point at it, and a missing edge is called out.
func TestPublicEdge(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	if _, body := h.get("/settings/public"); strings.Contains(body, "How people reach your public services") {
		t.Error("offered a way in without a cloud server")
	}
	fm.edges = []manager.EdgeServer{{ID: "n-0000000002", Name: "vps-sydney", Address: "172.105.165.39"}}
	_, body := h.get("/settings/public")
	for _, want := range []string{"How people reach your public services", `value="" required checked`, "Through vps-sydney, a cloud server in this lab",
		"<code>172.105.165.39</code>"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if code, _ := h.post("/settings/public/edge", url.Values{"edge": {"n-0000000099"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("a server that can't be the edge: %d", code)
	}
	_, body = h.post("/settings/public/edge", url.Values{"edge": {"n-0000000002"}}, h.srv.URL)
	if fm.edge != "n-0000000002" || !strings.Contains(body, `value="n-0000000002" checked`) || !strings.Contains(body, "Saved. Public addresses follow") {
		t.Errorf("choosing the edge: edge %q", fm.edge)
	}
	// The edge gone from the lab's network: said so.
	fm.edges = nil
	if _, body := h.get("/settings/public"); !strings.Contains(body, "n-0000000002 isn't connected to the lab now") {
		t.Error("a missing edge isn't called out")
	}
	h.post("/settings/public/edge", url.Values{"edge": {""}}, h.srv.URL)
	if fm.edge != "" {
		t.Error("back to port forwarding")
	}
}

// The Owner makes an address public (SPEC.md §6.11): a warning, the
// sharing question the first time, private paths left home-only unless
// ticked; then a checklist, and a check link that ticks itself when it's
// opened from outside.
func TestMakePublic(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.server.Checks = &public.Checks{Record: func(host string, at time.Time, from string) error {
		return RecordPublicCheck(context.Background(), h.store, host, at, from)
	}}
	h.server.Protected = func(context.Context) bool { return true }
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{IP: "192.168.1.20", PublicIP: "81.2.69.160",
		Public: []dnsrecords.PublicRecord{{Host: "passwords.lab.example.com", IP: "81.2.69.160", State: dnsrecords.PublicOK,
			Message: "passwords.lab.example.com points at 81.2.69.160."}}}}

	if _, body := h.get("/services/vaultwarden"); !strings.Contains(body, "Your home network (or VPN)") ||
		!strings.Contains(body, `href="/services/vaultwarden/public/web"`) {
		t.Fatal("the service page should say who can reach it, with a way to make it public")
	}
	_, body := h.get("/services/vaultwarden/public/web")
	for _, want := range []string{"anyone on the internet", "Share attack reports", `name="open" value="/admin"`, "Vaultwarden&#39;s admin panel.",
		"Make passwords.lab.example.com public? Anyone on the internet will be able to reach it."} {
		if !strings.Contains(body, want) {
			t.Errorf("the page should show %q", want)
		}
	}
	// The sharing question must be answered.
	if status, _ := h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}}, h.srv.URL); status != http.StatusBadRequest {
		t.Errorf("without the sharing answer: %d", status)
	}
	h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}, "share": {"no"}}, h.srv.URL)
	if len(fm.publicReqs) != 1 || !fm.publicReqs[0].Public || len(fm.publicReqs[0].OpenPaths) != 0 || !fm.sharingAsked || fm.share {
		t.Fatalf("made public: %+v, sharing asked %v share %v", fm.publicReqs, fm.sharingAsked, fm.share)
	}
	logs, _ := h.store.RecentAudit(context.Background(), 5)
	if len(logs) == 0 || logs[0].Detail != "passwords.lab.example.com made public" {
		t.Errorf("audit: %+v", logs)
	}

	_, body = h.get("/services/vaultwarden/public/web")
	for _, want := range []string{"CrowdSec is running", "points at 81.2.69.160", "192.168.1.20", "Make a check link",
		`<button type="button" class="quiet danger"`, `<button type="submit" class="danger">Make private</button>`,
		"Parts kept private", `name="open" value="/admin" >`} {
		if !strings.Contains(body, want) {
			t.Errorf("the checklist should show %q", want)
		}
	}
	// While it's public, the private parts can be opened (and closed).
	h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}, "open": {"/admin"}}, h.srv.URL)
	if req := fm.publicReqs[len(fm.publicReqs)-1]; !req.Public || !slices.Equal(req.OpenPaths, []string{"/admin"}) {
		t.Errorf("opening /admin: %+v", req)
	}
	if _, body := h.get("/services/vaultwarden/public/web"); !strings.Contains(body, `value="/admin" checked`) ||
		!strings.Contains(body, "(public now)") {
		t.Error("an opened part shows as public")
	}
	h.post("/services/vaultwarden/public/web/check", nil, h.srv.URL)
	_, body = h.get("/services/vaultwarden/public/web")
	token := regexp.MustCompile(`roostack-check/([0-9a-f]{32})`).FindStringSubmatch(body)
	if token == nil || !strings.Contains(body, "<svg") {
		t.Fatal("no check link and QR code")
	}

	visit := func(from string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/.well-known/roostack-check/"+token[1], nil)
		req.Host = "passwords.lab.example.com"
		req.Header.Set("X-Forwarded-For", from)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := new(strings.Builder)
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		b.Write(buf[:n])
		return resp.StatusCode, b.String()
	}
	if _, msg := visit("192.168.1.30"); !strings.Contains(msg, "Turn Wi-Fi off") {
		t.Errorf("from home: %s", msg)
	}
	if status, msg := visit("81.2.69.200"); status != http.StatusOK || !strings.Contains(msg, "It works") {
		t.Errorf("from outside: %d %s", status, msg)
	}
	if status, _ := visit("81.2.69.200"); status != http.StatusNotFound {
		t.Errorf("a used link: %d", status)
	}
	if _, body := h.get("/services/vaultwarden/public/web"); !strings.Contains(body, "It worked from outside") || !strings.Contains(body, "81.2.69.200") {
		t.Error("the check should be ticked")
	}

	// Private again.
	h.post("/services/vaultwarden/public/web", url.Values{"public": {"off"}}, h.srv.URL)
	if last := fm.publicReqs[len(fm.publicReqs)-1]; last.Public {
		t.Error("should be private again")
	}
}

// An address the package calls unsafe needs a tick; only Owners get the
// pages.
func TestMakePublicUnsafeAndOwnerOnly(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.sharingAsked = true
	fm.access = []manager.RouteAccess{{Route: "web", Host: "passwords.lab.example.com", Unsafe: "It's an admin tool."}}
	if _, body := h.get("/services/vaultwarden/public/web"); !strings.Contains(body, "It&#39;s an admin tool.") ||
		strings.Contains(body, "Share attack reports") {
		t.Error("the package's reason, and no sharing question once answered")
	}
	status, body := h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}}, h.srv.URL)
	if status != http.StatusBadRequest || !strings.Contains(body, "Tick the box") || len(fm.publicReqs) != 0 {
		t.Errorf("unsafe without the tick: %d", status)
	}
	h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}, "accept_unsafe": {"on"}, "open": {"/nope"}}, h.srv.URL)
	if len(fm.publicReqs) != 1 {
		t.Error("with the tick it goes public")
	}

	ha, _ := loggedIn(t, auth.RoleAdmin)
	for _, path := range []string{"/services/vaultwarden/public/web", "/settings/public"} {
		if status, _ := ha.get(path); status != http.StatusForbidden {
			t.Errorf("%s as an Admin: %d", path, status)
		}
	}
	if _, body := ha.get("/services/vaultwarden"); strings.Contains(body, "/public/web") {
		t.Error("Admins shouldn't be offered it")
	}
}

// Settings → Public access lists what's public and changes the sharing
// choice.
func TestPublicSettings(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	if _, body := h.get("/settings"); !strings.Contains(body, "Nothing is public") {
		t.Error("overview card")
	}
	if _, body := h.get("/settings/public"); !strings.Contains(body, "Nothing is public") || !strings.Contains(body, "Not chosen yet") {
		t.Error("public settings page")
	} else if strings.Contains(body, `name="share" value="yes" required checked`) || strings.Contains(body, `value="no" checked`) {
		t.Error("nothing is ticked until the Owner chooses")
	}
	h.post("/settings/public/sharing", url.Values{"share": {"no"}}, h.srv.URL)
	if !fm.sharingAsked || fm.share {
		t.Error("sharing not saved")
	}
	if status, _ := h.post("/settings/public/sharing", url.Values{"share": {"maybe"}}, h.srv.URL); status != http.StatusBadRequest {
		t.Errorf("a bad answer: %d", status)
	}
}

// TestEdgeDown: with visitors coming in through a cloud server that isn't
// connected, public services are down from outside, and that's a problem
// to fix; connected, nothing to say.
func TestEdgeDown(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.access = []manager.RouteAccess{{Host: "photos.smith.family", Public: true}}
	fm.edges = []manager.EdgeServer{{ID: "n-0000000002", Name: "vps-sydney", Address: "172.105.165.39"}}
	fm.edge = "n-0000000002"
	this := manager.Server{ID: "n-0123456789", Name: "nuc", Controller: true, This: true, Reachable: true}
	vps := manager.Server{ID: "n-0000000002", Name: "vps-sydney", Reachable: true}
	fm.servers = []manager.Server{this, vps}
	if _, body := h.get("/"); strings.Contains(body, "Public services can&#39;t be reached") {
		t.Error("a connected edge called out")
	}
	vps.Reachable = false
	vps.Status = manager.ApplyStatus{Finished: true, LastErr: fmt.Errorf("Roostack can't reach vps-sydney: %w", node.ErrUnreachable)}
	fm.servers = []manager.Server{this, vps}
	if _, body := h.get("/"); !strings.Contains(body, "Public services can&#39;t be reached from the internet: visitors come in through vps-sydney") ||
		!strings.Contains(body, `href="/settings/public#way-in"`) {
		t.Error("an edge that isn't connected isn't called out")
	}
}

// TestPublicRelay: a lab linked to Roostack Cloud, with its roost.ac name
// as its address, can have visitors come in through the relay; Cloud's
// refusal is shown, and choosing port forwarding again turns it off.
func TestPublicRelay(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	if _, body := h.get("/settings/public"); strings.Contains(body, "Roostack Cloud&#39;s relay") || strings.Contains(body, "Roostack Cloud's relay") {
		t.Error("the relay offered without Cloud")
	}
	link, f := newCloudWith(t)
	h.server.Cloud = link
	link.UseName = func(context.Context, cloudapi.NameClaim) error { return nil }
	if _, err := link.Start(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	f.Confirm(link.Status().Waiting.Code)
	testwait.For(t, 10*time.Second, func() bool { return link.Status().Linked })
	if err := link.ClaimName(context.Background(), "smiths"); err != nil {
		t.Fatal(err)
	}
	h.server.Domain = "smiths.roost.test"
	_, body := h.get("/settings/public")
	if !strings.Contains(body, "Through Roostack Cloud's relay") || !strings.Contains(body, `value="cloud"`) {
		t.Fatal("the relay isn't offered to a linked lab with its roost.ac name")
	}
	fm.relayErr = &manager.FormError{Msg: "Roostack Cloud's relay isn't open for this account yet."}
	if _, body := h.post("/settings/public/edge", url.Values{"edge": {"cloud"}}, h.srv.URL); !strings.Contains(body, "isn&#39;t open for this account") || fm.relay {
		t.Error("Cloud's refusal isn't shown")
	}
	fm.relayErr = nil
	if _, body := h.post("/settings/public/edge", url.Values{"edge": {"cloud"}}, h.srv.URL); !fm.relay || !strings.Contains(body, `value="cloud" checked`) {
		t.Error("choosing the relay")
	}
	h.post("/settings/public/edge", url.Values{"edge": {""}}, h.srv.URL)
	if fm.relay {
		t.Error("port forwarding didn't turn the relay off")
	}
}

// TestPublicCountries: while relayed, the Owner chooses who can visit by
// country, and can let everyone in for a while (travel) and end it.
func TestPublicCountries(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	if _, body := h.get("/settings/public"); strings.Contains(body, "Visitors by country") {
		t.Error("countries offered without the relay")
	}
	fm.relay = true
	_, body := h.get("/settings/public")
	if !strings.Contains(body, "Visitors by country") || !strings.Contains(body, `<option value="NZ" >New Zealand</option>`) {
		t.Fatalf("no countries card:\n%s", body)
	}
	if _, body := h.post("/settings/public/countries", url.Values{"mode": {"allow"}}, h.srv.URL); !strings.Contains(body, "Choose at least one country") {
		t.Error("allowing no countries")
	}
	_, body = h.post("/settings/public/countries", url.Values{"mode": {"allow"}, "countries": {"NZ", "AU", "XX"}}, h.srv.URL)
	if !slices.Equal(fm.countries.Allow, []string{"AU", "NZ"}) || !strings.Contains(body, `<option value="AU" selected>Australia</option>`) ||
		!strings.Contains(body, "Let everyone in") {
		t.Errorf("allowing: %+v", fm.countries)
	}
	_, body = h.post("/settings/public/countries/open", url.Values{"action": {"open"}, "days": {"14"}}, h.srv.URL)
	if until := time.Until(fm.countries.OpenUntil); until < 13*24*time.Hour || !strings.Contains(body, "Everyone can visit until") ||
		!strings.Contains(body, "only visitors from\n      Australia and New Zealand again") {
		t.Errorf("opening: %v\n%s", fm.countries.OpenUntil, body)
	}
	if code, _ := h.post("/settings/public/countries/open", url.Values{"action": {"open"}, "days": {"400"}}, h.srv.URL); code != 400 {
		t.Errorf("a year and more: %d", code)
	}
	h.post("/settings/public/countries/open", url.Values{"action": {"close"}}, h.srv.URL)
	if !fm.countries.OpenUntil.IsZero() || len(fm.countries.Allow) != 2 {
		t.Errorf("closing: %+v", fm.countries)
	}
	h.post("/settings/public/countries", url.Values{"mode": {"anyone"}, "countries": {"NZ"}}, h.srv.URL)
	if fm.countries.Limited() {
		t.Errorf("anyone: %+v", fm.countries)
	}
	fm.countries = config.Countries{}
	if _, body := h.post("/settings/public/countries", url.Values{"mode": {"block"}, "countries": {"RU"}}, h.srv.URL); !slices.Equal(fm.countries.Block, []string{"RU"}) ||
		strings.Contains(body, "Everyone can visit until") {
		t.Errorf("blocking: %+v", fm.countries)
	}
}

// Blocking known exploits (SPEC.md §6.10 item 4): a public service's page
// counts what AppSec refused this week and lists the newest, by its
// description, path (never the query) and rule; the Owner can turn it off
// for the service after a warning, and on again.
func TestKnownExploits(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	now := time.Now()
	asked := 0
	h.server.Blocks = func(context.Context, time.Duration) ([]public.Block, error) {
		asked++
		return []public.Block{
			{At: now, Host: "passwords.lab.example.com", Rule: "vpatch-CVE-2017-9841", What: "PHPUnit RCE (CVE-2017-9841)",
				Path: "/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php", From: "203.0.113.10"},
			{At: now.Add(-time.Minute), Host: "git.lab.example.com", Rule: "vpatch-env-access", What: "Detect access to .env files", Path: "/.env"},
			{At: now.Add(-2 * time.Minute), Host: "passwords.lab.example.com", Rule: "vpatch-env-access", What: "Detect access to .env files", Path: "/.env"},
		}, nil
	}
	// Private: no card.
	if _, body := h.get("/services/vaultwarden"); strings.Contains(body, "Known exploits") {
		t.Error("a private service shows refused requests")
	}
	fm.Access("vaultwarden")
	fm.access[0].Public = true
	_, body := h.get("/services/vaultwarden")
	for _, want := range []string{"Known exploits", `<span class="big-figure">2<small>refused this week</small>`, "PHPUnit RCE (CVE-2017-9841)",
		"<code>/vendor/phpunit/phpunit/src/Util/PHP/eval-stdin.php</code>", "from 203.0.113.10", "vpatch-CVE-2017-9841",
		`href="/services/vaultwarden/public/web#waf"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the service page should show %q", want)
		}
	}
	if h.get("/services/vaultwarden"); asked != 1 {
		t.Errorf("CrowdSec asked %d times: pages that poll use the last answer for a minute", asked)
	}

	_, body = h.get("/services/vaultwarden/public/web")
	if !strings.Contains(body, "Blocking known exploits") || !strings.Contains(body, "Stop blocking known exploits for Vaultwarden?") {
		t.Fatal("the public access page should say it's on, with a way to turn it off")
	}
	h.post("/services/vaultwarden/public/web/waf", url.Values{"waf": {"off"}}, h.srv.URL)
	if fm.access[0].WAF {
		t.Fatal("not turned off")
	}
	if logs, _ := h.store.RecentAudit(context.Background(), 1); len(logs) == 0 || logs[0].Detail != "blocking known exploits turned off for vaultwarden" {
		t.Errorf("audit: %+v", logs)
	}
	if _, body := h.get("/services/vaultwarden"); !strings.Contains(body, "Not blocked: requests that try a known exploit reach Vaultwarden.") {
		t.Error("the service page should say it's off")
	}
	if _, body := h.get("/services/vaultwarden/public/web"); !strings.Contains(body, "Not blocked for Vaultwarden") ||
		!strings.Contains(body, ">Block known exploits</button>") {
		t.Error("the public access page should warn while it's off")
	}
	h.post("/services/vaultwarden/public/web/waf", url.Values{"waf": {"on"}}, h.srv.URL)
	if !fm.access[0].WAF {
		t.Error("not turned on again")
	}

	// Only the Owner.
	ha, fa := loggedIn(t, auth.RoleAdmin)
	fa.Access("vaultwarden")
	if code, _ := ha.post("/services/vaultwarden/public/web/waf", url.Values{"waf": {"off"}}, ha.srv.URL); code != http.StatusForbidden || !fa.access[0].WAF {
		t.Errorf("an Admin turned it off: %d", code)
	}
}

// The weekly summary's line, by service, most first.
func TestWAFSummary(t *testing.T) {
	blocks := []public.Block{{Host: "vault.example.com"}, {Host: "photos.example.com"}, {Host: "photos.example.com"}, {Host: "old.example.com"}}
	got := wafSummary(blocks, map[string]string{"vault.example.com": "Vaultwarden", "photos.example.com": "Immich"})
	if want := "refused 4 requests that tried a known exploit (Immich 2, Vaultwarden 1, old.example.com 1)."; got != want {
		t.Errorf("got %q", got)
	}
	if wafSummary(nil, nil) != "" {
		t.Error("none")
	}
}
