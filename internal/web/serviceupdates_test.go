package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
)

func (f *fakeManager) AvailableUpdates(context.Context) map[string]*manager.Update {
	return f.svcUpdates
}
func (f *fakeManager) UpdateService(_ context.Context, name, to, by string, acceptRisks bool) error {
	u := f.svcUpdates[name]
	if u == nil {
		return manager.ErrNotFound
	}
	if u.To != to {
		return &manager.FormError{Msg: "The update on offer has changed."}
	}
	if len(u.NewRisks) > 0 && !acceptRisks {
		return &manager.FormError{Msg: "Tick the box."}
	}
	f.svcUpdated = append(f.svcUpdated, name+" "+to+" by "+by)
	return nil
}
func (f *fakeManager) Updating(name string) *manager.ServiceUpdate { return f.updating[name] }
func (f *fakeManager) LastUpdate(_ context.Context, name string) *manager.UpdateRecord {
	return f.lastUpdate[name]
}
func (f *fakeManager) UpdatePolicy(name string) string {
	if p := f.policies[name]; p != "" {
		return p
	}
	return "notify"
}
func (f *fakeManager) SetUpdatePolicy(_ context.Context, name, policy string) error {
	if f.policies == nil {
		f.policies = map[string]string{}
	}
	f.policies[name] = policy
	return nil
}

func updateServices(context.Context) ([]ServiceView, error) {
	return []ServiceView{{Name: "immich", Title: "Immich", Status: "running", Update: "2.1.0"},
		{Name: "vaultwarden", Title: "Vaultwarden", Status: "running", Update: "1.36.1"}}, nil
}

