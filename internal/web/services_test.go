package web

import (
	"context"
	"errors"
	"fmt"
	"github.com/mylab/mylab/internal/activity"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/manager"
	"github.com/mylab/mylab/internal/pkgspec"
	"github.com/mylab/mylab/internal/store"
)

type fakeManager struct {
	installs []manager.InstallRequest
	removed  []string
	failWith error
	status   manager.ApplyStatus

	ports   []manager.PortStatus
	portErr error
	setPort []string

	setup     map[string]manager.HookStatus
	retried   []string
	actions   []string
	actionOut string
	actionErr error

	values  map[string]any
	updated []manager.SettingsRequest

	acts     *activity.Registry
	machines []manager.Machine
}

func (f *fakeManager) LatestActivity(kind string) (activity.Snapshot, bool) {
	return f.acts.Latest(kind)
}
func (f *fakeManager) Machines() ([]manager.Machine, error) {
	if f.machines != nil {
		return f.machines, nil
	}
	return []manager.Machine{{ID: "n-0123456789", Name: "nuc", Controller: true, This: true,
		Services: []string{"vaultwarden"}, Status: f.status}}, nil
}

// acts is the fake node's activity registry.
func (h *harness) acts() *activity.Registry {
	fm := h.server.Node.(*fakeManager)
	if fm.acts == nil {
		fm.acts = activity.New()
	}
	return fm.acts
}

func (f *fakeManager) Catalog() ([]manager.CatalogItem, error) {
	return []manager.CatalogItem{
		{Name: "immich", Title: "Immich", Description: "Photos"},
		{Name: "vaultwarden", Title: "Vaultwarden", Installed: true},
	}, nil
}

func (f *fakeManager) Package(name string) (*pkgspec.Package, error) {
	if name != "immich" && name != "vaultwarden" && name != "forgejo" {
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

func (f *fakeManager) DataDirs(name string) []string              { return []string{"/srv/mylab/services/" + name} }
func (f *fakeManager) Ports(string) ([]manager.PortStatus, error) { return f.ports, nil }
func (f *fakeManager) InstallStatus(_ context.Context, name string) manager.HookStatus {
	return f.setup[name]
}
func (f *fakeManager) CurrentSettings(name string) (map[string]any, string, error) {
	if f.values == nil {
		return map[string]any{"signups_allowed": false}, "git", nil
	}
	return f.values, "git", nil
}
func (f *fakeManager) UpdateSettings(_ context.Context, name string, req manager.SettingsRequest) error {
	if req.Subdomain == "taken" {
		return &manager.FormError{Msg: "That web address is already used by another service."}
	}
	f.updated = append(f.updated, req)
	return nil
}
func (f *fakeManager) RevealSecret(_ context.Context, name, key string) (string, error) {
	if key != "admin_token" {
		return "", manager.ErrNotFound
	}
	return "s3cret-token", nil
}
func (f *fakeManager) RetryInstall(_ context.Context, name string) error {
	f.retried = append(f.retried, name)
	return nil
}
func (f *fakeManager) RunAction(_ context.Context, name, action string, inputs map[string]string) (string, error) {
	f.actions = append(f.actions, fmt.Sprintf("%s %s %v", name, action, inputs))
	return f.actionOut, f.actionErr
}
func (f *fakeManager) SetPort(_ context.Context, name, port string, enabled bool, hostPort int) error {
	if f.portErr != nil {
		return f.portErr
	}
	f.setPort = append(f.setPort, fmt.Sprintf("%s %s %v %d", name, port, enabled, hostPort))
	return nil
}
func (f *fakeManager) Status() manager.ApplyStatus { return f.status }

// loggedIn returns a harness with a fake manager and a logged-in user.
func loggedIn(t *testing.T, role string) (*harness, *fakeManager) {
	t.Helper()
	h := newHarness(t, "")
	fm := &fakeManager{}
	h.server.Manager, h.server.Node = fm, fm
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

	_, body := h.get("/services")
	if !strings.Contains(body, `href="/catalog"`) || !strings.Contains(body, `href="/services/vaultwarden"`) {
		t.Error("admins should see Add, and links to each service")
	}
	_, body = h.get("/services/vaultwarden")
	if !strings.Contains(body, "/services/vaultwarden/remove") || !strings.Contains(body, "passwords.lab.example.com") {
		t.Error("the service page should show its address and a Remove link")
	}
	if code, _ := h.get("/services/nope"); code != http.StatusNotFound {
		t.Errorf("unknown service page: %d", code)
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
	if _, body := h.get("/services"); strings.Contains(body, `href="/catalog"`) || strings.Contains(body, `href="/services/vaultwarden"`) {
		t.Error("members must not see management controls")
	}
	for _, path := range []string{"/catalog", "/install/immich", "/services/vaultwarden", "/services/vaultwarden/remove", "/settings"} {
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
	for _, path := range []string{"/partials/services", "/partials/home"} {
		if _, body := h.get(path); !strings.Contains(body, "Applying changes") {
			t.Errorf("%s: expected the applying banner", path)
		}
	}
	fm.status = manager.ApplyStatus{Finished: true, LastErr: errors.New("image pull failed")}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "image pull failed") || !strings.Contains(body, "Needs attention") {
		t.Error("expected the last error on the home page's attention list")
	}
}

func TestNavigationByRole(t *testing.T) {
	for role, want := range map[string][]string{
		auth.RoleOwner:  {`href="/services"`, `href="/backups"`, `href="/settings"`, `href="/account"`},
		auth.RoleAdmin:  {`href="/services"`, `href="/backups"`, `href="/account"`},
		auth.RoleMember: {`href="/services"`, `href="/account"`},
	} {
		h, _ := loggedIn(t, role)
		_, body := h.get("/account")
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s: nav missing %s", role, w)
			}
		}
		if role != auth.RoleOwner && strings.Contains(body, `href="/settings"`) {
			t.Errorf("%s must not see Settings", role)
		}
		if role == auth.RoleMember && strings.Contains(body, `href="/backups"`) {
			t.Error("members must not see Backups")
		}
	}
}

func TestHomeAllGood(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner) // owners have 2FA in this harness
	h.server.Manager, h.server.Node = nil, nil
	_, body := h.get("/")
	if !strings.Contains(body, "Everything&#39;s working") && !strings.Contains(body, "Everything's working") {
		t.Error("with nothing to do, home should say everything's working")
	}
	if !strings.Contains(body, "Vaultwarden") {
		t.Error("home should show the services")
	}
}

