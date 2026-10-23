package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/registry"
)

// Pausing public access (SPEC.md §6.16): Home offers it while anything is
// public, the page pauses everything (by default), by what services are
// built with, or chosen services, each recorded in the audit log by who
// did it; Home, the services list, the service's page and Settings →
// Public access then say so, and the Owner opens it again.
func TestPausePublic(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	ctx := context.Background()
	if _, body := h.get("/"); !strings.Contains(body, `href="/pause"`) || !strings.Contains(body, "Pause public access…") {
		t.Error("Home doesn't offer to pause public access")
	}
	code, body := h.get("/pause")
	for _, want := range []string{"Pause public access", `value="all" checked`, "Vaultwarden", "passwords.lab.example.com",
		`name="built_with" value="rust">`, "Rust <small", "<code>sqlite</code>", `name="service" value="vaultwarden"`, "Pause now"} {
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("the pause page lacks %q (%d)", want, code)
		}
	}
	if _, body := h.post("/pause", url.Values{"scope": {"some"}}, h.srv.URL); !strings.Contains(body, "Choose what to pause") {
		t.Errorf("nothing chosen:\n%s", body)
	}

	h.post("/pause", url.Values{"scope": {"all"}}, h.srv.URL)
	if len(fm.paused) != 1 || !fm.paused[0].All || fm.pausedBy[0] != "u" {
		t.Fatalf("paused %+v by %v", fm.paused, fm.pausedBy)
	}
	_, body = h.get("/")
	if !strings.Contains(body, "Public access to everything is paused, since") || !strings.Contains(body, "Public access paused</span>") ||
		!strings.Contains(body, `href="/settings/public#paused"`) {
		t.Errorf("Home doesn't say it's paused:\n%s", body)
	}
	if _, body := h.get("/services/vaultwarden"); !strings.Contains(body, `id="paused"`) || !strings.Contains(body, "Everything public is paused.") ||
		!strings.Contains(body, "<dt>Built with</dt><dd>Rust and SQLite") {
		t.Errorf("the service's page doesn't say it's paused")
	}
	if _, body := h.get("/settings/public"); !strings.Contains(body, "Paused now") || !strings.Contains(body, "Everything public") {
		t.Errorf("Settings → Public access doesn't list it")
	}
	h.post("/pause/lift", url.Values{"all": {"yes"}, "next": {"/settings/public#paused"}}, h.srv.URL)
	if fm.all != nil || len(fm.lifted) != 1 {
		t.Errorf("not lifted: %+v", fm.lifted)
	}

	if _, body := h.get("/pause?scope=some&service=vaultwarden&next=/services/vaultwarden"); !strings.Contains(body, `name="next" value="/services/vaultwarden"`) ||
		!strings.Contains(body, `value="some" checked`) || !strings.Contains(body, `name="service" value="vaultwarden" checked`) {
		t.Errorf("from the service's page:\n%s", body)
	}
	if _, body := h.get("/pause?next=https://evil.example/"); !strings.Contains(body, `name="next" value="/"`) {
		t.Error("only a page of Roostack's own to go back to")
	}
	h.post("/pause", url.Values{"scope": {"some"}, "service": {"vaultwarden"}, "next": {"/services/vaultwarden"}}, h.srv.URL)
	if fm.own["vaultwarden"] == nil {
		t.Fatal("the service wasn't paused")
	}
	if _, body := h.get("/services/vaultwarden"); !strings.Contains(body, "Public access to Vaultwarden is paused.") ||
		!strings.Contains(body, `name="service" value="vaultwarden"`) || !strings.Contains(body, "Open it again") {
		t.Errorf("the service's own pause:\n%s", body)
	}
	h.post("/pause", url.Values{"scope": {"built"}, "built_with": {"rust"}}, h.srv.URL)
	if _, ok := fm.builtW["rust"]; !ok {
		t.Error("not paused by what it's built with")
	}

	entries, _ := h.store.AuditSince(ctx, 0)
	var got []string
	for _, e := range entries {
		if e.Action == attention.AuditPublicPaused || e.Action == attention.AuditPublicOpened {
			got = append(got, e.Action+": "+e.Detail)
		}
	}
	want := "public access paused: everything|public access opened: everything|public access paused: Vaultwarden|public access paused: services built with Rust"
	if strings.Join(got, "|") != want {
		t.Errorf("audit: %q", got)
	}
}

