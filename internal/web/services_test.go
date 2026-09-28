package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/units"
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

	acts        *activity.Registry
	servers     []manager.Server
	serverOps   []string
	destination string
	copies      []manager.BackupCopy
	ending      []manager.EndingStorage
	retriedWith []string
	guestServer string
	providers   []string
	moves       []string
	guests      []manager.GuestView
	guestOps    []string
	backups     BackupService
	restores    RestoreService
	updates     HostUpdates
	settings    node.Settings
	disk        *node.DiskSpace
}

// DiskSpace: the fake's disk, if it has one.
func (f *fakeManager) DiskSpace(context.Context) (node.DiskSpace, error) {
	if f.disk == nil {
		return node.DiskSpace{}, errors.New("no disk")
	}
	return *f.disk, nil
}

func (f *fakeManager) Settings() node.Settings { return f.settings }

// The fake is also the lab's one server.
func (f *fakeManager) Identity() nodeid.Identity {
	return nodeid.Identity{ID: "n-0123456789", Name: "nuc"}
}
func (f *fakeManager) Apply(node.Assignment) {}
func (f *fakeManager) ServiceStates(context.Context) (map[string]string, error) {
	return nil, nil
}
func (f *fakeManager) NodeFor(string) node.Node          { return f }
func (f *fakeManager) OfferStorage(context.Context) bool { return len(f.guests) > 0 }
func (f *fakeManager) StorageGuests(context.Context) ([]manager.GuestView, error) {
	return f.guests, nil
}
func (f *fakeManager) InviteGuest(_ context.Context, name string, quota int64, endpoint string) (labnet.StorageInvite, error) {
	if quota < 1*units.GB {
		return labnet.StorageInvite{}, &manager.FormError{Msg: "Offer at least 1 GB."}
	}
	f.guestOps = append(f.guestOps, fmt.Sprintf("invite %s %d %s", name, quota/units.GB, endpoint))
	return labnet.StorageInvite{Token: "t", User: "g1"}, nil
}
func (f *fakeManager) RetryInstallWith(_ context.Context, name string, settings, secrets map[string]string) error {
	if settings["admin_username"] == "" {
		return &manager.FormError{Msg: "Administrator username is required."}
	}
	f.retriedWith = append(f.retriedWith, name+" "+settings["admin_username"]+" "+secrets["admin_password"])
	return nil
}
func (f *fakeManager) GuestServer(context.Context) string {
	if f.guestServer == "" {
		return "n-0123456789"
	}
	return f.guestServer
}
func (f *fakeManager) SetGuestServer(_ context.Context, id string) error {
	if len(f.guests) > 0 {
		return &manager.FormError{Msg: "Other labs store backups on nuc."}
	}
	f.guestServer = id
	return nil
}
func (f *fakeManager) DeleteRemovedGuest(_ context.Context, id int64) error {
	f.guestOps = append(f.guestOps, fmt.Sprintf("delete now %d", id))
	return nil
}
func (f *fakeManager) EndingStorages(context.Context) []manager.EndingStorage { return f.ending }
func (f *fakeManager) RemoveGuest(_ context.Context, id int64) error {
	f.guestOps = append(f.guestOps, fmt.Sprintf("remove %d", id))
	return nil
}
func (f *fakeManager) SetGuestQuota(_ context.Context, id, quota int64) error {
	f.guestOps = append(f.guestOps, fmt.Sprintf("quota %d %d", id, quota/units.GB))
	return nil
}
func (f *fakeManager) LeftoverGuests(context.Context) ([]manager.LeftoverGuest, error) {
	return []manager.LeftoverGuest{{User: "g9", Size: 2 * units.GB}}, nil
}
func (f *fakeManager) DeleteLeftoverGuest(_ context.Context, user string) error {
	if user != "g9" {
		return manager.ErrNotFound
	}
	f.guestOps = append(f.guestOps, "delete "+user)
	return nil
}
func (f *fakeManager) AddRemoteStorage(_ context.Context, code string) error {
	if !strings.HasPrefix(code, "roostack-storage-") {
		return &manager.FormError{Msg: "that isn't a Roostack storage invite"}
	}
	f.guestOps = append(f.guestOps, "add remote")
	return nil
}
func (f *fakeManager) RemoteStorages(context.Context) []manager.RemoteStorage {
	return []manager.RemoteStorage{{HostName: "Mum's lab", Quota: 50 * units.GB, Ref: "provider:7"}}
}
func (f *fakeManager) RemoteUsage(context.Context, manager.RemoteStorage) (labnet.GuestUsage, error) {
	return labnet.GuestUsage{Used: 3 * units.GB, Quota: 50 * units.GB}, nil
}
func (f *fakeManager) KeptData(_ context.Context, id string) ([]node.KeptFolder, error) {
	if id != "n-9876543210" {
		return nil, nil
	}
	return []node.KeptFolder{{Path: "/srv/roostack/services/forgejo.moved-to-nuc-20260928-002806", Service: "forgejo",
		MovedAway: true, Time: time.Now(), Size: 70 * units.MB}}, nil
}
func (f *fakeManager) RemoveKeptData(_ context.Context, id, path string) error {
	f.serverOps = append(f.serverOps, "delete "+path+" on "+id)
	return nil
}
func (f *fakeManager) MoveInfo(_ context.Context, name string) (manager.MoveInfo, error) {
	if name != "vaultwarden" {
		return manager.MoveInfo{}, manager.ErrNotFound
	}
	return manager.MoveInfo{From: manager.Server{ID: "n-0123456789", Name: "nuc", This: true}, Size: 5 * units.MB,
		Suggested: manager.MoveCopy}, nil
}
func (f *fakeManager) MoveService(_ context.Context, name, to, way string) error {
	if to == "" {
		return &manager.FormError{Msg: "Choose a server in this lab."}
	}
	f.moves = append(f.moves, name+" → "+to+" ("+way+")")
	return nil
}
func (f *fakeManager) BackupChoices(context.Context) ([]manager.BackupChoice, error) {
	return []manager.BackupChoice{{Ref: "server:n-0123456789", Title: "nuc's disk"}, {Ref: "provider:1", Title: "Backblaze"}}, nil
}
func (f *fakeManager) BackupKind(_ context.Context, _, ref string) string {
	switch {
	case ref == "drive:system":
		return manager.KindSystem
	case strings.HasPrefix(ref, "drive:"):
		return manager.KindUSB
	case strings.HasPrefix(ref, "provider:"):
		return manager.KindOnline
	case strings.HasPrefix(ref, "remote:"):
		return manager.KindFriend
	}
	return manager.KindServer
}
func (f *fakeManager) PromoteBackupCopy(_ context.Context, _, ref string) error {
	for i, c := range f.copies {
		if c.Ref == ref {
			f.copies[i] = manager.BackupCopy{Ref: f.destination, Title: "nuc's disk"}
			f.destination = ref
			return nil
		}
	}
	return &manager.FormError{Msg: "That isn't one of its backups."}
}
func (f *fakeManager) BackupCopiesOf(context.Context, string) []manager.BackupCopy { return f.copies }
func (f *fakeManager) AddBackupCopy(_ context.Context, id, ref string) error {
	if ref == f.destination {
		return &manager.FormError{Msg: "That's where its backups already go."}
	}
	f.copies = append(f.copies, manager.BackupCopy{Ref: ref, Title: "Backblaze", Repository: "s3:https://s3.example.com/b/roostack/l/" + id})
	return nil
}
func (f *fakeManager) RemoveBackupCopy(_ context.Context, _, ref string) error {
	f.copies = slices.DeleteFunc(f.copies, func(c manager.BackupCopy) bool { return c.Ref == ref })
	return nil
}
func (f *fakeManager) BackupTargetOf(_ context.Context, id string) (store.BackupTarget, string, bool) {
	if f.destination == "" {
		return store.BackupTarget{}, "", false
	}
	return store.BackupTarget{ServerID: id, Provider: f.destination, Repository: "rest:http://100.96.0.1:8000/x"}, "nuc's disk", true
}
func (f *fakeManager) SetBackupDestination(_ context.Context, id, ref string) error {
	if ref == "" {
		return &manager.FormError{Msg: "Choose where backups go."}
	}
	f.destination = ref
	return nil
}
func (f *fakeManager) AddS3Provider(_ context.Context, name string, p manager.S3Provider) error {
	if p.Bucket == "" {
		return &manager.FormError{Msg: "Enter the storage endpoint (starting with https://) and a bucket name."}
	}
	f.providers = append(f.providers, name)
	return nil
}
func (f *fakeManager) RemoveServer(_ context.Context, id string) error {
	if id == "n-9876543210" {
		return &manager.FormError{Msg: "vps still runs Forgejo. Remove it first, or move it to another server."}
	}
	f.serverOps = append(f.serverOps, "remove "+id)
	return nil
}
func (f *fakeManager) RenameServer(_ context.Context, id, name string) error {
	f.serverOps = append(f.serverOps, "rename "+id+" "+name)
	return nil
}
func (f *fakeManager) NodeByID(id string) node.Node {
	ms, _ := f.Servers()
	for _, m := range ms {
		if m.ID == id {
			return f // one fake plays every server
		}
	}
	return nil
}

