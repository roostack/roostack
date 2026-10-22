package web

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/editor"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/registry"
)

// The Owner adds and removes packages of their own from the catalog
// (SPEC.md §8.3.4); nobody else can.
func TestAddYourOwn(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.local = map[string]bool{"forgejo": true}

	_, body := h.get("/catalog")
	for _, want := range []string{`href="/catalog/add"`, `<span class="badge badge-warn">Your own</span>`,
		`action="/catalog/forgejo/remove"`, `href="/install/forgejo">Install`} {
		if !strings.Contains(body, want) {
			t.Errorf("the Owner's catalog is missing %s", want)
		}
	}
	if strings.Contains(body, `action="/catalog/immich/remove"`) {
		t.Error("a registry package can't be removed from the catalog")
	}

	_, body = h.get("/catalog/add")
	if !strings.Contains(body, "Nobody has reviewed this package") || !strings.Contains(body, `enctype="multipart/form-data"`) ||
		!strings.Contains(body, "/var/lib/roostack/local-packages") {
		t.Errorf("the add page:\n%s", body)
	}

	data := pkgtest.Read(t, "forgejo")
	code, _ := h.postFiles("/catalog/add", map[string][]byte{"package": data, "icon": {}})
	if code != http.StatusOK || len(fm.added) != 1 || !bytes.Equal(fm.added[0], data) {
		t.Fatalf("adding: status %d, added %d", code, len(fm.added))
	}
	if _, body := h.get("/catalog?added=forgejo"); !strings.Contains(body, "Added Forgejo.") || !strings.Contains(body, `href="/install/forgejo">Install it`) {
		t.Error("the catalog should say it was added, with a link to install it")
	}

	fm.failWith = &manager.FormError{Msg: "The service registry has a package called forgejo already: give yours another name."}
	code, body = h.postFiles("/catalog/add", map[string][]byte{"package": data})
	if code != http.StatusBadRequest || !strings.Contains(body, "give yours another name") {
		t.Errorf("a refused package: %d", code)
	}
	fm.failWith = nil

	h.post("/catalog/forgejo/remove", url.Values{}, h.srv.URL)
	if len(fm.removedLocal) != 1 || fm.removedLocal[0] != "forgejo" {
		t.Errorf("removed %v", fm.removedLocal)
	}
	if code, _ := h.post("/catalog/immich/remove", url.Values{}, h.srv.URL); code != http.StatusNotFound {
		t.Errorf("removing a registry package: %d", code)
	}

	// An admin can install the Owner's own packages, but not add or
	// remove them (decided by the Owner, 2026-10-14).
	h, fm = loggedIn(t, auth.RoleAdmin)
	fm.local = map[string]bool{"forgejo": true}
	if _, body := h.get("/catalog"); strings.Contains(body, `/catalog/add`) || strings.Contains(body, `/remove"`) ||
		!strings.Contains(body, `href="/install/forgejo">Install`) {
		t.Error("an admin shouldn't see Add your own or Remove, but can install")
	}
	if code, body := h.get("/catalog/add"); code == http.StatusOK && strings.Contains(body, "Add your own service") {
		t.Error("an admin reached the add page")
	}
	h.postFiles("/catalog/add", map[string][]byte{"package": data})
	h.post("/catalog/forgejo/remove", url.Values{}, h.srv.URL)
	if len(fm.added) != 0 || len(fm.removedLocal) != 0 {
		t.Errorf("an admin added %d and removed %v", len(fm.added), fm.removedLocal)
	}
}

