package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/units"
)

// A server that can't be reached offers "is gone"; its page lists its
// services with their backups, all ticked when they fit.
func TestServerGone(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true, Reachable: true},
		{ID: "n-9876543210", Name: "nas", Services: []string{"photos", "jellyfin"}}}
	moment := manager.BackupMoment{ServerID: "n-9876543210", ServerName: "nas", Time: time.Now().Add(-time.Hour),
		Places: []manager.ServiceBackup{{Where: "Backblaze", Snapshot: backup.Snapshot{ID: "aa"}}}}
	fm.lostPlan = &manager.LostPlan{Server: fm.servers[1],
		Services: []manager.LostService{{Name: "photos", Title: "Photos", Backup: &moment, Size: 180 * units.GB},
			{Name: "jellyfin", Title: "Jellyfin", Size: -1}},
		Targets: fm.servers[:1], Free: map[string]int64{"n-0123456789": 412 * units.GB}}

	if _, body := h.get("/servers"); !strings.Contains(body, `href="/settings/servers/n-9876543210/gone">nas is gone`) ||
		strings.Contains(body, `href="/settings/servers/n-0123456789/gone"`) {
		t.Error("only a server that can't be reached offers it")
	}
	_, body := h.get("/settings/servers/n-9876543210/gone")
	for _, want := range []string{`value="photos" data-size="180000000000" checked`, "(read from Backblaze (copy))",
		`value="jellyfin" data-size="-1" disabled`, "No backup of it can be read", `data-free="412000000000">nuc</option>`} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q", want)
		}
	}
	// Too big for the free space: none ticked, for the Owner to choose.
	fm.lostPlan.Free["n-0123456789"] = 100 * units.GB
	if _, body := h.get("/settings/servers/n-9876543210/gone"); strings.Contains(body, " checked") {
		t.Error("services that don't fit shouldn't be ticked")
	}
	if code, _ := h.get("/settings/servers/n-1111111111/gone"); code != http.StatusOK && code != http.StatusSeeOther {
		t.Errorf("unknown server: %d", code)
	}

	if code, _ := h.post("/settings/servers/n-9876543210/gone", url.Values{"service": {"photos"}, "to": {"n-0123456789"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("unconfirmed: %d", code)
	}
	h.post("/settings/servers/n-9876543210/gone", url.Values{"service": {"photos"}, "to": {"n-0123456789"}, "confirm": {"on"}}, h.srv.URL)
	if len(fm.labRestores) != 1 || fm.labRestores[0] != "gone n-9876543210: photos → n-0123456789" {
		t.Errorf("restores %v", fm.labRestores)
	}

	// Admins can't.
	ha, fa := loggedIn(t, auth.RoleAdmin)
	fa.lostPlan = fm.lostPlan
	if code, _ := ha.get("/settings/servers/n-9876543210/gone"); code != http.StatusForbidden {
		t.Errorf("admin: %d", code)
	}
}

// A service waiting to be restored says so everywhere, and can be
// restored or removed with its backups.
func TestWaitingService(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.node().restores = &fakeRestores{}
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "vaultwarden", Title: "Vaultwarden", Status: StatusWaiting, WasOn: "nas"},
			{Name: "forgejo", Title: "Forgejo", Status: "running"}}, nil
	}
	_, body := h.get("/")
	if !strings.Contains(body, "Vaultwarden is waiting to be restored (it was on nas).") {
		t.Error("home should say a service waits")
	}
	_, body = h.get("/backups")
	if i := strings.Index(body, `<span class="pill pill-warn">Waiting to be restored</span> <span class="hint">was on nas</span>`); i < 0 ||
		!strings.Contains(body[i:min(len(body), i+800)], `<a class="table-action" href="/backups/restore/vaultwarden">`) {
		t.Error("Restoring should list it first, as waiting")
	}
	_, body = h.get("/services/vaultwarden")
	if !strings.Contains(body, "Vaultwarden isn&#39;t running: its server, nas, is gone.") ||
		!strings.Contains(body, "/services/vaultwarden/remove-waiting") || strings.Contains(body, `href="/services/vaultwarden/remove"`) {
		t.Error("its page should say so, and offer removing it with its backups")
	}
	h.post("/services/vaultwarden/remove-waiting", nil, h.srv.URL)
	if len(fm.labRestores) != 1 || fm.labRestores[0] != "remove waiting vaultwarden" {
		t.Errorf("removed %v", fm.labRestores)
	}
}