// setSetup sets the wizard; a fakeSetup is also the server's settings.
func (h *harness) setSetup(ls LabSetup) {
	h.server.Setup = ls
	if fs, ok := ls.(*fakeSetup); ok {
		h.node().settings = fs
	}
}

func (f *fakeManager) Backups() node.Backups   { return f.backups }
func (f *fakeManager) Restores() node.Restores { return f.restores }
func (f *fakeManager) Updates() node.Updates   { return f.updates }

// node is the fake server.
func (h *harness) node() *fakeManager { return h.server.Node.(*fakeManager) }

func (f *fakeManager) LatestActivity(kind string) (activity.Snapshot, bool) {
	return f.acts.Latest(kind)
}
func (f *fakeManager) Servers() ([]manager.Server, error) {
	if f.servers != nil {
		return f.servers, nil
	}
	return []manager.Server{{ID: "n-0123456789", Name: "nuc", Controller: true, This: true,
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

func (f *fakeManager) DataDirs(name string) []string {
	return []string{"/srv/roostack/services/" + name}
}
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
	if !strings.Contains(body, "Your data is kept") || !strings.Contains(body, "/srv/roostack/services/vaultwarden") {
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
	fm.portErr = &manager.FormError{Msg: "Port 8480 is already used by the Roostack admin UI."}
	if code, body := h.post("/services/vaultwarden/ports/ssh", url.Values{"enabled": {"on"}, "host_port": {"8480"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "already used by the Roostack admin UI") {
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
	if !strings.Contains(body, "My Services") {
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

func TestServers(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.status = manager.ApplyStatus{Finished: true, LastRun: time.Now()}
	_, body := h.get("/settings/servers")
	for _, want := range []string{"<h2>nuc", "This server", "Runs the lab", `href="/services/vaultwarden"`, "Up to date", "n-0123456789"} {
		if !strings.Contains(body, want) {
			t.Errorf("servers page missing %q", want)
		}
	}
	if _, body := h.get("/settings"); !strings.Contains(body, "1 server: nuc") {
		t.Error("settings overview should summarise the servers")
	}

	// With several servers, problems say which one.
	fm.servers = []manager.Server{
		{ID: "n-0123456789", Name: "nuc", Controller: true, This: true, Status: manager.ApplyStatus{Finished: true}},
		{ID: "n-9876543210", Name: "vps", Status: manager.ApplyStatus{Finished: true, LastErr: errors.New("image pull failed")}},
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "The last change on vps didn&#39;t finish: image pull failed") {
		t.Errorf("home should name the server:\n%s", body)
	}
	if _, body := h.get("/settings"); !strings.Contains(body, "A change on vps didn&#39;t finish") {
		t.Error("settings overview should flag the server")
	}
	// The vps (the same fake here) has no SSH choice yet: its firewall is off.
	fm.settings = &fakeSetup{}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "vps&#39;s firewall is off") ||
		!strings.Contains(body, `href="/settings/servers/n-9876543210/remote-access"`) {
		t.Errorf("home should ask for vps's SSH choice:\n%s", body)
	}

	ha, _ := loggedIn(t, auth.RoleAdmin)
	if code, _ := ha.get("/settings/servers"); code == http.StatusOK {
		t.Error("only Owners see servers")
	}

	// Other servers' backups and updates show up too, named.
	fm.backups = &fakeBackups{status: backup.Status{Configured: true, Progress: -1,
		Last: &store.BackupRun{OK: false, Error: "repository locked"}}}
	fm.settings = &fakeSetup{ssh: "lan"}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "The last backup on vps didn&#39;t finish: repository locked") ||
		!strings.Contains(body, `href="/settings/servers/n-9876543210/backups"`) {
		t.Errorf("home should flag vps's backups:\n%s", body)
	}

	// Renaming and removing.
	if code, body := h.post("/settings/servers/n-9876543210/remove", nil, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "vps still runs Forgejo") {
		t.Errorf("removing a server with services: %d", code)
	}
	h.post("/settings/servers/n-9876543210/rename", url.Values{"name": {"cloud"}}, h.srv.URL)
	if len(fm.serverOps) != 1 || fm.serverOps[0] != "rename n-9876543210 cloud" {
		t.Errorf("ops = %v", fm.serverOps)
	}
	if _, body := h.post("/settings/servers/n-0123456789/rename", url.Values{"name": {"nuc2"}}, h.srv.URL); !strings.Contains(body, "Applying your settings") {
		t.Error("renaming this server restarts Roostack")
	}

	// Kept copies moves left behind, and deleting one.
	fm.serverOps = nil
	_, body = h.get("/settings/servers")
	if !strings.Contains(body, "forgejo.moved-to-nuc-20260928-002806") || !strings.Contains(body, "70.0 MB") ||
		!strings.Contains(body, "forgejo before it moved away") {
		t.Errorf("servers page should list kept copies:\n%s", body)
	}
	h.post("/settings/servers/n-9876543210/kept/remove", url.Values{"path": {"/srv/roostack/services/forgejo.moved-to-nuc-20260928-002806"}}, h.srv.URL)
	if len(fm.serverOps) != 1 || fm.serverOps[0] != "delete /srv/roostack/services/forgejo.moved-to-nuc-20260928-002806 on n-9876543210" {
		t.Errorf("ops = %v", fm.serverOps)
	}

	// A server's own pages; an unknown server is not found.
	fm.updates = &fakeUpdates{}
	if code, body := h.get("/settings/servers/n-0123456789/updates"); code != http.StatusOK || !strings.Contains(body, "Updates and restarts") {
		t.Errorf("server updates page: %d", code)
	}
	if code, _ := h.get("/settings/servers/n-9999999999/updates"); code != http.StatusNotFound {
		t.Errorf("unknown server: %d", code)
	}
}

// The Backups page's plan: numbered backups, empty slots with
// suggestions, and one chooser for adding anywhere (SPEC.md §6.9).
func TestBackupPlan(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	_, body := h.get("/backups")
	for _, want := range []string{"Your backup plan", "a drive at home", `href="/backups?add=2#plan"`, "2 kinds of storage", "Tell me more",
		"Your data on this server"} {
		if !strings.Contains(body, want) {
			t.Errorf("empty plan missing %q", want)
		}
	}
	// Backups are added in order: only the next one.
	if _, body := h.get("/backups?add=3"); strings.Contains(body, "Where should backup 3 go?") || strings.Contains(body, `href="/backups?add=3#plan"`) {
		t.Error("backup 3 before the main backup")
	}
	_, body = h.get("/backups?add=2")
	for _, want := range []string{"Where should your main backup go?", `value="provider:1"`, "Another Roostack instance", `value="server:n-0123456789"`} {
		if !strings.Contains(body, want) {
			t.Errorf("chooser missing %q", want)
		}
	}
	if code, body := h.post("/backups/plan", url.Values{"add": {"2"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "Choose where this backup goes.") || !strings.Contains(body, "Where should your main backup go?") {
		t.Errorf("no choice: %d", code)
	}
	// A new online account: an error keeps the chooser, never the secret.
	code, body := h.post("/backups/plan", url.Values{"add": {"2"}, "choice": {"online"}, "provider": {"new"}, "name": {"B2"},
		"secret_key": {"SUPERSECRET"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "bucket name") || strings.Contains(body, "SUPERSECRET") {
		t.Errorf("bad provider: %d", code)
	}
	h.post("/backups/plan", url.Values{"add": {"2"}, "choice": {"server"}, "server": {"server:n-0123456789"}}, h.srv.URL)
	if fm.destination != "server:n-0123456789" {
		t.Fatalf("backup 1 = %q", fm.destination)
	}
	h.post("/backups/plan", url.Values{"add": {"3"}, "choice": {"online"}, "provider": {"provider:1"}}, h.srv.URL)
	if len(fm.copies) != 1 || fm.copies[0].Ref != "provider:1" {
		t.Fatalf("backup 2 = %+v", fm.copies)
	}
	_, body = h.get("/backups?add=4")
	for _, want := range []string{"nuc&#39;s disk", "Backblaze", "Make this the main backup", "Stop this backup", "Where should the extra backup go?",
		`<span class="sr-only">Extra</span>`, "Main backup ·", `✓</span>3 copies`, `✓</span>2 kinds of storage`} {
		if !strings.Contains(body, want) {
			t.Errorf("plan missing %q", want)
		}
	}
	if strings.Contains(body, `name="provider" value="provider:1"`) {
		t.Error("where backups already go isn't offered again")
	}
	h.post("/backups/plan/promote", url.Values{"destination": {"provider:1"}}, h.srv.URL)
	if fm.destination != "provider:1" || fm.copies[0].Ref != "server:n-0123456789" {
		t.Errorf("promote: %q %+v", fm.destination, fm.copies)
	}
	// Changing the main backup keeps the others.
	h.post("/backups/plan", url.Values{"replace": {"1"}, "choice": {"server"}, "server": {"server:n-0123456789"}}, h.srv.URL)
	if fm.destination != "server:n-0123456789" || len(fm.copies) != 1 {
		t.Errorf("replace: %q %+v", fm.destination, fm.copies)
	}
	h.post("/backups/plan/remove", url.Values{"destination": {"server:n-0123456789"}}, h.srv.URL)
	if len(fm.copies) != 0 {
		t.Error("remove")
	}
	// Another server's plan lives under its settings.
	if _, body := h.get("/settings/servers/n-0123456789/backups"); !strings.Contains(body, "Your backup plan") {
		t.Error("server backups page")
	}
	// Admins don't choose destinations.
	ha, _ := loggedIn(t, auth.RoleAdmin)
	if _, body := ha.get("/backups"); strings.Contains(body, "Your backup plan") {
		t.Error("only Owners manage the plan")
	}
}

func TestMovePage(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true},
		{ID: "n-9876543210", Name: "vps"}}
	if _, body := h.get("/services/vaultwarden"); !strings.Contains(body, `href="/services/vaultwarden/move"`) {
		t.Error("service page should offer moving when there are several servers")
	}
	_, body := h.get("/services/vaultwarden/move")
	for _, want := range []string{"Move Vaultwarden to another server", "5.0 MB", `value="n-9876543210"`} {
		if !strings.Contains(body, want) {
			t.Errorf("move page missing %q", want)
		}
	}
	if strings.Contains(body, `value="n-0123456789"`) {
		t.Error("it can't move to where it runs")
	}
	if code, _ := h.post("/services/vaultwarden/move", url.Values{}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("no target: %d", code)
	}
	if !strings.Contains(body, `value="backup"  disabled`) && !strings.Contains(body, `value="backup" disabled`) &&
		!strings.Contains(body, "disabled") {
		t.Error("through a backup needs backups set up through Roostack")
	}
	h.post("/services/vaultwarden/move", url.Values{"to": {"n-9876543210"}, "way": {"copy"}}, h.srv.URL)
	if len(fm.moves) != 1 || fm.moves[0] != "vaultwarden → n-9876543210 (copy)" {
		t.Errorf("moves = %v", fm.moves)
	}
	if code, _ := h.get("/services/nope/move"); code != http.StatusNotFound {
		t.Errorf("unknown service: %d", code)
	}
	ha, _ := loggedIn(t, auth.RoleAdmin)
	if code, _ := ha.get("/services/vaultwarden/move"); code == http.StatusOK {
		t.Error("only Owners move services")
	}
}

func TestStorageForOthers(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.guests = []manager.GuestView{{Guest: store.Guest{ID: 1, Name: "Tyris's lab", Quota: 50 * units.GB, Joined: true},
		Used: 3 * units.GB, LastSeen: time.Now()}}
	_, body := h.get("/settings/storage")
	for _, want := range []string{"Tyris&#39;s lab", "3.0 GB", "50.0 GB", "Data from labs you removed", "g9", "UDP", "51821"} {
		if !strings.Contains(body, want) {
			t.Errorf("storage page missing %q", want)
		}
	}
	if code, body := h.post("/settings/storage/invite", url.Values{"name": {"Sam"}, "quota": {"0"}, "endpoint": {"1.2.3.4:51821"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "at least 1 GB") {
		t.Errorf("zero quota: %d", code)
	}
	_, body = h.post("/settings/storage/invite", url.Values{"name": {"Sam"}, "quota": {"20"}, "endpoint": {"1.2.3.4:51821"}}, h.srv.URL)
	if !strings.Contains(body, "Send this invite to Sam") {
		t.Error("the invite should be shown once")
	}
	h.post("/settings/storage/guests/1/quota", url.Values{"quota": {"80"}}, h.srv.URL)
	h.post("/settings/storage/guests/1/remove", nil, h.srv.URL)
	h.post("/settings/storage/leftover/g9/delete", nil, h.srv.URL)
	if code, _ := h.post("/settings/storage/leftover/g1/delete", nil, h.srv.URL); code != http.StatusNotFound {
		t.Errorf("deleting a current guest's data: %d", code)
	}
	want := []string{"invite Sam 20 1.2.3.4:51821", "quota 1 80", "remove 1", "delete g9"}
	if !slices.Equal(fm.guestOps, want) {
		t.Errorf("ops = %v", fm.guestOps)
	}
	// The guest side: the Backups page shows storage at other labs.
	_, body = h.get("/backups")
	if !strings.Contains(body, "Mum&#39;s lab</strong>: 3.0 GB of 50.0 GB used") || !strings.Contains(body, "no server backs up here yet") {
		t.Errorf("backups page should show storage at other labs")
	}
	// It's the whole lab's: shown on every server's page, naming who uses it.
	fm.destination = "provider:7"
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true}, {ID: "n-9876543210", Name: "vps"}}
	if _, body := h.get("/settings/servers/n-9876543210/backups"); !strings.Contains(body, "used by nuc, vps") {
		t.Errorf("another server's page should show storage at other labs and who uses it")
	}
	if code, _ := h.post("/backups/plan", url.Values{"add": {"2"}, "choice": {"friend"}, "invite": {"hello"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("bad invite: %d", code)
	}
	ha, _ := loggedIn(t, auth.RoleAdmin)
	if code, _ := ha.get("/settings/storage"); code == http.StatusOK {
		t.Error("only Owners")
	}
}

func TestServiceCardsAndBuiltIns(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "vaultwarden", Title: "Vaultwarden", Status: "running", Server: "nuc",
			URLs: []string{"https://vault.lab.example.com"}, LastBackup: time.Now(),
			ServiceDetail: node.ServiceDetail{Containers: []node.Container{{Name: "roostack-vaultwarden-app",
				Image: "docker.io/vaultwarden/server:1.34.1", State: "active (running)"}},
				Routes: []string{"vault.lab.example.com → 127.0.0.1:18000"}, DataDirs: []string{"/srv/roostack/services/vaultwarden"},
				DataSize: 12 * units.MB}}}, nil
	}
	for _, path := range []string{"/", "/services"} {
		_, body := h.get(path)
		for _, want := range []string{`On <span class="server-chip">`, `</svg>nuc</span>`, `id="details-vaultwarden"`, "backed up today", "roostack-vaultwarden-app", "docker.io/vaultwarden/server:1.34.1",
			"vault.lab.example.com → 127.0.0.1:18000", "/srv/roostack/services/vaultwarden", "12.0 MB"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
	}
	if _, body := h.get("/services"); strings.Contains(body, "Roostack&#39;s own services</h2>") {
		t.Error("built-ins are hidden by default")
	}
	_, body := h.get("/services?builtin=1")
	if !strings.Contains(body, "</svg>nuc</span></h3>") || !strings.Contains(body, "Caddy") || !strings.Contains(body, `hx-get="/partials/services?builtin=1"`) {
		t.Errorf("built-ins should show and stay shown:\n%s", body)
	}
}

func (f *fakeManager) ServiceDetails(context.Context) (map[string]node.ServiceDetail, error) {
	return nil, nil
}
func (f *fakeManager) AllBuiltIns(ctx context.Context) []manager.ServerBuiltIns {
	items, _ := f.BuiltIns(ctx)
	return []manager.ServerBuiltIns{{Server: "nuc", Items: items}}
}
func (f *fakeManager) BuiltIns(context.Context) ([]node.BuiltIn, error) {
	return []node.BuiltIn{{Name: "Caddy", Kind: "proxy", Status: "active (running)", Detail: "Web addresses"}}, nil
}

func TestPerServerSettings(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{ssh: "lan", p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}})
	fm.updates = &fakeUpdates{restart: osupdate.RestartStatus{Needed: true}}
	// One server: no switcher.
	if _, body := h.get("/settings/updates"); strings.Contains(body, "server-switcher") {
		t.Error("a single server needs no switcher")
	}
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true, Status: manager.ApplyStatus{Finished: true}},
		{ID: "n-9876543210", Name: "vps", Status: manager.ApplyStatus{Finished: true}}}
	_, body := h.get("/settings/updates")
	for _, want := range []string{"server-switcher", `href="/settings/updates" aria-current="page"`,
		`href="/settings/servers/n-9876543210/updates"`, "Needs a restart to finish updates"} {
		if !strings.Contains(body, want) {
			t.Errorf("updates page missing %q", want)
		}
	}
	// The other server's page marks it current.
	if _, body := h.get("/settings/servers/n-9876543210/time-zone"); !strings.Contains(body, `href="/settings/servers/n-9876543210/time-zone" aria-current="page"`) {
		t.Error("the switcher should mark vps")
	}
	// The overview summarises every server, a line each.
	if _, body := h.get("/settings"); strings.Count(body, `<strong>nuc</strong><br><span class="area-summary">SSH from your home network only`) != 1 ||
		strings.Count(body, `<strong>vps</strong><br><span class="area-summary">SSH from your home network only`) != 1 {
		t.Errorf("overview should summarise each server")
	}
	// A server's own page.
	_, body = h.get("/settings/servers/n-9876543210")
	if !strings.Contains(body, "<h2>vps") || strings.Contains(body, "<h2>nuc") || !strings.Contains(body, "<h3>Settings</h3>") ||
		strings.Contains(body, "Add a server") && strings.Contains(body, "Make a join code") {
		t.Errorf("server page:\n%s", body)
	}
	if code, _ := h.get("/settings/servers/n-1111111111"); code != http.StatusNotFound {
		t.Errorf("unknown server: %d", code)
	}
	// Home: My Servers, with a link to each server's settings.
	if _, body := h.get("/"); !strings.Contains(body, "My Servers") || !strings.Contains(body, `href="/settings/servers/n-9876543210"`) ||
		!strings.Contains(body, "Needs a restart to finish updates") {
		t.Errorf("home should show My Servers")
	}
}

func TestBackupsPageRestoresThisServersServices(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.node().restores = &fakeRestores{}
	// Every service names its server, this one's included.
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "vaultwarden", Title: "Vaultwarden", Server: "nuc"}, {Name: "forgejo", Title: "Forgejo", Server: "vps"}}, nil
	}
	_, body := h.get("/backups")
	if strings.Contains(body, "No services installed") || !strings.Contains(body, `href="/backups/restore/vaultwarden"`) ||
		strings.Contains(body, `href="/backups/restore/forgejo"`) {
		t.Error("this server's services should be restorable")
	}
}

func TestCertificateExpiry(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true, CertExpires: time.Now().Add(300 * 24 * time.Hour)},
		{ID: "n-9876543210", Name: "vps", CertExpires: time.Now().Add(10 * 24 * time.Hour)}}
	if _, body := h.get("/settings/servers"); strings.Count(body, "Certificate</dt>") != 2 || !strings.Contains(body, "<dd>Expires ") {
		t.Error("Servers should show each certificate")
	}
	_, body := h.get("/")
	if !strings.Contains(body, "vps&#39;s certificate on Roostack&#39;s network expires") || strings.Contains(body, "nuc&#39;s certificate") {
		t.Errorf("a certificate that isn't renewing should need attention")
	}
	fm.servers[1].CertExpires = time.Now().Add(-time.Hour)
	if _, body := h.get("/"); !strings.Contains(body, "has expired") {
		t.Error("an expired certificate")
	}
}

