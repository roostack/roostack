package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
)

func fresh() registry.Status {
	now := time.Now()
	return registry.Status{URL: registry.DefaultURL, Checked: now, Fetched: now, Serial: now.Unix(), Date: now.Add(-time.Hour),
		Commit: "8eb8a2e29d95c8266526261d391151a8117387ea", Packages: 3, KeysSerial: 1,
		Skipped: map[string]string{"jellyfin": "needs Roostack 0.9 or newer"}}
}

// The catalog checks the registry when opened, and says when its list is
// missing, old, or turned off.
func TestCatalogRegistry(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	reg := &fakeRegistry{on: true, status: fresh()}
	h.server.Registry = reg

	_, body := h.get("/catalog")
	if reg.opened != 1 || !strings.Contains(body, `href="/install/immich">Install`) || strings.Contains(body, "service registry") {
		t.Errorf("fresh list: checked %d times; body:\n%s", reg.opened, body)
	}

	reg.status.Date = time.Now().Add(-20 * 24 * time.Hour)
	if _, body := h.get("/catalog"); !strings.Contains(body, "doesn&#39;t know of fixes or withdrawn versions") ||
		!strings.Contains(body, `href="/install/immich">Install`) {
		t.Error("an old list should be offered with a warning")
	}

	reg.status = registry.Status{Checked: time.Now(), Error: "dial tcp: no route to host"}
	if _, body := h.get("/catalog"); !strings.Contains(body, "can&#39;t get the list of services") {
		t.Error("no list: the catalog should say why")
	}

	// Turned off, nothing is offered, and installing goes back to the
	// catalog, which says why.
	reg.on, reg.status = false, fresh()
	if _, body := h.get("/catalog"); strings.Contains(body, `/install/immich`) || !strings.Contains(body, "turned off") {
		t.Error("turned off: the catalog should offer nothing, and say why")
	}
	if _, body := h.get("/install/immich"); !strings.Contains(body, "turned off") || strings.Contains(body, `name="subdomain"`) {
		t.Error("turned off: installing should go back to the catalog")
	}
}

// The catalog shows each service's logo (or its category's icon), tier
// and categories, with filters for the categories in use; one needing a
// newer Roostack is listed, not offered (SPEC.md §8.3.6).
func TestCatalogListing(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	reg := &fakeRegistry{on: true, status: fresh(), listing: map[string]registry.Listed{
		"immich":      {Package: &registry.Package{Title: "Immich", Tier: registry.TierOfficial}, Icon: true},
		"vaultwarden": {Package: &registry.Package{Title: "Vaultwarden", Tier: registry.TierCommunity}},
		"paperless": {Package: &registry.Package{Title: "Paperless-ngx", Description: "Files your paper.", Tier: registry.TierOfficial,
			Categories: []string{"documents"}}, NeedsRoostack: "0.9"},
		"jellyfin": {Package: &registry.Package{Title: "Jellyfin", Tier: registry.TierOfficial}}, // withdrawn, say
	}}
	h.server.Registry = reg

	_, body := h.get("/catalog")
	immich, paperless, vault := strings.Index(body, "<h2>Immich"), strings.Index(body, "<h2>Paperless-ngx"), strings.Index(body, "<h2>Vaultwarden")
	if immich < 0 || paperless < immich || vault < paperless {
		t.Errorf("not listed by title: %d %d %d", immich, paperless, vault)
	}
	for _, want := range []string{
		`<span class="logo-tile"><img src="/install/immich/icon.png"`,          // its logo
		`data-categories="photos"`, "Photos and videos", "Documents and notes", // filters, and each one's
		`<span class="badge">Official</span>`, `<span class="badge badge-warn">Community</span>`,
		`<span class="badge badge-ok">Installed</span>`,
		"Needs a newer Roostack: 0.9 or newer.", "Files your paper.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the catalog is missing %q", want)
		}
	}
	if strings.Contains(body, "/install/paperless") || strings.Contains(body, "Jellyfin") {
		t.Error("only packages the lab can install, or could after updating Roostack, are listed; and only those offered")
	}
	if strings.Contains(body, `href="/settings/updates#registry">Details`) {
		t.Error("an Admin can't open Settings → Updates")
	}
	if strings.Contains(body, "Network") || strings.Contains(body, "Films, TV and music") {
		t.Error("filters only for categories in use")
	}

	// Installing it anyway goes back to the catalog, which says why; an
	// unknown package is still not found.
	if _, body := h.get("/install/paperless"); !strings.Contains(body, "Paperless-ngx needs a newer Roostack (0.9 or newer), so it can&#39;t be installed yet.") {
		t.Errorf("installing one that needs a newer Roostack:\n%s", body)
	}
	if code, _ := h.get("/install/jellyfin"); code != http.StatusNotFound {
		t.Errorf("a package not listed: %d", code)
	}
}