// Installing shows the package policy (SPEC.md §8.3.4): a package of the
// Owner's own with what it asks for, to confirm; a registry package that
// breaks it can't be installed.
func TestInstallShowsPolicy(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.local = map[string]bool{"forgejo": true}

	_, body := h.get("/install/forgejo")
	if !strings.Contains(body, "nobody has reviewed it") || strings.Contains(body, `name="accept_risks"`) {
		t.Error("one of the Owner's own, with no problems: a warning, nothing to tick")
	}
	fm.risks = []string{"containers.server.image: pin the image by digest"}
	_, body = h.get("/install/forgejo")
	for _, want := range []string{"can&#39;t have:", "<li>containers.server.image: pin the image by digest</li>", `name="accept_risks" required`} {
		if !strings.Contains(body, want) {
			t.Errorf("a risky package of the Owner's own: missing %s", want)
		}
	}
	form := url.Values{"setting.admin_username": {"mum"}, "setting.admin_email": {"mum@example.com"},
		"setting.admin_password": {"correct horse"}, "accept_risks": {"on"}}
	h.post("/install/forgejo", form, h.srv.URL)
	if len(fm.installs) != 1 || !fm.installs[0].AcceptRisks {
		t.Errorf("the Owner's confirmation wasn't passed on: %+v", fm.installs)
	}
	if logs, _ := h.store.RecentAudit(context.Background(), 1); len(logs) != 1 ||
		logs[0].Detail != "forgejo; accepted risks: containers.server.image: pin the image by digest" {
		t.Errorf("the accepted risks are audited: %+v", logs)
	}

	// A registry package that breaks it.
	fm.policy = []string{"containers.app.quadlet: Container.PodmanArgs=--privileged isn't allowed"}
	_, body = h.get("/install/vaultwarden")
	if !strings.Contains(body, "can&#39;t be installed") || !strings.Contains(body, "PodmanArgs") || strings.Contains(body, `type="submit">Install`) {
		t.Error("a registry package that breaks the policy should say why, with no Install button")
	}

	// Only the Owner can accept the risks.
	h, fm = loggedIn(t, auth.RoleAdmin)
	fm.local, fm.risks = map[string]bool{"forgejo": true}, []string{"containers.server.image: pin the image by digest"}
	_, body = h.get("/install/forgejo")
	if !strings.Contains(body, "Only the Owner can install it") || strings.Contains(body, `type="submit">Install`) {
		t.Error("an admin shouldn't be offered a risky package of the Owner's own")
	}
	h.post("/install/forgejo", form, h.srv.URL)
	if len(fm.installs) != 1 || fm.installs[0].AcceptRisks {
		t.Errorf("an admin's confirmation was passed on: %+v", fm.installs)
	}
}

// With the registry turned off, the Owner's own packages are still listed
// and can be installed (SPEC.md §8.3.3).
func TestYourOwnWithRegistryOff(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.local = map[string]bool{"forgejo": true}
	h.server.Registry = &fakeRegistry{on: false, status: fresh()}
	_, body := h.get("/catalog")
	if strings.Contains(body, "/install/immich") || !strings.Contains(body, `href="/install/forgejo">Install`) {
		t.Error("turned off: only the Owner's own should be offered")
	}
	if _, body := h.get("/install/forgejo"); !strings.Contains(body, `name="subdomain"`) {
		t.Error("turned off: the Owner's own can still be installed")
	}
}

// Installed services show who looks after their package, as the catalog
// does (SPEC.md §8.3.4): the registry's tier now, by package (a second copy
// too), or "Your own".
func TestServiceTierBadges(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	h.server.Registry = &fakeRegistry{on: true, status: fresh(), listing: map[string]registry.Listed{
		"vaultwarden": {Package: &registry.Package{Title: "Vaultwarden", Tier: registry.TierCommunity}},
		"immich":      {Package: &registry.Package{Title: "Immich", Tier: registry.TierOfficial}},
		"forgejo":     {Package: &registry.Package{Title: "Forgejo", Tier: registry.TierOfficial}},
	}}
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{
			{Name: "vaultwarden", Title: "Vaultwarden", Status: "running", Package: "vaultwarden"},
			{Name: "immich-family", Title: "Immich (family)", Status: "running", Package: "immich"},
			// The Owner's own, with a name the registry has since used.
			{Name: "forgejo", Title: "Forgejo", Status: "running", Package: "forgejo", Local: true},
			{Name: "gone", Title: "Gone", Status: "running", Package: "gone"},
		}, nil
	}
	_, body := h.get("/services")
	for _, want := range []string{
		`Vaultwarden</a> <span class="badge badge-warn">Community</span>`,
		`Immich (family)</a> <span class="badge">Official</span>`,
		`Forgejo</a> <span class="badge badge-warn">Your own</span>`,
		`Gone</a> `,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the Services list is missing %s", want)
		}
	}
	if strings.Contains(body, `Gone</a> <span class="badge`) {
		t.Error("a package the registry no longer lists has no tier badge")
	}
	if _, body := h.get("/services/immich-family"); !strings.Contains(body, `<h1>Immich (family) <span class="badge">Official</span></h1>`) {
		t.Error("the service page should show its tier")
	}
	if _, body := h.get("/services/forgejo"); !strings.Contains(body, `<span class="badge badge-warn">Your own</span></h1>`) {
		t.Error("the service page should show Your own")
	}
}