func TestLabName(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	h.setSetup(fs)
	h.server.LabName = "lab.example.com"
	if _, body := h.get("/settings"); !strings.Contains(body, `<span class="lab-name">lab.example.com</span>`) ||
		!strings.Contains(body, "Lab name") {
		t.Error("the header and Settings should show the lab's name")
	}
	if code, _ := h.post("/settings/lab", url.Values{"name": {" "}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("an empty name: %d", code)
	}
	h.post("/settings/lab", url.Values{"name": {"The Smiths' lab"}}, h.srv.URL)
	if fs.labName != "The Smiths' lab" || !fs.restarted {
		t.Errorf("rename: %q %v", fs.labName, fs.restarted)
	}
}

func TestRemovedGuestLabs(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.guests = []manager.GuestView{
		{Guest: store.Guest{ID: 1, Name: "Sam's lab", Joined: true, Quota: 10 * units.GB}, Used: 1 * units.GB},
		{Guest: store.Guest{ID: 2, Name: "Old lab", Joined: true, Quota: 10 * units.GB, Removed: time.Now().Add(-24 * time.Hour)}, Used: 2 * units.GB},
	}
	_, body := h.get("/settings/storage")
	i := strings.Index(body, "Labs you removed")
	if i < 0 || !strings.Contains(body[i:], "Old lab") || strings.Contains(body[:i], "Old lab") || !strings.Contains(body[i:], "deleted on") {
		t.Fatalf("removed labs are listed apart:\n%s", body)
	}
	h.post("/settings/storage/guests/2/delete", nil, h.srv.URL)
	if !slices.Contains(fm.guestOps, "delete now 2") {
		t.Errorf("ops = %v", fm.guestOps)
	}
	// The guest's side: a host that stopped storing its backups needs attention.
	fm.ending = []manager.EndingStorage{{HostName: "Mum's lab", DeleteOn: time.Now().Add(29 * 24 * time.Hour)}}
	if _, body := h.get("/"); !strings.Contains(body, "Mum&#39;s lab has stopped storing your backups") {
		t.Error("an ending storage needs attention")
	}
}

func TestRetryInstallWithChangedSettings(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "forgejo", Title: "Forgejo", Status: "running"}}, nil
	}
	fm.values = map[string]any{"admin_username": "Tyris Smith", "admin_email": "tyris@example.com", "signups_allowed": false}
	fm.setup = map[string]manager.HookStatus{"forgejo": {Failed: "username is invalid"}}
	_, body := h.get("/services/forgejo")
	if !strings.Contains(body, "Change what you entered") || !strings.Contains(body, `name="setting.admin_username" value="Tyris Smith"`) ||
		!strings.Contains(body, "Leave empty to keep what you typed before") {
		t.Fatalf("a failed install offers its settings:\n%s", body)
	}
	form := url.Values{"setting.admin_username": {""}, "setting.admin_email": {"tyris@example.com"}}
	if code, body := h.post("/services/forgejo/setup/retry-with", form, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "username is required") {
		t.Errorf("an empty username: %d", code)
	}
	form.Set("setting.admin_username", "tyris")
	form.Set("setting.admin_password", "a better password")
	h.post("/services/forgejo/setup/retry-with", form, h.srv.URL)
	if !slices.Equal(fm.retriedWith, []string{"forgejo tyris a better password"}) {
		t.Errorf("retried with %v", fm.retriedWith)
	}
}