// Settings → Updates shows the registry on the controller's page, and
// the Owner can check now or turn checking off.
func TestSettingsRegistry(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	reg := &fakeRegistry{on: true, status: fresh()}
	h.server.Registry = reg

	_, body := h.get("/settings/updates")
	for _, want := range []string{`id="registry"`, "Services you can install", "3 services",
		`href="https://github.com/roostack/services/tree/8eb8a2e29d95c8266526261d391151a8117387ea"`, "<code>8eb8a2e29d95</code>",
		registry.DefaultURL, "/var/lib/roostack/catalog", "jellyfin", "needs Roostack 0.9 or newer", `name="on" value="1" checked`} {
		if !strings.Contains(body, want) {
			t.Errorf("Settings → Updates is missing %q", want)
		}
	}

	// A hand-built or mirrored index may give a short commit.
	reg.status.Commit = "abc12"
	if code, body := h.get("/settings/updates"); code != http.StatusOK || !strings.Contains(body, "<code>abc12</code>") {
		t.Errorf("a short commit: %d", code)
	}
	reg.status = fresh()

	h.post("/settings/updates/registry/check", nil, h.srv.URL)
	if reg.checks != 1 {
		t.Errorf("Check now: %d checks", reg.checks)
	}
	h.post("/settings/updates/registry/on", url.Values{}, h.srv.URL)
	if reg.on {
		t.Error("unticked: fetching should be off")
	}
	_, body = h.get("/settings/updates")
	form, _, _ := strings.Cut(body[strings.Index(body, `action="/settings/updates/registry/on"`):], "</form>")
	if strings.Contains(body, "Check now") || strings.Contains(form, `value="1" checked`) {
		t.Error("turned off: no Check now, and the box unticked")
	}
	if !strings.Contains(body, "Let Roostack stop a service the registry says is dangerous") {
		t.Error("the opt-out from stops is beside it")
	}
	h.post("/settings/updates/registry/on", url.Values{"on": {"1"}}, h.srv.URL)
	if !reg.on || len(reg.setOnTo) != 2 {
		t.Errorf("turning it on again: %v", reg.setOnTo)
	}
}

// Only the Owner changes it.
func TestSettingsRegistryOwnerOnly(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	reg := &fakeRegistry{on: true, status: fresh()}
	h.server.Registry = reg
	for _, path := range []string{"/settings/updates/registry/check", "/settings/updates/registry/on"} {
		if code, _ := h.post(path, nil, h.srv.URL); code == http.StatusOK || reg.checks != 0 || len(reg.setOnTo) != 0 {
			t.Errorf("%s: an Admin got %d", path, code)
		}
	}
}

// An old list is a warning under "Needs attention", for the Owner.
func TestRegistryAttention(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	reg := &fakeRegistry{on: true, status: fresh()}
	h.server.Registry = reg
	const warning = "list of services is from"
	if _, body := h.get("/"); strings.Contains(body, warning) {
		t.Error("a fresh list needs no attention")
	}
	reg.status.Date = time.Now().Add(-20 * 24 * time.Hour)
	if _, body := h.get("/"); !strings.Contains(body, warning) || !strings.Contains(body, `href="/settings/updates#registry"`) {
		t.Error("an old list should be under Needs attention")
	}
	reg.on = false
	if _, body := h.get("/"); strings.Contains(body, warning) {
		t.Error("turned off on purpose: no warning")
	}
}

// Every catalog category has a drawn icon, for a service without a logo.
func TestCategoryIcons(t *testing.T) {
	for _, c := range pkgspec.Categories {
		if categoryIcon([]string{c.Key}) != c.Key {
			t.Errorf("category %s has no icon", c.Key)
		}
	}
	if categoryIcon(nil) != "services" {
		t.Error("no category: the services icon")
	}
}

// Without a list yet, the catalog says why: no root key in this build, a
// failed fetch with its reason, or a fetch running, while it asks
// /catalog/ready until there's something to show and then reloads.
func TestCatalogWaitingForRegistry(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	reg := &fakeRegistry{on: true, status: registry.Status{Fetching: true}}
	h.server.Registry = reg
	_, body := h.get("/catalog")
	if !strings.Contains(body, "This page will show them as soon as it has them") || !strings.Contains(body, `hx-get="/catalog/ready"`) {
		t.Errorf("fetching:\n%s", body)
	}
	resp, err := h.client.Get(h.srv.URL + "/catalog/ready")
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ready while fetching: %v %v", resp.StatusCode, err)
	}
	reg.status = registry.Status{Error: "Get \"https://registry.roostack.cloud/v1/keys.json\": dial tcp: lookup registry.roostack.cloud: no such host\nsecond line of the error", Checked: time.Now()}
	resp, _ = h.client.Get(h.srv.URL + "/catalog/ready")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("HX-Refresh") != "true" {
		t.Errorf("ready after a failure: %d %q", resp.StatusCode, resp.Header.Get("HX-Refresh"))
	}
	if _, body = h.get("/catalog"); !strings.Contains(body, "no such host.") || strings.Contains(body, "second line of the error") || strings.Contains(body, `hx-get="/catalog/ready"`) {
		t.Errorf("failed:\n%s", body)
	}
	reg.status = registry.Status{NoRootKey: true, Error: "this build of Roostack has no registry root key", Checked: time.Now()}
	if _, body = h.get("/catalog"); !strings.Contains(body, "can&#39;t check the service registry&#39;s signatures") {
		t.Errorf("no root key:\n%s", body)
	}
}