// Admins pause, since that only reduces what's reachable, but only the
// Owner opens it again; Members can do neither.
func TestPauseRoles(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	if code, _ := h.post("/pause", url.Values{"scope": {"all"}}, h.srv.URL); code != http.StatusOK || fm.all == nil {
		t.Errorf("an Admin pausing: %d", code)
	}
	if _, body := h.get("/"); !strings.Contains(body, "Public access to everything is paused") || strings.Contains(body, "Open it again…") {
		t.Error("an Admin sees the pause, without the Owner's link to open it again")
	}
	if code, _ := h.post("/pause/lift", url.Values{"all": {"yes"}}, h.srv.URL); code != http.StatusForbidden || fm.all == nil {
		t.Errorf("an Admin lifting: %d", code)
	}
	h, fm = loggedIn(t, auth.RoleMember)
	if code, _ := h.get("/pause"); code != http.StatusForbidden {
		t.Errorf("a Member's pause page: %d", code)
	}
	if code, _ := h.post("/pause", url.Values{"scope": {"all"}}, h.srv.URL); code != http.StatusForbidden || fm.all != nil {
		t.Errorf("a Member pausing: %d", code)
	}
}

// Changes waiting for a slow apply say so, in order, on Home and Services,
// and a pause whose web addresses have changed already says that too; the
// servers table says how many are waiting.
func TestApplyWaiting(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	fm.status = manager.ApplyStatus{Running: true, Doing: []node.Change{{Reason: "Installing immich"}},
		Waiting: []node.Change{{Reason: "Pausing public access to everything", Early: true}, {Reason: "Changing Vaultwarden's settings"}}}
	for _, page := range []string{"/", "/services"} {
		_, body := h.get(page)
		for _, want := range []string{"2 changes are waiting for the one being applied now (Installing immich) to finish.",
			"<li>Pausing public access to everything <small class=\"hint\">· its web addresses have changed already</small></li>",
			"<li>Changing Vaultwarden&#39;s settings</li>"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %q", page, want)
			}
		}
	}
	fm.status.Waiting = nil
	if _, body := h.get("/"); strings.Contains(body, "waiting for the one being applied") {
		t.Error("nothing waiting says nothing")
	}
}

// A security advisory (SPEC.md §6.16) Roostack paused a service for shows
// on Home, the service's page (with what to do, and Keep it public anyway
// for the Owner) and Settings → Public access, whose policy the Owner sets.
func TestAdvisoryPages(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	adv := &registry.Advisory{ID: "2026-0007", Title: "Vaultwarden lets anyone read every vault", Severity: registry.SeverityCritical,
		Exploited: true, Published: "2026-10-22", CVEs: []string{"CVE-2026-41234"}, Link: "https://example.org/notice"}
	fm.access = []manager.RouteAccess{{Route: "web", Host: "passwords.lab.example.com", Public: true, WAF: true}}
	fm.advisories = map[string][]*manager.ServiceAdvisory{"vaultwarden": {{Advisory: adv, Version: "1.34.3-roostack.1",
		Fixed: "1.34.4-roostack.1", Public: true, Paused: true}}}
	_, body := h.get("/")
	if !strings.Contains(body, "Roostack has paused public access to Vaultwarden because of a security problem that&#39;s being used in attacks") ||
		!strings.Contains(body, `href="/services/vaultwarden#advisory"`) {
		t.Errorf("Home:\n%s", body)
	}
	_, body = h.get("/services/vaultwarden")
	for _, want := range []string{`id="advisory"`, "Roostack has paused public access to Vaultwarden to keep it safe.", "(CVE-2026-41234)",
		"The registry rates it critical", "It's being used in attacks now.", "update it to 1.34.4, which fixes it",
		"Keep it public anyway…", `name="advisory" value="2026-0007"`, `href="#advisory">public access is paused`} {
		if !strings.Contains(body, want) {
			t.Errorf("the service's page lacks %q", want)
		}
	}
	if strings.Contains(body, `id="paused"`) {
		t.Error("the person's pause notice shows for an advisory's pause")
	}

	h.post("/services/vaultwarden/public-anyway", url.Values{"advisory": {"2026-0007"}}, h.srv.URL)
	if strings.Join(fm.keptPublic, ",") != "vaultwarden 2026-0007" {
		t.Errorf("kept: %v", fm.keptPublic)
	}
	_, body = h.get("/settings/public")
	for _, want := range []string{`id="advisories"`, `name="advisories" value="exploited" checked`,
		"By Roostack, for a security problem\n      (2026-0007)"} {
		if !strings.Contains(body, want) {
			t.Errorf("Settings → Public access lacks %q", want)
		}
	}
	h.post("/settings/public/advisories", url.Values{"advisories": {"critical"}}, h.srv.URL)
	if fm.advPolicy != "critical" {
		t.Errorf("policy %q", fm.advPolicy)
	}
	entries, _ := h.store.AuditSince(context.Background(), 0)
	var got []string
	for _, e := range entries {
		if e.Action == "public access" {
			got = append(got, e.Detail)
		}
	}
	if strings.Join(got, "|") != "vaultwarden kept public despite security advisory 2026-0007|security advisories: pause it for any critical problem" {
		t.Errorf("audit: %q", got)
	}

	h, fm = loggedIn(t, auth.RoleAdmin)
	fm.advisories = map[string][]*manager.ServiceAdvisory{"vaultwarden": {{Advisory: adv, Public: true, Paused: true}}}
	if _, body := h.get("/services/vaultwarden"); strings.Contains(body, "Keep it public anyway") {
		t.Error("only the Owner keeps it public")
	}
	if code, _ := h.post("/settings/public/advisories", url.Values{"advisories": {"tell"}}, h.srv.URL); code != http.StatusForbidden || fm.advPolicy != "" {
		t.Errorf("an Admin setting the policy: %d", code)
	}
	if code, _ := h.post("/services/vaultwarden/public-anyway", url.Values{"advisory": {"2026-0007"}}, h.srv.URL); code != http.StatusForbidden {
		t.Errorf("an Admin keeping it public: %d", code)
	}
}

