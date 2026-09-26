package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/manager"
	"github.com/mylab/mylab/internal/pkgspec"
	"github.com/mylab/mylab/internal/store"
)

type fakeManager struct {
	installs []manager.InstallRequest
	removed  []string
	failWith error
	status   manager.ApplyStatus
}

func (f *fakeManager) Catalog() ([]manager.CatalogItem, error) {
	return []manager.CatalogItem{
		{Name: "immich", Title: "Immich", Description: "Photos"},
		{Name: "vaultwarden", Title: "Vaultwarden", Installed: true},
	}, nil
}

func (f *fakeManager) Package(name string) (*pkgspec.Package, error) {
	if name != "immich" && name != "vaultwarden" {
		return nil, manager.ErrNotFound
	}
	return pkgspec.Load(filepath.Join("..", "..", "docs", "examples", name+".yaml"))
}

func (f *fakeManager) Install(_ context.Context, _ string, req manager.InstallRequest) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.installs = append(f.installs, req)
	return nil
}

func (f *fakeManager) Remove(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeManager) DataDirs(name string) []string { return []string{"/srv/mylab/services/" + name} }
func (f *fakeManager) Status() manager.ApplyStatus   { return f.status }

// loggedIn returns a harness with a fake manager and a logged-in user.
func loggedIn(t *testing.T, role string) (*harness, *fakeManager) {
	t.Helper()
	h := newHarness(t, "")
	fm := &fakeManager{}
	h.server.Manager = fm
	h.server.Domain = "lab.example.com"
	h.server.HasGPU = true
	hash, _ := auth.HashPassword("a long password")
	id, _ := h.store.CreateUser(context.Background(), store.User{Username: "u", PasswordHash: hash, Role: role})
	if role == auth.RoleOwner {
		secret, _ := auth.NewTOTPSecret()
		h.store.SetTOTP(context.Background(), id, secret, 0)
		h.post("/login", url.Values{"username": {"u"}, "password": {"a long password"}}, h.srv.URL)
		code, _ := auth.TOTPCode(secret, nowFunc())
		h.post("/login/totp", url.Values{"code": {code}}, h.srv.URL)
	} else {
		h.post("/login", url.Values{"username": {"u"}, "password": {"a long password"}}, h.srv.URL)
	}
	return h, fm
}

func TestInstallFlow(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)

	_, body := h.get("/")
	if !strings.Contains(body, `href="/catalog"`) || !strings.Contains(body, "/services/") {
		t.Error("admins should see Add and Remove controls")
	}
	if _, body := h.get("/catalog"); !strings.Contains(body, `href="/install/immich"`) || strings.Contains(body, `/install/vaultwarden"`) {
		t.Error("catalog should offer Immich and mark Vaultwarden installed")
	}

	_, body = h.get("/install/immich")
	for _, want := range []string{`value="photos"`, `.lab.example.com`, `name="setting.machine_learning" checked`,
		`name="setting.hardware_acceleration" checked`} { // auto + GPU
		if !strings.Contains(body, want) {
			t.Errorf("install form missing %s", want)
		}
	}

	code, _ := h.post("/install/immich", url.Values{"subdomain": {"pics"}, "setting.machine_learning": {"on"}}, h.srv.URL)
	if code != http.StatusOK || len(fm.installs) != 1 {
		t.Fatalf("install: status %d, calls %d", code, len(fm.installs))
	}
	req := fm.installs[0]
	if req.Subdomain != "pics" || req.Settings["machine_learning"] != "on" {
		t.Errorf("request = %+v", req)
	}
	if _, ticked := req.Settings["hardware_acceleration"]; ticked {
		t.Error("unticked checkbox must not be sent as set")
	}

	// A form error re-renders the form with the user's values.
	fm.failWith = &manager.FormError{Msg: "That web address is already used"}
	code, body = h.post("/install/immich", url.Values{"subdomain": {"pics"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "already used") || !strings.Contains(body, `value="pics"`) {
		t.Errorf("form error: %d", code)
	}
	if strings.Contains(body, `name="setting.machine_learning" checked`) {
		t.Error("re-rendered form must keep the user's unticked box")
	}

	// Other failures are server errors, not shown in detail.
	fm.failWith = errors.New("disk on fire")
	if code, body := h.post("/install/immich", nil, h.srv.URL); code != http.StatusInternalServerError || strings.Contains(body, "disk on fire") {
		t.Errorf("server error: %d", code)
	}
	fm.failWith = nil

	if code, _ := h.get("/install/nope"); code != http.StatusNotFound {
		t.Errorf("unknown package: %d", code)
	}

	_, body = h.get("/services/vaultwarden/remove")
	if !strings.Contains(body, "Your data is kept") || !strings.Contains(body, "/srv/mylab/services/vaultwarden") {
		t.Error("remove page should explain that data is kept, and where")
	}
	h.post("/services/vaultwarden/remove", nil, h.srv.URL)
	if len(fm.removed) != 1 || fm.removed[0] != "vaultwarden" {
		t.Errorf("removed = %v", fm.removed)
	}
}

func TestSecretFieldsAreNotEchoed(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	fm.failWith = &manager.FormError{Msg: "nope"}
	// Vaultwarden's only secret is generated, so it must not be on the form.
	if _, body := h.get("/install/vaultwarden"); strings.Contains(body, "admin_token") {
		t.Error("generated secrets must not appear on the form")
	}
	_, body := h.post("/install/vaultwarden", url.Values{"subdomain": {"vault"}, "setting.admin_token": {"hunter2hunter2"}}, h.srv.URL)
	if strings.Contains(body, "hunter2hunter2") {
		t.Error("submitted secrets must never be echoed back")
	}
}

func TestMembersCannotManage(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	if _, body := h.get("/"); strings.Contains(body, `href="/catalog"`) || strings.Contains(body, "/remove") {
		t.Error("members must not see management controls")
	}
	for _, path := range []string{"/catalog", "/install/immich", "/services/vaultwarden/remove"} {
		if code, _ := h.get(path); code != http.StatusForbidden {
			t.Errorf("GET %s: %d, want 403", path, code)
		}
	}
	if code, _ := h.post("/services/vaultwarden/remove", nil, h.srv.URL); code != http.StatusForbidden {
		t.Errorf("POST remove: %d, want 403", code)
	}
}

func TestApplyBanner(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	fm.status = manager.ApplyStatus{Running: true}
	if _, body := h.get("/partials/services"); !strings.Contains(body, "Applying changes") {
		t.Error("expected the applying banner")
	}
	fm.status = manager.ApplyStatus{Finished: true, LastErr: errors.New("image pull failed")}
	if _, body := h.get("/partials/services"); !strings.Contains(body, "image pull failed") {
		t.Error("expected the last error")
	}
}