func TestHostChanges(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}})
	up := &fakeUpdates{}
	up.changes.AptSources = []string{"docker.list: https://download.docker.com/linux/debian"}
	up.changes.Held = []string{"linux-image-amd64"}
	fm.updates = up
	if _, body := h.get("/"); !strings.Contains(body, "Changes Roostack didn&#39;t make: software from outside Debian, packages held back from updates") {
		t.Error("host changes need attention")
	}
	_, body := h.get("/settings/updates")
	if !strings.Contains(body, `id="host-changes"`) || !strings.Contains(body, "download.docker.com") || !strings.Contains(body, "I made these changes") {
		t.Fatalf("the updates page lists them:\n%s", body)
	}
	h.post("/settings/host-changes/known", nil, h.srv.URL)
	if !up.known {
		t.Fatal("marking them known")
	}
	if _, body := h.get("/"); strings.Contains(body, "Changes Roostack didn") {
		t.Error("known changes don't need attention")
	}
	if _, body := h.get("/settings/updates"); !strings.Contains(body, "You said these were made on purpose") {
		t.Error("the page says they're known")
	}
}

func TestStorageOfferedByAnotherServer(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true}, {ID: "n-9876543210", Name: "bigdisk"}}
	_, body := h.get("/settings/storage")
	if !strings.Contains(body, `name="server"`) || !strings.Contains(body, "Address they reach nuc at") {
		t.Fatalf("the offering server can be chosen:\n%s", body)
	}
	h.post("/settings/storage/server", url.Values{"server": {"n-9876543210"}}, h.srv.URL)
	if fm.guestServer != "n-9876543210" {
		t.Fatalf("server = %q", fm.guestServer)
	}
	if _, body := h.get("/settings/storage"); !strings.Contains(body, "Address they reach bigdisk at") || !strings.Contains(body, "to bigdisk") {
		t.Error("the page names the server other labs reach")
	}
	fm.guests = []manager.GuestView{{Guest: store.Guest{ID: 1, Name: "Sam's lab", Joined: true}}}
	if code, body := h.post("/settings/storage/server", url.Values{"server": {"n-0123456789"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "Other labs store backups") {
		t.Errorf("can't move while a lab stores there: %d", code)
	}
}

func TestHomeShowsWhatAnApplyDoes(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	log := h.acts().Start(activity.Apply, "Applying changes")
	log.Step("Starting roostack-caddy: builds its image the first time (compiling: several minutes)")
	if _, body := h.get("/"); !strings.Contains(body, "builds its image the first time") {
		t.Error("the home page shows what the apply is doing")
	}
	log.Finish(nil)
	if _, body := h.get("/"); strings.Contains(body, "builds its image the first time") {
		t.Error("a finished apply isn't shown")
	}
}

// Another server's disk is at home or away as the Owner says (SPEC.md
// §6.9): asked on the plan, and on the Servers page.
func TestBackupOnAnotherServerLocation(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true}, {ID: "n-9876543210", Name: "vps"}}
	fm.destination = "server:n-9876543210"
	_, body := h.get("/backups")
	if !strings.Contains(body, "Where is nuc?") || !strings.Contains(body, "Where is vps?") || !strings.Contains(body, "at home or away?") {
		t.Fatal("the plan should ask where both servers are")
	}
	h.post("/settings/servers/n-0123456789/location", url.Values{"location": {"home"}, "back": {"/backups#plan"}}, h.srv.URL)
	if code, _ := h.post("/settings/servers/n-9876543210/location", url.Values{"location": {"moon"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("unknown location: %d", code)
	}
	h.post("/settings/servers/n-9876543210/location", url.Values{"location": {"away"}}, h.srv.URL)
	_, body = h.get("/backups")
	if strings.Contains(body, "Where is") || !strings.Contains(body, "away from home") ||
		!strings.Contains(body, `<li class="plan-ok"><span class="plan-mark" aria-hidden="true">✓</span>1 away from home`) {
		t.Error("vps somewhere else: the backup there is away from home")
	}
	if _, body := h.get("/settings/servers"); !strings.Contains(body, `name="location" value="away" checked`) {
		t.Error("the Servers page shows where each server is")
	}
	// Both at home: at home.
	h.post("/settings/servers/n-9876543210/location", url.Values{"location": {"home"}}, h.srv.URL)
	if _, body := h.get("/backups"); !strings.Contains(body, "Another Roostack instance · at home") {
		t.Error("both at home")
	}
}

// A backup on the server's own disk says what it doesn't protect against.
func TestBackupOnTheSameDiskWarns(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.destination = "provider:1"
	if _, body := h.get("/backups"); strings.Contains(body, "same disk as your data") {
		t.Error("online storage isn't on the same disk")
	}
	fm.destination = "drive:system"
	if _, body := h.get("/backups"); !strings.Contains(body, "if the disk fails, or ransomware gets onto the server") {
		t.Error("a backup on this server's disk should warn")
	}
}

// Each backup shows the space it takes, and what's free where that's known.
func TestBackupPlanSpace(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}})
	fm.destination = "drive:system"
	h.node().backups = &fakeBackups{status: backup.Status{Configured: true, Progress: -1,
		LastOK: &store.BackupRun{OK: true, Finished: time.Now(), Stored: uint64(1536 * units.MB)}}}
	_, body := h.get("/backups")
	if !strings.Contains(body, "Backups here take <strong>1.5 GB</strong> · <span>30.0 GB free on this server&#39;s disk</span>") {
		t.Errorf("space on the main backup:\n%s", body[strings.Index(body, `id="plan"`):])
	}
}