// The package editor (SPEC.md §8.3.7): the Owner's page, with the
// editor's files; its check answers with the lab's own checks; and what
// it adds arrives as text, through the same checks as a file.
func TestPackageEditor(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	code, body := h.get("/catalog/editor")
	if code != http.StatusOK {
		t.Fatalf("the editor page: %d", code)
	}
	for _, want := range []string{`data-editor data-where="roostack"`, `data-check="/catalog/editor/check"`, `data-add="/catalog/add"`,
		`data-help="/help/"`, `<script src="/editor/check.js" defer></script>`, `<link rel="stylesheet" href="/editor/editor.css">`} {
		if !strings.Contains(body, want) {
			t.Errorf("the editor page is missing %s", want)
		}
	}
	if code, _ := h.get("/editor/editor.js"); code != http.StatusOK {
		t.Errorf("the editor's script: %d", code)
	}
	if _, body := h.get("/catalog/add"); !strings.Contains(body, `href="/catalog/editor"`) {
		t.Error("Add your own should link to the editor")
	}

	check := func(data []byte, origin string) (int, string) {
		req, _ := http.NewRequest("POST", h.srv.URL+"/catalog/editor/check", bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/yaml")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	data := pkgtest.Read(t, "forgejo")
	if code, body := check(data, h.srv.URL); code != http.StatusOK || !strings.Contains(body, `"errors":null`) {
		t.Errorf("checking a good package: %d %s", code, body)
	}
	if code, body := check([]byte("name: Bad\n"), h.srv.URL); code != http.StatusOK || !strings.Contains(body, `must be lowercase letters`) {
		t.Errorf("checking a bad package: %d %s", code, body)
	}
	if code, _ := check(data, "https://evil.example"); code != http.StatusForbidden {
		t.Errorf("a check from another site: %d", code)
	}

	// Added from the editor: the text, as its form sends it (multipart,
	// lines ending CRLF), and LF again when it arrives.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("package_text", strings.ReplaceAll(string(data), "\n", "\r\n"))
	mw.Close()
	req, _ := http.NewRequest("POST", h.srv.URL+"/catalog/add", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", h.srv.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(fm.added) != 1 || !bytes.Equal(fm.added[0], data) {
		t.Errorf("added from the editor: %d packages", len(fm.added))
	}

	// Only the Owner.
	h, _ = loggedIn(t, auth.RoleAdmin)
	if code, body := h.get("/catalog/editor"); code == http.StatusOK && strings.Contains(body, "data-editor") {
		t.Error("an admin reached the editor")
	}
	if code, _ := check(data, h.srv.URL); code == http.StatusOK {
		t.Error("an admin used the editor's check")
	}
}

// The editor draws Roostack's own icons (it runs outside Roostack too, so
// it has copies): each must be the same as icons.go's.
func TestEditorIcons(t *testing.T) {
	js, err := fs.ReadFile(editor.Files(), "editor.js")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(js), "const icons = {")
	end := strings.Index(string(js)[start:], "\n  };")
	if start < 0 || end < 0 {
		t.Fatal("editor.js has no icons")
	}
	found := 0
	for _, m := range regexp.MustCompile(`(?m)^\s+"?([a-z-]+)"?: '([^']*)',$`).FindAllStringSubmatch(string(js)[start:start+end], -1) {
		found++
		if icons[m[1]] != m[2] {
			t.Errorf("editor.js's icon %q isn't icons.go's", m[1])
		}
	}
	if found < 5 {
		t.Errorf("found %d of editor.js's icons", found)
	}
}
