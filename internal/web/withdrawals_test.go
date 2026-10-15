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
	"roostack.cloud/roostack/internal/registry"
)

func (f *fakeManager) Withdrawals(context.Context) map[string]*manager.Withdrawal { return f.withdrawn }
func (f *fakeManager) StartAnyway(_ context.Context, name string, private bool) error {
	w := f.withdrawn[name]
	if w == nil {
		return manager.ErrNotFound
	}
	f.startedAnyway = append(f.startedAnyway, name+map[bool]string{true: " private", false: ""}[private])
	w.Stopped, w.StartedAnyway, w.Shown = false, true, registry.LevelWarning
	return nil
}
func (f *fakeManager) StopsOn() bool { return !f.stopsOff }
func (f *fakeManager) SetStops(on bool) error {
	f.stopsOff = !on
	return nil
}

func withdrawalServices(context.Context) ([]ServiceView, error) {
	return []ServiceView{{Name: "immich", Title: "Immich", Status: StatusRegistryStop, URLs: []string{"https://photos.example.org"}},
		{Name: "forgejo", Title: "Forgejo", Status: "running"}, {Name: "vaultwarden", Title: "Vaultwarden", Status: "running"}}, nil
}

// A stopped service says why, who said so and what to do, on its page and
// under "Needs attention"; the Owner can start it again, making it
// private first.
func TestRegistryStop(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.server.Services = withdrawalServices
	fm.withdrawn = map[string]*manager.Withdrawal{
		"immich": {Withdrawal: registry.Withdrawal{Level: registry.LevelStop, Reason: "Being exploited; no fix yet.",
			Link: "https://example.org/advisory"}, Version: "2.1.0-roostack.1", Shown: registry.LevelStop, Stopped: true,
			Since: time.Now().Add(-time.Hour), Public: true},
		"forgejo":     {Withdrawal: registry.Withdrawal{Level: registry.LevelNotice, Reason: "Run a repository check after updating"}, Version: "15.0.9-roostack.1", Shown: registry.LevelNotice},
		"vaultwarden": {Withdrawal: registry.Withdrawal{Level: registry.LevelWarning, Reason: "Loses uploads"}, Version: "1.34.3-roostack.1", Shown: registry.LevelWarning},
	}
	_, body := h.get("/")
	for _, want := range []string{
		"Roostack stopped Immich: the service registry says the version it has is dangerous.</strong><span>Its data is kept.",
		"The version of Vaultwarden you have has been withdrawn by the service registry: Loses uploads.",
		"The service registry has a notice about Forgejo 15.0.9-roostack.1: Run a repository check after updating.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing %q", want)
		}
	}
	_, body = h.get("/services/immich")
	for _, want := range []string{"Roostack stopped Immich to protect your lab.", "“Being exploited; no fix yet.”",
		`href="https://example.org/advisory"`, "Stopped by Roostack", "off while it's stopped",
		`action="/services/immich/start-anyway"`, `name="private" value="1" checked`, "Start Immich anyway? The service registry says",
		`href="/services/immich/remove">remove it</a>`} {
		if !strings.Contains(body, want) {
			t.Errorf("service page missing %q", want)
		}
	}
	_, body = h.get("/services/vaultwarden")
	if !strings.Contains(body, "It keeps running, but it can't be installed any more.") || strings.Contains(body, "start-anyway") {
		t.Error("a warning says it keeps running, with nothing to start")
	}

	// Admins see it, but only the Owner can start it.
	ha, fa := loggedIn(t, auth.RoleAdmin)
	ha.server.Services, fa.withdrawn = withdrawalServices, fm.withdrawn
	if _, body := ha.get("/services/immich"); strings.Contains(body, "start-anyway") || !strings.Contains(body, "Roostack stopped Immich") {
		t.Error("admins see the stop without starting it")
	}
	if code, _ := ha.post("/services/immich/start-anyway", url.Values{}, ha.srv.URL); code != http.StatusForbidden {
		t.Errorf("admin start: %d", code)
	}

	h.post("/services/immich/start-anyway", url.Values{"private": {"1"}}, h.srv.URL)
	if len(fm.startedAnyway) != 1 || fm.startedAnyway[0] != "immich private" {
		t.Fatalf("started %v", fm.startedAnyway)
	}
	if _, body := h.get("/"); !strings.Contains(body, "Immich is running a version the service registry says is dangerous: you started it anyway.") {
		t.Error("started anyway stays under Needs attention")
	}
}

// With stops turned off, a stop is a warning that says so; the switch is
// on Settings → Updates.
func TestRegistryStopsOff(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.server.Services = withdrawalServices
	fm.withdrawn = map[string]*manager.Withdrawal{"immich": {Withdrawal: registry.Withdrawal{Level: registry.LevelStop, Reason: "Bad"},
		Version: "2.1.0-roostack.1", Shown: registry.LevelWarning, NoStops: true}}
	if _, body := h.get("/"); !strings.Contains(body, "Roostack would stop it, but you&#39;ve turned that off.") {
		t.Error("a stop that's off is a warning saying so")
	}
	if _, body := h.get("/services/immich"); !strings.Contains(body, "Roostack would stop it, but that's turned off") {
		t.Error("service page says stops are off")
	}
	h.post("/settings/updates/registry/stops", url.Values{}, h.srv.URL)
	if !fm.stopsOff {
		t.Error("unticked turns stops off")
	}
	h.post("/settings/updates/registry/stops", url.Values{"on": {"1"}}, h.srv.URL)
	if fm.stopsOff {
		t.Error("ticked turns them on")
	}
}