// An update on offer shows on the service page with its notes and under
// "Needs attention"; an Admin starts it, and chooses the update policy.
func TestServiceUpdateOffered(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	h.server.Services = updateServices
	fm.svcUpdates = map[string]*manager.Update{
		"vaultwarden": {Service: "vaultwarden", Title: "Vaultwarden", From: "1.36.0-roostack.1", To: "1.36.1-roostack.1",
			Kind: pkgspec.UpdatePatch, Policy: "notify"},
		"immich": {Service: "immich", Title: "Immich", From: "1.9.4-roostack.1", To: "2.1.0-roostack.1", Kind: pkgspec.UpdateMajor,
			Policy: "minor", Notes: []pkgspec.UpgradeNote{{Version: "2.0.0-roostack.1", Note: "Faces are rebuilt."}},
			WhatsNew: "https://github.com/immich-app/immich/releases/tag/v2.1.0"},
	}
	_, body := h.get("/")
	if !strings.Contains(body, "Updates are available for Immich (2.1.0) and Vaultwarden (1.36.1).") {
		t.Error("home lists the updates in one line")
	}
	_, body = h.get("/services/vaultwarden")
	for _, want := range []string{"An update is available:</strong> Vaultwarden 1.36.0 → 1.36.1, a fix release.",
		`action="/services/vaultwarden/update"`, `name="to" value="1.36.1-roostack.1"`, ">Update now</button>",
		"You chose to be told about updates and apply them yourself.", "<dd>Tell me, and I&#39;ll update it</dd>"} {
		if !strings.Contains(body, want) {
			t.Errorf("service page missing %q", want)
		}
	}
	if _, body = h.get("/services/vaultwarden/settings"); !strings.Contains(body, `value="notify" checked`) {
		t.Error("its settings page should offer the update choices")
	}
	_, body = h.get("/services/immich")
	for _, want := range []string{"a new major version. <a href=\"https://github.com/immich-app/immich/releases/tag/v2.1.0\" target=\"_blank\" rel=\"noopener\">What's new in 2.1.0",
		"</a> Read these first:", "<strong>2.0.0:</strong> Faces are rebuilt.",
		"Update Immich to 2.1.0? Roostack backs it up first", "It has notes to read, so Roostack waits for you."} {
		if !strings.Contains(body, want) {
			t.Errorf("immich page missing %q", want)
		}
	}
	h.post("/services/vaultwarden/update", url.Values{"to": {"1.36.1-roostack.1"}}, h.srv.URL)
	if len(fm.svcUpdated) != 1 || fm.svcUpdated[0] != "vaultwarden 1.36.1-roostack.1 by u" {
		t.Errorf("updated %v", fm.svcUpdated)
	}
	if code, body := h.post("/services/vaultwarden/update", url.Values{"to": {"1.36.0-roostack.9"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "The update on offer has changed.") {
		t.Errorf("a stale update is a form error: %d", code)
	}
	h.post("/services/vaultwarden/updates", url.Values{"updates": {"patch"}}, h.srv.URL)
	if fm.policies["vaultwarden"] != "patch" {
		t.Errorf("policy %v", fm.policies)
	}
	if code, _ := h.post("/services/vaultwarden/updates", url.Values{"updates": {"sometimes"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("unknown policy: %d", code)
	}

	// Members can't update.
	hm, fmm := loggedIn(t, auth.RoleMember)
	hm.server.Services, fmm.svcUpdates = updateServices, fm.svcUpdates
	if code, _ := hm.post("/services/vaultwarden/update", url.Values{"to": {"1.36.1-roostack.1"}}, hm.srv.URL); code != http.StatusForbidden {
		t.Errorf("member update: %d", code)
	}
}

// Updating shows its steps; a failed update says Roostack went back,
// offers the backup from before it, and is a problem on Home.
func TestServiceUpdateProgressAndFailure(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.server.Services = updateServices
	fm.updating = map[string]*manager.ServiceUpdate{"vaultwarden": {To: "1.36.1-roostack.1", Progress: &node.InstallProgress{
		Steps: []node.InstallStep{{Key: "download", Title: "Downloading the new version", Started: time.Now(), Finished: time.Now()},
			{Key: "switch", Title: "Switching to 1.36.1", Started: time.Now()}}}}}
	_, body := h.get("/services/vaultwarden")
	for _, want := range []string{"Updating Vaultwarden to 1.36.1.", "Switching to 1.36.1…", `hx-get="/services/vaultwarden?updating=1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("updating page missing %q", want)
		}
	}
	fm.updating = nil
	fm.svcUpdates = map[string]*manager.Update{"immich": {Service: "immich", Title: "Immich", From: "1.9.4-roostack.1", To: "2.1.0-roostack.1",
		Kind: pkgspec.UpdateMajor, Policy: "minor", FailedBefore: true}}
	fm.lastUpdate = map[string]*manager.UpdateRecord{"immich": {From: "1.9.4-roostack.1", To: "2.1.0-roostack.1", Result: manager.UpdateRolledBack,
		Error: "It still wasn't working after 5 minutes: immich-server has stopped", Started: time.Now().Add(-time.Hour), Finished: time.Now(),
		Backup: time.Now().Add(-time.Hour)}}
	_, body = h.get("/services/immich")
	for _, want := range []string{"Updating Immich to 2.1.0 didn't work, so Roostack went back to 1.9.4.", "immich-server has stopped",
		`href="/backups/restore/immich">put back the backup from before the update`, ">Try again</button>"} {
		if !strings.Contains(body, want) {
			t.Errorf("failed update missing %q", want)
		}
	}
	if _, body := h.get("/"); !strings.Contains(body, "Updating Immich to 2.1.0 didn&#39;t work; Roostack went back to 1.9.4.") {
		t.Error("a failed update is on Home")
	}
}

// A withdrawn version with a fix on offer: the withdrawal says to update,
// with the button in it, and Home doesn't list the update twice.
func TestServiceUpdateWithdrawn(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.server.Services = updateServices
	fm.withdrawn = map[string]*manager.Withdrawal{"immich": {Withdrawal: registry.Withdrawal{Level: registry.LevelStop, Reason: "Exploited"},
		Version: "1.9.4-roostack.1", Shown: registry.LevelStop, Stopped: true}}
	fm.svcUpdates = map[string]*manager.Update{"immich": {Service: "immich", Title: "Immich", From: "1.9.4-roostack.1", To: "1.9.5-roostack.1",
		Kind: pkgspec.UpdatePatch, Policy: "notify"}}
	_, body := h.get("/services/immich")
	for _, want := range []string{"update it to 1.9.5, which the service registry offers instead", ">Update now</button>"} {
		if !strings.Contains(body, want) {
			t.Errorf("withdrawn page missing %q", want)
		}
	}
	if strings.Contains(body, "An update is available") {
		t.Error("the update is in the withdrawal's notice, not a second one")
	}
	if _, body := h.get("/"); strings.Contains(body, "An update is available for Immich") {
		t.Error("Home doesn't list a withdrawn service's update again")
	}
}

// A new package revision of the same app version names the revisions,
// so the two versions never read the same.
func TestServiceUpdateRevision(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	h.server.Services = updateServices
	fm.svcUpdates = map[string]*manager.Update{"vaultwarden": {Service: "vaultwarden", Title: "Vaultwarden",
		From: "1.34.3-roostack.1", To: "1.34.3-roostack.2", Kind: pkgspec.UpdateRevision, Policy: "patch", Auto: true}}
	_, body := h.get("/services/vaultwarden")
	for _, want := range []string{"an improved Roostack package for Vaultwarden 1.34.3", "(package revision 1 → 2)",
		"Roostack updates it by itself tonight, after the nightly backup."} {
		if !strings.Contains(body, want) {
			t.Errorf("revision page missing %q", want)
		}
	}
}

// A failed update stays news while its version is on offer; a newer
// offer replaces it, and with nothing on offer it goes after a week.
func TestLastUpdateShown(t *testing.T) {
	failed := &manager.UpdateRecord{To: "2.1.0-roostack.1", Result: manager.UpdateRolledBack, Finished: time.Now().Add(-30 * 24 * time.Hour)}
	if !lastUpdateShown(failed, &manager.Update{To: "2.1.0-roostack.1"}) {
		t.Error("still on offer: shown")
	}
	if lastUpdateShown(failed, &manager.Update{To: "2.1.1-roostack.1"}) || lastUpdateShown(failed, nil) {
		t.Error("replaced, or old with nothing on offer: not shown")
	}
	failed.Finished = time.Now()
	if !lastUpdateShown(failed, nil) {
		t.Error("recent with nothing on offer: shown")
	}
}

// A newer version of the Owner's own package that asks for more lists
// what, and only the Owner can accept it; an Admin is told so.
func TestServiceUpdateNewRisks(t *testing.T) {
	ups := map[string]*manager.Update{"vaultwarden": {Service: "vaultwarden", Title: "Vaultwarden", Package: "vaultwarden",
		From: "1.34.3-roostack.1", To: "1.34.4-roostack.1", Kind: pkgspec.UpdatePatch, Policy: "notify", Local: true,
		NewRisks: []string{"containers.app.image: pin the image by digest"}}}
	h, fm := loggedIn(t, auth.RoleOwner)
	h.server.Services, fm.svcUpdates = updateServices, ups
	_, body := h.get("/services/vaultwarden")
	for _, want := range []string{"nobody has reviewed it", "1.34.4 asks for things the version it runs didn",
		"containers.app.image: pin the image by digest", `name="accept_risks" required`} {
		if !strings.Contains(body, want) {
			t.Errorf("owner page missing %q", want)
		}
	}
	h.post("/services/vaultwarden/update", url.Values{"to": {"1.34.4-roostack.1"}, "accept_risks": {"on"}}, h.srv.URL)
	if len(fm.svcUpdated) != 1 {
		t.Errorf("the Owner accepted: %v", fm.svcUpdated)
	}

	ha, fa := loggedIn(t, auth.RoleAdmin)
	ha.server.Services, fa.svcUpdates = updateServices, ups
	_, body = ha.get("/services/vaultwarden")
	if !strings.Contains(body, "Only the Owner can update it") || strings.Contains(body, `action="/services/vaultwarden/update"`) {
		t.Error("an admin can't accept the risks")
	}
	if code, _ := ha.post("/services/vaultwarden/update", url.Values{"to": {"1.34.4-roostack.1"}, "accept_risks": {"on"}}, ha.srv.URL); code != http.StatusBadRequest {
		t.Errorf("an admin's accept doesn't count: %d", code)
	}
}