func TestServicePorts(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	fm.ports = []manager.PortStatus{{Name: "ssh", Title: "Git over SSH", Description: "Clone with SSH keys",
		PortContext: generate.PortContext{HostPort: 2222, Protocol: "tcp", Exposure: "private"}}}

	_, body := h.get("/services/vaultwarden")
	if !strings.Contains(body, "Git over SSH") || !strings.Contains(body, `action="/services/vaultwarden/ports/ssh"`) ||
		!strings.Contains(body, `value="2222"`) {
		t.Fatal("service page should list the package's ports")
	}

	if code, body := h.post("/services/vaultwarden/ports/ssh", url.Values{"enabled": {"on"}, "host_port": {"ssh"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "Enter a port number") {
		t.Errorf("bad number: %d", code)
	}
	fm.portErr = &manager.FormError{Msg: "Port 8480 is already used by the MyLab admin UI."}
	if code, body := h.post("/services/vaultwarden/ports/ssh", url.Values{"enabled": {"on"}, "host_port": {"8480"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "already used by the MyLab admin UI") {
		t.Errorf("clash: %d", code)
	}
	fm.portErr = nil
	h.post("/services/vaultwarden/ports/ssh", url.Values{"enabled": {"on"}, "host_port": {"2200"}}, h.srv.URL)
	h.post("/services/vaultwarden/ports/ssh", url.Values{"host_port": {"2200"}}, h.srv.URL)
	if got := strings.Join(fm.setPort, "; "); got != "vaultwarden ssh true 2200; vaultwarden ssh false 2200" {
		t.Errorf("SetPort calls: %s", got)
	}
}

func TestServiceSetupAndActions(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "forgejo", Title: "Forgejo", Status: "running", URLs: []string{"https://git.lab.example.com"}}}, nil
	}

	// The install form enforces what the package asks for.
	_, body := h.get("/install/forgejo")
	if !strings.Contains(body, `name="setting.admin_password" value="" autocomplete="new-password"`) ||
		!strings.Contains(body, `minlength="8"`) || strings.Count(body, "required") < 4 {
		t.Error("install form: admin fields should be required, the password at least 8 characters")
	}

	fm.setup = map[string]manager.HookStatus{"forgejo": {Pending: true}}
	_, body = h.get("/services/forgejo")
	if !strings.Contains(body, `id="setup-status"`) || !strings.Contains(body, `hx-select="#setup-status"`) {
		t.Error("pending setup: want a polling notice that only swaps itself")
	}
	if _, home := h.get("/"); !strings.Contains(home, "Finishing setting up Forgejo") {
		t.Error("home: pending setup should be listed")
	}

	fm.setup["forgejo"] = manager.HookStatus{Failed: "Forgejo didn't finish setting up: username is invalid"}
	_, body = h.get("/services/forgejo")
	if !strings.Contains(body, "username is invalid") || !strings.Contains(body, `action="/services/forgejo/setup/retry"`) {
		t.Error("failed setup: want the reason and a retry button")
	}
	if _, home := h.get("/"); !strings.Contains(home, "Forgejo didn&#39;t finish setting up.") {
		t.Error("home: failed setup should need attention")
	}
	h.post("/services/forgejo/setup/retry", nil, h.srv.URL)
	if len(fm.retried) != 1 {
		t.Error("retry not called")
	}

	fm.setup = nil
	_, body = h.get("/services/forgejo")
	if !strings.Contains(body, `action="/services/forgejo/actions/reset-password"`) || !strings.Contains(body, `name="input.password"`) {
		t.Fatal("service page should offer the package's actions")
	}
	fm.actionOut = "Password changed for mum."
	code, body := h.post("/services/forgejo/actions/reset-password", url.Values{"input.username": {"mum"}, "input.password": {"a new password"}}, h.srv.URL)
	if code != http.StatusOK || !strings.Contains(body, "Password changed for mum.") || !strings.Contains(body, "<details class=\"action\" open>") {
		t.Errorf("action result not shown: %d", code)
	}
	fm.actionErr = &manager.ActionError{Title: "Reset a password", Output: "user does not exist"}
	if code, body := h.post("/services/forgejo/actions/reset-password", url.Values{"input.username": {"dad"}, "input.password": {"a new password"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "Reset a password didn&#39;t work.") || !strings.Contains(body, "user does not exist") {
		t.Errorf("failed action: %d", code)
	}
	logs, _ := h.store.RecentAudit(context.Background(), 10)
	for _, l := range logs {
		if strings.Contains(l.Detail, "a new password") {
			t.Error("action inputs must not be written to the audit log")
		}
	}
}

func TestTemporaryPasswordMustBeChanged(t *testing.T) {
	h := newHarness(t, "")
	hash, _ := auth.HashPassword("temporary password")
	h.store.CreateUser(context.Background(), store.User{Username: "dad", PasswordHash: hash, Role: auth.RoleMember,
		MustChangePassword: true})
	h.post("/login", url.Values{"username": {"dad"}, "password": {"temporary password"}}, h.srv.URL)

	if _, body := h.get("/"); !strings.Contains(body, "using a temporary password") {
		t.Fatal("a temporary password should send the user to choose their own first")
	}
	_, body := h.post("/account/password", url.Values{"current": {"temporary password"},
		"password": {"my own long password"}, "confirm": {"my own long password"}}, h.srv.URL)
	if !strings.Contains(body, "Your services") {
		t.Error("after choosing a password, the user should carry on to the dashboard")
	}
	if u, _ := h.store.UserByUsername(context.Background(), "dad"); u.MustChangePassword {
		t.Error("flag not cleared")
	}
}

func TestThemeToggle(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	if _, body := h.get("/services"); !strings.Contains(body, `<html lang="en" data-palette="teal">`) ||
		!strings.Contains(body, `name="theme" value="light"`) {
		t.Fatal("new users follow the device; the toggle offers light next")
	}
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/account/theme", strings.NewReader("theme=dark"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.srv.URL)
	req.Header.Set("Referer", h.srv.URL+"/services")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `data-palette="teal" data-mode="dark"`) || !strings.Contains(string(body), "<title>Services") {
		t.Error("choosing dark should return to the same page, in dark")
	}
	if code, _ := h.post("/account/theme", url.Values{"theme": {"purple"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("unknown theme: %d", code)
	}
}

func TestServiceSettings(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "forgejo", Title: "Forgejo", Status: "running", URLs: []string{"https://git.lab.example.com"}},
			{Name: "vaultwarden", Title: "Vaultwarden", Status: "running", URLs: []string{"https://vault.lab.example.com"}}}, nil
	}

	_, body := h.get("/services/forgejo")
	for _, want := range []string{`href="https://git.lab.example.com/-/admin"`, "Site administration ↗",
		`name="subdomain" value="git"`, `name="setting.signups_allowed"`} {
		if !strings.Contains(body, want) {
			t.Errorf("forgejo settings missing %q", want)
		}
	}
	if strings.Contains(body, "setting.admin_username") || strings.Contains(body, "setting.admin_password") {
		t.Error("install-only settings must not be offered again")
	}

	h.post("/services/forgejo/settings", url.Values{"subdomain": {"code"}, "setting.signups_allowed": {"on"}}, h.srv.URL)
	if len(fm.updated) != 1 || fm.updated[0].Subdomain != "code" || fm.updated[0].Settings["signups_allowed"] != "on" {
		t.Errorf("updated %+v", fm.updated)
	}
	code, body := h.post("/services/forgejo/settings", url.Values{"subdomain": {"taken"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "already used") || !strings.Contains(body, `value="taken"`) {
		t.Errorf("clash: %d", code)
	}

	// Vaultwarden's admin token is shown on request, and audited.
	_, body = h.get("/services/vaultwarden")
	if !strings.Contains(body, `action="/services/vaultwarden/reveal/admin_token"`) || strings.Contains(body, "s3cret-token") {
		t.Error("the token should be behind a Show button")
	}
	_, body = h.post("/services/vaultwarden/reveal/admin_token", nil, h.srv.URL)
	if !strings.Contains(body, "s3cret-token") {
		t.Error("token not shown")
	}
	if code, _ := h.post("/services/vaultwarden/reveal/signups_allowed", nil, h.srv.URL); code != http.StatusNotFound {
		t.Errorf("revealing a non-revealable setting: %d", code)
	}
	logs, _ := h.store.RecentAudit(context.Background(), 5)
	if len(logs) == 0 || logs[0].Action != "secret shown" {
		t.Errorf("reveal not audited: %+v", logs)
	}
}

func TestAppearance(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	_, body := h.get("/account")
	if !strings.Contains(body, `name="palette" value="teal" checked`) || !strings.Contains(body, `data-palette="catppuccin" data-mode="dark"`) {
		t.Fatal("account page should offer every theme, with the default chosen")
	}
	h.post("/account/appearance", url.Values{"palette": {"catppuccin"}, "mode": {"dark"}}, h.srv.URL)
	_, body = h.get("/services")
	if !strings.Contains(body, `<html lang="en" data-palette="catppuccin" data-mode="dark">`) {
		t.Error("pages should use the chosen theme and mode")
	}
	// Saved in the background (htmx): no redirect, nothing to swap.
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/account/appearance", strings.NewReader("palette=nord&mode=auto"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.srv.URL)
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	oob, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(oob), `id="theme-toggle" hx-swap-oob="true"`) ||
		!strings.Contains(string(oob), "◐ Auto") {
		t.Fatalf("background save: %v %v %s", resp.StatusCode, err, oob)
	}
	if _, body := h.get("/services"); !strings.Contains(body, `<html lang="en" data-palette="nord">`) {
		t.Error("background save not applied")
	}
	if code, _ := h.post("/account/appearance", url.Values{"palette": {"../../evil"}, "mode": {"dark"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("unknown theme accepted: %d", code)
	}
}

func TestAccent(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	h.post("/account/appearance", url.Values{"palette": {"catppuccin"}, "accent-catppuccin": {"peach"}, "mode": {"auto"}}, h.srv.URL)
	_, body := h.get("/account")
	if !strings.Contains(body, `<html lang="en" data-palette="catppuccin" data-accent="peach">`) ||
		!strings.Contains(body, `name="accent-catppuccin" value="peach"
          checked`) {
		t.Error("the chosen accent should be applied and selected")
	}
	if code, _ := h.post("/account/appearance", url.Values{"palette": {"catppuccin"}, "accent-catppuccin": {"red"}, "mode": {"auto"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("red isn't offered: %d", code)
	}
	// Switching to a theme without accents drops it.
	h.post("/account/appearance", url.Values{"palette": {"nord"}, "accent-catppuccin": {"peach"}, "mode": {"auto"}}, h.srv.URL)
	if _, body := h.get("/services"); !strings.Contains(body, `<html lang="en" data-palette="nord">`) {
		t.Error("nord has no accents")
	}
}

func TestMachines(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.status = manager.ApplyStatus{Finished: true, LastRun: time.Now()}
	_, body := h.get("/settings/machines")
	for _, want := range []string{"<h2>nuc", "This machine", "Runs the lab", `href="/services/vaultwarden"`, "Up to date", "n-0123456789"} {
		if !strings.Contains(body, want) {
			t.Errorf("machines page missing %q", want)
		}
	}
	if _, body := h.get("/settings"); !strings.Contains(body, "1 machine: nuc") {
		t.Error("settings overview should summarise the machines")
	}

	// With several machines, problems say which one.
	fm.machines = []manager.Machine{
		{ID: "n-0123456789", Name: "nuc", Controller: true, This: true, Status: manager.ApplyStatus{Finished: true}},
		{ID: "n-9876543210", Name: "vps", Status: manager.ApplyStatus{Finished: true, LastErr: errors.New("image pull failed")}},
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "The last change on vps didn&#39;t finish: image pull failed") {
		t.Errorf("home should name the machine:\n%s", body)
	}
	if _, body := h.get("/settings"); !strings.Contains(body, "A change on vps didn&#39;t finish") {
		t.Error("settings overview should flag the machine")
	}

	ha, _ := loggedIn(t, auth.RoleAdmin)
	if code, _ := ha.get("/settings/machines"); code == http.StatusOK {
		t.Error("only Owners see machines")
	}
}
