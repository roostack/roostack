package web

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/store"
)

// withMonitor gives the harness a monitor whose service checks answer as
// up says: Vaultwarden (public) and Forgejo (private, down when up is
// false), and a server; own checks always answer.
func withMonitor(h *harness, up *bool) {
	h.server.AutoChecks = func(context.Context) []monitor.Check {
		return []monitor.Check{
			{Key: "service:vaultwarden:web", Kind: monitor.KindService, Name: "Vaultwarden", Service: "vaultwarden", Address: "vault.lab.example.com",
				Server: "nuc", URL: "https://vault.lab.example.com/alive", Alert: true, Public: true},
			{Key: "service:forgejo:web", Kind: monitor.KindService, Name: "Forgejo", Service: "forgejo", Address: "git.lab.example.com",
				Server: "nuc", URL: "https://git.lab.example.com/", Alert: true},
			{Key: "server:n-2", Kind: monitor.KindServer, Name: "mum-and-dad", Address: "over the lab network", TCP: "100.96.0.2:8471"},
		}
	}
	h.server.StatusPage = &fakeStatusPage{}
	h.server.Monitor = &monitor.Monitor{Store: h.store, Checks: h.server.MonitorChecks, Enabled: h.server.MonitorOn,
		Prober: func(_ context.Context, c monitor.Check) store.MonitorResult {
			if c.Key == "service:forgejo:web" && !*up {
				return store.MonitorResult{Err: "it answered 502 Bad Gateway", Status: 502}
			}
			return store.MonitorResult{Up: true, Status: 200, Millis: 140}
		}}
}

