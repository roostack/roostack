package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
)

// The Updates page (SPEC.md §7.1): every service with an update, fix
// releases ticked; one with notes needs them read, one that can't update
// says why; chosen ones update now or wait for tonight, which can be
// cancelled; the services list links to it.
func TestUpdatesPage(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "immich", Title: "Immich", Status: "running", Update: "2.1.0"},
			{Name: "vaultwarden", Title: "Vaultwarden", Status: "running", Update: "1.36.1"},
			{Name: "pingvin", Title: "Pingvin Share", Status: StatusOff, Update: "1.13.2"}}, nil
	}
	fm.svcUpdates = map[string]*manager.Update{
		"vaultwarden": {Service: "vaultwarden", Title: "Vaultwarden", From: "1.36.0-roostack.1", To: "1.36.1-roostack.1",
			Kind: pkgspec.UpdatePatch, Policy: "notify"},
		"immich": {Service: "immich", Title: "Immich", From: "1.9.4-roostack.1", To: "2.1.0-roostack.1", Kind: pkgspec.UpdateMajor,
			Policy: "minor", Notes: []pkgspec.UpgradeNote{{Version: "2.0.0-roostack.1", Note: "Faces are rebuilt."}}},
		"pingvin": {Service: "pingvin", Title: "Pingvin Share", From: "1.13.0-roostack.1", To: "1.13.2-roostack.1", Kind: pkgspec.UpdatePatch,
			Blocked: "Turn Pingvin Share on first: updating will start it to check that the new version works."},
	}

	_, body := h.get("/services")
	for _, want := range []string{"2 services have an update:</strong>", "Immich and Vaultwarden. <a href=\"/updates\">See the updates</a>",
		`<a href="/updates#vaultwarden">`} {
		if !strings.Contains(body, want) {
			t.Errorf("services list missing %q", want)
		}
	}
	_, body = h.get("/updates")
	for _, want := range []string{
		`value="vaultwarden" aria-label="Update Vaultwarden"`, `data-kind="patch" checked>`,
		`data-notes="notes-immich">`, `name="read.immich"`, "Before updating Immich to 2.1.0", "<strong>2.0.0:</strong> Faces are rebuilt.",
		`<tr id="pingvin" class="unchoosable">`, "Turn Pingvin Share on first", "New major version", "Fix release",
		"No: you chose to be told", "No: it has notes to read",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("updates page missing %q", want)
		}
	}
	if strings.Contains(body, `data-notes="notes-immich" checked`) {
		t.Error("an update with notes isn't ticked when the page opens")
	}

	// Now: notes must be read.
	if code, body := h.post("/updates", url.Values{"update": {"vaultwarden", "immich"}, "to.vaultwarden": {"1.36.1-roostack.1"},
		"to.immich": {"2.1.0-roostack.1"}, "when": {"now"}}, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "Read the notes for Immich first.") {
		t.Errorf("notes not read: %d", code)
	}
	h.post("/updates", url.Values{"update": {"vaultwarden", "immich"}, "to.vaultwarden": {"1.36.1-roostack.1"},
		"to.immich": {"2.1.0-roostack.1"}, "read.immich": {"1"}, "when": {"now"}}, h.srv.URL)
	if strings.Join(fm.svcUpdated, "; ") != "vaultwarden 1.36.1-roostack.1 by u; immich 2.1.0-roostack.1 by u" {
		t.Errorf("updated %v", fm.svcUpdated)
	}

	// Running: the page polls, and reloads once the list changes.
	fm.updating = map[string]*manager.ServiceUpdate{"vaultwarden": {Progress: &node.InstallProgress{}, Waiting: "Waiting its turn: 2 of 2"}}
	_, body = h.get("/updates")
	if !strings.Contains(body, `hx-get="/updates/live?updating=vaultwarden"`) || !strings.Contains(body, "waiting its turn: 2 of 2") {
		t.Error("updates in progress poll")
	}
	if resp := h.poll("/updates/live?updating=vaultwarden"); resp.Header.Get("HX-Refresh") != "" {
		t.Error("no change: no reload")
	}
	// Switched to the new version but still being checked: still listed.
	fm.updating["gokapi"] = &manager.ServiceUpdate{From: "2.2.4-roostack.2", To: "2.2.4-roostack.3", Progress: &node.InstallProgress{}}
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "gokapi", Title: "Gokapi", Status: "updating"}, {Name: "vaultwarden", Title: "Vaultwarden", Status: "running", Update: "1.36.1"}}, nil
	}
	if _, body = h.get("/updates"); !strings.Contains(body, `updating=gokapi,vaultwarden`) || !strings.Contains(body, "<strong>Gokapi</strong> 2.2.4, package revision 2 → 2.2.4, package revision 3") {
		t.Error("an update being checked stays listed")
	}
	delete(fm.updating, "gokapi")
	fm.updating = nil
	if resp := h.poll("/updates/live?updating=vaultwarden"); resp.Header.Get("HX-Refresh") != "true" {
		t.Error("finished: the page reloads")
	}

	// Tonight, and cancelling it.
	h.post("/updates", url.Values{"update": {"vaultwarden"}, "to.vaultwarden": {"1.36.1-roostack.1"}, "when": {"tonight"}}, h.srv.URL)
	if fm.tonight["vaultwarden"].By != "u" {
		t.Fatalf("tonight %v", fm.tonight)
	}
	_, body = h.get("/updates")
	if !strings.Contains(body, "Roostack will update Vaultwarden tonight</strong>") || !strings.Contains(body, `form="cancel-vaultwarden">Cancel`) {
		t.Error("tonight's shown, with Cancel")
	}
	if _, body = h.get("/services/vaultwarden"); !strings.Contains(body, "Roostack will update it tonight after the nightly backup, as u chose") {
		t.Error("the service page says it's tonight")
	}
	h.post("/updates/cancel", url.Values{"service": {"vaultwarden"}}, h.srv.URL)
	if len(fm.tonight) != 0 {
		t.Errorf("cancelled: %v", fm.tonight)
	}

	// Members can't see it.
	hm, fmm := loggedIn(t, auth.RoleMember)
	fmm.svcUpdates = fm.svcUpdates
	if code, _ := hm.get("/updates"); code != http.StatusForbidden {
		t.Errorf("member: %d", code)
	}
}