// Closing public access completely (SPEC.md §6.16): its page says what
// will happen, and for an advisory about Roostack, which; Admins can
// close and only Owners open it again; Home and Settings → Public access
// say it's closed.
func TestClosePublic(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.roostack = []*manager.ServiceAdvisory{{Advisory: &registry.Advisory{ID: "2026-0009", Title: "Caddy lets anyone read any file",
		Severity: registry.SeverityCritical, Exploited: true}, Version: "0.4.1", Fixed: "0.4.2"}}
	if _, body := h.get("/pause"); !strings.Contains(body, `href="/pause/close"`) {
		t.Error("the pause page doesn't point to closing")
	}
	if _, body := h.get("/"); !strings.Contains(body, "security problem in Roostack 0.4.1 itself") || !strings.Contains(body, `href="/pause/close?advisory=2026-0009"`) {
		t.Errorf("Home doesn't ask to close:\n%s", body)
	}
	code, body := h.get("/pause/close?advisory=2026-0009")
	for _, want := range []string{"Close public access completely", "every server's firewall will refuse connections", "Close it now",
		`name="advisory" value="2026-0009"`, "Roostack 0.4.2 will fix it"} {
		if code != http.StatusOK || !strings.Contains(body, want) {
			t.Errorf("the close page lacks %q (%d)", want, code)
		}
	}
	if strings.Contains(body, "ssh -L") {
		t.Error("the tunnel's command at home")
	}
	// On the internet, Roostack's own pages close too: how to reach them.
	fs := h.server.Setup.(*fakeSetup)
	fs.network = config.NetworkInternet
	if _, body := h.get("/pause/close"); !strings.Contains(body, "Roostack's own pages will be closed too") ||
		!strings.Contains(body, "ssh -L 8480:127.0.0.1:8480 you@") {
		t.Errorf("on the internet:\n%s", body)
	}
	fs.network = ""

	a, fa := loggedIn(t, auth.RoleAdmin)
	a.post("/pause/close", url.Values{"advisory": {"2026-0009"}}, a.srv.URL)
	if fa.closed == nil || fa.closed.Advisory != "2026-0009" || fa.pausedBy[0] != "u" {
		t.Fatalf("an Admin closing: %+v", fa.closed)
	}
	if code, _ := a.post("/pause/lift", url.Values{"closed": {"yes"}}, a.srv.URL); code == http.StatusOK && fa.closed == nil {
		t.Error("an Admin opened it again")
	}

	h.post("/pause/close", nil, h.srv.URL)
	if _, body := h.get("/"); !strings.Contains(body, "Public access is closed completely, since") || strings.Contains(body, "security problem in Roostack") {
		t.Errorf("Home while closed:\n%s", body)
	}
	if _, body := h.get("/settings/public"); !strings.Contains(body, "Closed completely") || !strings.Contains(body, "can't connect at all") ||
		!strings.Contains(body, `name="closed" value="yes"`) {
		t.Errorf("Settings → Public access while closed:\n%s", body)
	}
	if _, body := h.get("/pause/close"); !strings.Contains(body, "Public access is closed completely</strong>") || strings.Contains(body, "Close it now") {
		t.Error("the close page while closed")
	}
	h.post("/pause/lift", url.Values{"closed": {"yes"}, "next": {"/settings/public#paused"}}, h.srv.URL)
	if fm.closed != nil {
		t.Error("not opened again")
	}
}