// The Monitoring page lists services, servers and own checks with their
// state; three failures make a check down, on the page, under "needs
// attention" and on its Details.
func TestMonitoringPage(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	up := true
	withMonitor(h, &up)
	ctx := context.Background()
	h.server.Monitor.Round(ctx)
	_, body := h.get("/monitoring")
	for _, want := range []string{"Vaultwarden", "vault.lab.example.com · on nuc · 140 ms", "mum-and-dad", `href="/monitoring/check/service:vaultwarden:web"`,
		"Public status page", "status.lab.example.com", "Turn monitoring off", `class="uptime-bar mini"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// Ticked by default: services open to the internet only.
	if !strings.Contains(body, `value="service:vaultwarden:web" checked`) || strings.Contains(body, `value="service:forgejo:web" checked`) {
		t.Errorf("status page choices:\n%s", body)
	}
	up = false
	for range monitor.DownAfter {
		h.server.Monitor.Round(ctx)
	}
	_, body = h.get("/monitoring")
	if !strings.Contains(body, "Forgejo isn&#39;t answering at its address: it answered 502 Bad Gateway.") {
		t.Errorf("no notice of Forgejo down:\n%s", body)
	}
	_, body = h.get("/")
	if !strings.Contains(body, "Forgejo isn&#39;t answering at its address") {
		t.Errorf("Home doesn't say Forgejo is down")
	}
	_, body = h.get("/monitoring/check/service:forgejo:web")
	for _, want := range []string{"Forgejo", "Not answering since", "https://git.lab.example.com/", "When it was down", "It answered 502 Bad Gateway"} {
		if !strings.Contains(body, want) {
			t.Errorf("Details lack %q", want)
		}
	}
	if code, _ := h.get("/monitoring/check/nope"); code != http.StatusNotFound {
		t.Errorf("an unknown check: %d", code)
	}
}

// Own checks are added (a bad address comes back with the form), shown,
// and removed; only Owners change the switch and the status page.
func TestMonitoringOwnChecks(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	up := true
	withMonitor(h, &up)
	_, body := h.post("/monitoring/checks", url.Values{"name": {"NAS"}, "address": {"ftp://nas.local"}, "alert": {"on"}}, h.srv.URL)
	if !strings.Contains(body, "A web address starts with http://") || !strings.Contains(body, `value="ftp://nas.local"`) {
		t.Errorf("a bad address:\n%s", body)
	}
	// The add form is a dialog: it comes back open with the mistake, and
	// ?add=check (its button, without scripts) opens it; otherwise closed.
	if !strings.Contains(body, `id="add-check" aria-labelledby="add-check-title" open>`) {
		t.Error("a refused check's dialog isn't open")
	}
	if _, body := h.get("/monitoring?add=check"); !strings.Contains(body, `id="add-check" aria-labelledby="add-check-title" open>`) {
		t.Error("?add=check doesn't open the dialog")
	}
	if _, body := h.get("/monitoring"); !strings.Contains(body, `id="add-check" aria-labelledby="add-check-title">`) ||
		!strings.Contains(body, `data-dialog="add-check"`) {
		t.Error("the dialog is open, or has no button")
	}
	h.post("/monitoring/checks", url.Values{"name": {"NAS"}, "address": {"https://nas.local:5001"}, "skip_verify": {"on"}, "alert": {"on"}}, h.srv.URL)
	checks, _ := h.store.MonitorChecks(context.Background())
	if len(checks) != 1 || checks[0].Address != "https://nas.local:5001" || !checks[0].SkipVerify || !checks[0].Alert {
		t.Fatalf("checks %+v", checks)
	}
	h.server.Monitor.Round(context.Background())
	_, body = h.get("/monitoring")
	if !strings.Contains(body, "https://nas.local:5001 · 140 ms") {
		t.Errorf("the own check isn't listed:\n%s", body)
	}
	if strings.Contains(body, "Turn monitoring off") || strings.Contains(body, "Public status page") {
		t.Error("an Admin sees the Owner's controls")
	}
	if code, _ := h.post("/monitoring/switch", url.Values{"on": {"no"}}, h.srv.URL); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("an Admin turned monitoring off: %d", code)
	}
	h.post("/monitoring/checks/1/remove", url.Values{}, h.srv.URL)
	if checks, _ := h.store.MonitorChecks(context.Background()); len(checks) != 0 {
		t.Errorf("not removed: %+v", checks)
	}

	member, _ := loggedIn(t, auth.RoleMember)
	withMonitor(member, &up)
	if code, _ := member.get("/monitoring"); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("a Member opened Monitoring: %d", code)
	}
}

// fakeStatusPage is the manager's status page switch.
type fakeStatusPage struct{ on bool }

func (f *fakeStatusPage) StatusPageOn() bool          { return f.on }
func (f *fakeStatusPage) SetStatusPage(on bool) error { f.on = on; return nil }

// The Owner turns the status page on and chooses what it shows; the page
// written shows just that, without addresses. Turning monitoring off
// stops it.
func TestMonitoringOwnerSettings(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	up := false
	withMonitor(h, &up)
	sp := h.server.StatusPage.(*fakeStatusPage)
	h.server.StatusPageDir = t.TempDir()
	ctx := context.Background()
	for range monitor.DownAfter {
		h.server.Monitor.Round(ctx)
	}
	h.post("/monitoring/status-page", url.Values{"choices": {"yes"}, "on": {"yes"}, "show": {"service:forgejo:web"}}, h.srv.URL)
	if !sp.on {
		t.Error("the status page isn't on")
	}
	shown := h.server.statusShown(ctx, h.server.Monitor.Statuses())
	if !shown["service:forgejo:web"] || shown["service:vaultwarden:web"] {
		t.Errorf("shown %v", shown)
	}
	page, err := os.ReadFile(filepath.Join(h.server.StatusPageDir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Forgejo", "Nothing is working at the moment.", `class="uptime-bar"`, `href="/css/app.css"`} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the page lacks %q:\n%s", want, page)
		}
	}
	for _, never := range []string{"Vaultwarden", "git.lab.example.com", "502"} {
		if strings.Contains(string(page), never) {
			t.Errorf("the page shows %q", never)
		}
	}
	if _, err := os.Stat(filepath.Join(h.server.StatusPageDir, "fonts", "space-grotesk-latin.woff2")); err != nil {
		t.Errorf("the font isn't beside it: %v", err)
	}

	h.post("/monitoring/switch", url.Values{"on": {"no"}}, h.srv.URL)
	if h.server.MonitorOn(ctx) {
		t.Fatal("monitoring is still on")
	}
	h.server.Monitor.Round(ctx)
	if _, body := h.get("/monitoring"); !strings.Contains(body, "Monitoring is off") || !strings.Contains(body, "Turn monitoring on") {
		t.Errorf("off:\n%s", body)
	}
}

// A service's page has its Uptime card.
func TestServiceUptimeCard(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	up := true
	withMonitor(h, &up)
	h.server.Monitor.Round(context.Background())
	_, body := h.get("/services/vaultwarden")
	for _, want := range []string{"Uptime", "Answers in 140 ms", `href="/monitoring/check/service:vaultwarden:web"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the service's page lacks %q", want)
		}
	}
}
