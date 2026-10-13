package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/store"
)

// History lists what Roostack did (with its log) and what people did,
// newest first, filtered by kind; how long it's kept is chosen, and it
// can all be deleted, which is recorded.
func TestHistory(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	ctx := context.Background()
	now := time.Now()
	fm.events = []store.Event{
		{ID: 7, Started: now.Add(-2 * time.Hour), Finished: now.Add(-2*time.Hour + 30*time.Second), Kind: activity.Apply,
			Title: "Applying changes", Status: "Started forgejo", Output: "Why: Installing forgejo.\n==> Done"},
		{ID: 8, Started: now.Add(-time.Hour), Finished: now.Add(-time.Hour), Kind: activity.Backup, Title: "Backup",
			Error: "couldn't reach vps's disk in 2m0s"},
		{ID: 9, Started: now.AddDate(0, 0, -100), Finished: now.AddDate(0, 0, -100), Kind: activity.Backup, Title: "Old backup"},
	}
	u, _ := h.store.UserByUsername(ctx, "u")
	h.store.Audit(ctx, u, "install", "forgejo")

	_, body := h.get("/settings/history")
	for _, want := range []string{"<strong>Applying changes</strong>", "took 30s · Started forgejo", "dot-problem",
		"couldn&#39;t reach vps&#39;s disk", "<strong>install</strong> <small class=\"hint\">by u</small>", `href="/settings/history/n-0123456789/7">Log</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("history missing %q", want)
		}
	}
	if strings.Index(body, "<strong>install</strong>") > strings.Index(body, "<strong>Backup</strong>") {
		t.Error("newest first")
	}
	if _, body = h.get("/settings/history?kind=backup"); strings.Contains(body, "Applying changes</strong>") || strings.Contains(body, "<strong>install") {
		t.Error("filtered to backups")
	}
	if _, body = h.get("/settings/history/n-0123456789/7"); !strings.Contains(body, "Why: Installing forgejo.") {
		t.Errorf("log: %s", body)
	}
	if code, _ := h.get("/settings/history/n-0123456789/99"); code != http.StatusNotFound {
		t.Errorf("unknown event: %d", code)
	}

	// Keeping 90 days drops the 100-day-old backup.
	h.post("/settings/history/keep", url.Values{"days": {"90"}}, h.srv.URL)
	if len(fm.events) != 2 || h.server.historyKeepDays(ctx) != 90 {
		t.Errorf("kept: %d events", len(fm.events))
	}
	if code, _ := h.post("/settings/history/keep", url.Values{"days": {"7"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("not a choice: %d", code)
	}
	_, body = h.post("/settings/history/delete", nil, h.srv.URL)
	if len(fm.events) != 0 || !strings.Contains(body, "Deleted all history") || !strings.Contains(body, "deleted all history") {
		t.Errorf("delete: %d events left", len(fm.events))
	}

	ha, _ := loggedIn(t, auth.RoleAdmin)
	if code, _ := ha.get("/settings/history"); code != http.StatusForbidden {
		t.Errorf("admins: %d", code)
	}
}