func TestLowSpace(t *testing.T) {
	for _, c := range []struct {
		free, total int64
		want        string
	}{
		{100 * units.GB, 200 * units.GB, ""},
		{15 * units.GB, 200 * units.GB, levelWarning}, // under 10%
		{4 * units.GB, 20 * units.GB, levelWarning},   // under 5 GB
		{8 * units.GB, 200 * units.GB, levelProblem},  // under 5%
		{1 * units.GB, 10 * units.GB, levelProblem},   // under 2 GB
		{-1, 10 * units.GB, ""},
	} {
		if got := lowSpace(c.free, c.total); got != c.want {
			t.Errorf("lowSpace(%d, %d) = %q, want %q", c.free, c.total, got, c.want)
		}
	}
}

// Settings → Servers shows each server's disk; the home page warns when
// one runs low, or when other labs were promised more than is free.
func TestServerDiskSpace(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.disk = &node.DiskSpace{Total: 100 * units.GB, Used: 40 * units.GB, Free: 60 * units.GB, Services: 12 * units.GB, Backups: 3 * units.GB, Guests: 1 * units.GB}
	_, body := h.get("/settings/servers")
	for _, want := range []string{"<strong>60.0 GB free</strong> of 100.0 GB", "<dt>In use</dt><dd>40.0 GB</dd>",
		"Services' data</dt><dd>12.0 GB", "Backups of your lab's servers</dt><dd>3.0 GB", "Backups of other labs</dt><dd>1.0 GB",
		"The system and everything else</dt><dd>24.0 GB", `style="width: 40%"`} {
		if !strings.Contains(body, want) {
			t.Errorf("servers page missing %q", want)
		}
	}
	if _, body := h.get("/"); strings.Contains(body, "disk space") {
		t.Error("plenty of space: nothing to say")
	}
	fm.guests = []manager.GuestView{{Guest: store.Guest{ID: 1, Name: "Tyris's lab", Quota: 80 * units.GB, Joined: true}, Used: 5 * units.GB}}
	if _, body := h.get("/"); !strings.Contains(body, "Other labs could fill This server&#39;s disk: they&#39;ve been promised 75.0 GB more") {
		t.Error("promised more than is free")
	}
	fm.guests = nil
	fm.disk.Free = 3 * units.GB
	if _, body := h.get("/"); !strings.Contains(body, "This server is running out of disk space: 3.0 GB free of 100.0 GB") {
		t.Error("low space should need attention")
	}
}
