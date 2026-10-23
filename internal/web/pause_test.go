package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/auth"
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
		`name="built_with" value="rust"`, `name="service" value="vaultwarden"`, "Pause now"} {
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
	if _, body := h.get("/services/vaultwarden"); !strings.Contains(body, `id="paused"`) || !strings.Contains(body, "Public access to everything is paused too.") {
		t.Errorf("the service's page doesn't say it's paused")
	}
	if _, body := h.get("/settings/public"); !strings.Contains(body, "Paused now") || !strings.Contains(body, "Everything public") {
		t.Errorf("Settings → Public access doesn't list it")
	}
	h.post("/pause/lift", url.Values{"all": {"yes"}, "next": {"/settings/public#paused"}}, h.srv.URL)
	if fm.all != nil || len(fm.lifted) != 1 {
		t.Errorf("not lifted: %+v", fm.lifted)
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
	want := "public access paused: everything|public access opened: everything|public access paused: Vaultwarden|public access paused: services built with rust"
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
