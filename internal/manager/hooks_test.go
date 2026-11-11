package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/systemd"
	"roostack.cloud/roostack/internal/testwait"
	"roostack.cloud/roostack/internal/usererr"
)

type memKV struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *memKV) Setting(_ context.Context, key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[key], nil
}
func (k *memKV) SetSetting(_ context.Context, key, v string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = v
	return nil
}

// fakeExec records podman exec calls; fail decides whether call n fails.
type fakeExec struct {
	mu    sync.Mutex
	calls []map[string]string
	fail  func(n int) bool
}

func (f *fakeExec) exec(_ context.Context, container string, env map[string]string, cmd []string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, env)
	if container != "roostack-forgejo-app" || cmd[0] != "sh" {
		return nil, nil, errors.New("unexpected command")
	}
	if f.fail != nil && f.fail(len(f.calls)) {
		return nil, []byte("database not ready"), errors.New("exit status 1")
	}
	return []byte("ok"), nil, nil
}

func newHookManager(t *testing.T) (*Manager, *fakeSecrets, *memKV, *fakeExec) {
	m, secrets := newManager(t)
	addForgejo(t, m)
	kv, ex := &memKV{m: map[string]string{}}, &fakeExec{}
	n := local(m)
	n.State, n.Secrets, n.Exec = kv, secrets, ex.exec
	n.HookRetry, n.HookDeadline = time.Millisecond, time.Second
	m.Activity = activity.New()
	n.Activity = m.Activity
	return m, secrets, kv, ex
}

func TestInstallHook(t *testing.T) {
	m, secrets, _, ex := newHookManager(t)
	ctx := context.Background()
	ex.fail = func(n int) bool { return n == 1 } // not ready at first

	var fe *usererr.Error
	short := forgejoRequest()
	short.Secrets["admin_password"] = "short"
	if err := m.Install(ctx, "forgejo", short); !errors.As(err, &fe) || !strings.Contains(fe.Msg, "at least 8") {
		t.Fatalf("short password: %v", err)
	}

	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()

	if st := local(m).InstallStatus(ctx, "forgejo"); st.Pending || st.Running || st.Failed != "" {
		t.Errorf("status = %+v, want done", st)
	}
	if len(ex.calls) != 2 {
		t.Fatalf("exec calls = %d, want a retry after the first failure", len(ex.calls))
	}
	env := ex.calls[1]
	if env["ADMIN_USERNAME"] != "mum" || env["ADMIN_EMAIL"] != "mum@example.com" || env["ADMIN_PASSWORD"] != "correct horse" {
		t.Errorf("env = %v", env)
	}
	if _, kept := secrets.values["roostack-forgejo-admin_password"]; kept {
		t.Error("the admin password must be deleted once the hook succeeded")
	}
	setupLog, _ := m.Activity.Latest(activity.Setup)
	logText := strings.Join(setupLog.Lines, "\n")
	if setupLog.Title != "Setting up Forgejo" || !strings.Contains(logText, "database not ready") ||
		!strings.Contains(logText, "trying again") || strings.Contains(logText, "correct horse") || setupLog.Err != "" {
		t.Errorf("setup log:\n%s", logText)
	}
	applyLog, _ := m.Activity.Latest(activity.Apply)
	if applyLog.Running() || !slices.Contains(applyLog.Lines, "==> Writing configuration") {
		t.Errorf("apply log:\n%s", strings.Join(applyLog.Lines, "\n"))
	}

	// Later applies neither need the deleted secret nor run the hook again.
	m.Apply()
	m.Wait()
	local(m).WaitHooks()
	if st := m.Status(); st.LastErr != nil {
		t.Errorf("apply after the secret was deleted: %v", st.LastErr)
	}
	if len(ex.calls) != 2 {
		t.Errorf("hook ran again: %d calls", len(ex.calls))
	}
}

func TestInstallHookFailsThenRetry(t *testing.T) {
	t.Parallel()
	m, secrets, _, ex := newHookManager(t)
	ctx := context.Background()
	failing := true
	ex.fail = func(int) bool { return failing }

	m.Install(ctx, "forgejo", forgejoRequest())
	m.Wait()
	local(m).WaitHooks()
	st := local(m).InstallStatus(ctx, "forgejo")
	if !strings.Contains(st.Failed, "database not ready") {
		t.Fatalf("status = %+v, want the command's output", st)
	}
	if _, kept := secrets.values["roostack-forgejo-admin_password"]; !kept {
		t.Fatal("the password must be kept until the hook succeeds")
	}

	failing = false
	if err := local(m).RetryInstall(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	local(m).WaitHooks()
	if st := local(m).InstallStatus(ctx, "forgejo"); st.Failed != "" || st.Pending {
		t.Errorf("after retry: %+v", st)
	}
}

// A hook that failed on what was typed runs again with corrected values:
// new settings, and a new password replacing the one kept.
func TestRetryInstallWithNewValues(t *testing.T) {
	t.Parallel()
	m, secrets, _, ex := newHookManager(t)
	ctx := context.Background()
	ex.fail = func(int) bool { return true }
	m.Install(ctx, "forgejo", forgejoRequest())
	m.Wait()
	local(m).WaitHooks()
	if st := local(m).InstallStatus(ctx, "forgejo"); st.Failed == "" {
		t.Fatal("the hook should fail")
	}
	if got := InstallSettings(m.mustPackage(t, "forgejo")); len(got) != 3 {
		t.Errorf("install settings = %+v", got)
	}
	var fe *usererr.Error
	if err := m.RetryInstallWith(ctx, "forgejo", map[string]string{"admin_username": "", "admin_email": "x@example.com"}, nil); !errors.As(err, &fe) {
		t.Errorf("an empty username: %v", err)
	}
	ex.fail = func(int) bool { return false }
	if err := m.RetryInstallWith(ctx, "forgejo", map[string]string{"admin_username": "tyris", "admin_email": "tyris@example.com"},
		map[string]string{"admin_password": "a better password"}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()
	if st := local(m).InstallStatus(ctx, "forgejo"); st.Failed != "" || st.Pending {
		t.Fatalf("after retrying with new values: %+v", st)
	}
	env := ex.calls[len(ex.calls)-1]
	if env["ADMIN_USERNAME"] != "tyris" || env["ADMIN_PASSWORD"] != "a better password" {
		t.Errorf("env = %v", env)
	}
	if _, kept := secrets.values["roostack-forgejo-admin_password"]; kept {
		t.Error("the password goes once the hook succeeded")
	}
}

func TestRunAction(t *testing.T) {
	m, _, _, ex := newHookManager(t)
	ctx := context.Background()
	m.Install(ctx, "forgejo", forgejoRequest())
	m.Wait()
	local(m).WaitHooks()

	var fe *usererr.Error
	if _, err := local(m).RunAction(ctx, "forgejo", "reset-password", map[string]string{"username": "mum", "password": "short"}); !errors.As(err, &fe) {
		t.Errorf("short password: %v", err)
	}
	if _, err := local(m).RunAction(ctx, "forgejo", "nope", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown action: %v", err)
	}
	out, err := local(m).RunAction(ctx, "forgejo", "reset-password", map[string]string{"username": " mum ", "password": "a new password"})
	if err != nil || out != "ok" {
		t.Fatalf("out %q, err %v", out, err)
	}
	env := ex.calls[len(ex.calls)-1]
	if env["USERNAME"] != "mum" || env["NEW_PASSWORD"] != "a new password" {
		t.Errorf("env = %v", env)
	}

	ex.fail = func(int) bool { return true }
	var ae *ActionError
	if _, err := local(m).RunAction(ctx, "forgejo", "reset-password", map[string]string{"username": "x", "password": "a new password"}); !errors.As(err, &ae) ||
		!strings.Contains(ae.Output, "database not ready") {
		t.Errorf("failed action: %v", err)
	}
}

func TestUpdateSettingsAndReveal(t *testing.T) {
	m, secrets, _, _ := newHookManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()

	values, sub, err := m.CurrentSettings("forgejo")
	if err != nil || sub != "git" || values["signups_allowed"] != false {
		t.Fatalf("current = %v %q %v", values, sub, err)
	}
	var fe *usererr.Error
	if err := m.UpdateSettings(ctx, "forgejo", SettingsRequest{Subdomain: "vault"}); !errors.As(err, &fe) {
		t.Errorf("taking vaultwarden's address: %v", err)
	}
	if err := m.UpdateSettings(ctx, "forgejo", SettingsRequest{Subdomain: "Code",
		Settings: map[string]string{"signups_allowed": "on", "admin_username": "hacker"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	values, sub, _ = m.CurrentSettings("forgejo")
	if sub != "code" || values["signups_allowed"] != true || values["admin_username"] != "mum" {
		t.Errorf("after update: %v %q (install-only settings must not change)", values, sub)
	}
	unit, _ := os.ReadFile(filepath.Join(m.Lab.Paths.QuadletDir(), "roostack-forgejo-app.container"))
	if !strings.Contains(string(unit), "ROOT_URL=https://code.lab.example.com/") ||
		!strings.Contains(string(unit), "DISABLE_REGISTRATION=false") {
		t.Errorf("unit not regenerated:\n%s", unit)
	}

	if v, err := local(m).RevealSecret(ctx, "vaultwarden", "admin_token"); err != nil || v == "" ||
		v != string(secrets.values["roostack-vaultwarden-admin_token"]) {
		t.Errorf("reveal: %q %v", v, err)
	}
	if _, err := local(m).RevealSecret(ctx, "forgejo", "admin_password"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revealing a non-revealable secret: %v", err)
	}
}

func (m *Manager) mustPackage(t *testing.T, name string) *pkgspec.Package {
	t.Helper()
	p, err := m.Package(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestInstallOverLeftData: over a service's data from before, its
// first-time settings may be left empty: it installs without them and its
// install hooks don't run (the app keeps its accounts); filled in, they
// run as on a fresh install. Without that data, they're asked for.
func TestInstallOverLeftData(t *testing.T) {
	m, _, kv, ex := newHookManager(t)
	ctx := context.Background()
	var fe *usererr.Error
	if err := m.Install(ctx, "forgejo", InstallRequest{}); !errors.As(err, &fe) {
		t.Fatalf("no data from before, setup left empty: %v", err)
	}
	data := filepath.Join(m.Lab.Paths.ServicesDir, "forgejo")
	os.MkdirAll(filepath.Join(data, "data"), 0o755)
	if sum, err := m.InstallSummary("forgejo", InstallRequest{}); err != nil || !slices.Equal(sum.LeftData, []string{data}) {
		t.Fatalf("the summary's data from before: %v %v", sum, err)
	}
	if err := m.Install(ctx, "forgejo", InstallRequest{}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()
	if len(ex.calls) != 0 || kv.m["hooks.forgejo.install"] != "" {
		t.Errorf("setup ran over the data from before: %d calls, %q", len(ex.calls), kv.m["hooks.forgejo.install"])
	}
	_, in, err := m.installedService("forgejo")
	if err != nil || in.Settings["admin_username"] != nil {
		t.Errorf("installed with %v, %v", in, err)
	}

	// Filled in: setup runs again (e.g. another administrator).
	m.Remove(ctx, "forgejo")
	m.Wait()
	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()
	if len(ex.calls) != 1 || ex.calls[0]["ADMIN_USERNAME"] != "mum" {
		t.Errorf("setup with the settings filled in: %v", ex.calls)
	}
}

// Removing a service while its setup keeps retrying stops the setup, which
// isn't recorded as failed; installing it again then sets it up afresh
// rather than waiting behind the old setup until its deadline.
func TestInstallHookStoppedByRemoval(t *testing.T) {
	m, _, kv, ex := newHookManager(t)
	ctx := context.Background()
	local(m).HookDeadline = time.Minute
	var ready atomic.Bool // until then, every call fails
	ex.fail = func(int) bool { return !ready.Load() }

	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	testwait.For(t, 5*time.Second, func() bool { // the setup has run
		ex.mu.Lock()
		defer ex.mu.Unlock()
		return len(ex.calls) > 0
	})
	if err := m.Remove(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	stopped := make(chan struct{})
	go func() { local(m).WaitHooks(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the removed service's setup kept retrying")
	}
	if v, _ := kv.Setting(ctx, "hooks.forgejo.install"); v != "" {
		t.Errorf("setup state after removal = %q, want none", v)
	}

	ready.Store(true)
	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()
	if st := local(m).InstallStatus(ctx, "forgejo"); st.Pending || st.Running || st.Failed != "" {
		t.Errorf("status after installing again = %+v, want done", st)
	}
}

// Single sign-on's hook (SPEC.md §5.2): run once its install hooks are
// done, told on with the client secret, not again for the same values,
// then told off without it, after which the client secret goes.
func TestSSOHook(t *testing.T) {
	m, secrets, _, ex := newHookManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()
	if err := m.SetSSO(ctx, "forgejo", true); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()
	sso := func(on string) []map[string]string {
		var out []map[string]string
		for _, c := range ex.calls {
			if c["ROOSTACK_SSO"] == on {
				out = append(out, c)
			}
		}
		return out
	}
	ons := sso("on")
	if len(ons) != 1 {
		t.Fatalf("on runs: %d of %d calls", len(ons), len(ex.calls))
	}
	secret := string(secrets.values["roostack-forgejo-sso-client-secret"])
	if secret == "" || ons[0]["CLIENT_SECRET"] != secret || ons[0]["SOURCE_NAME"] != "lab.example.com" {
		t.Errorf("on run's env: %v", ons[0])
	}
	if st := local(m).InstallStatus(ctx, "forgejo").SSO; !st.On || st.Failed != "" || st.Running {
		t.Errorf("status %+v", st)
	}
	setupLog, _ := m.Activity.Latest(activity.Setup)
	if setupLog.Title != "Connecting Forgejo to the sign-in page" || strings.Contains(strings.Join(setupLog.Lines, "\n"), secret) {
		t.Errorf("log %q:\n%s", setupLog.Title, strings.Join(setupLog.Lines, "\n"))
	}

	// Nothing changed: not run again.
	m.Apply()
	m.Wait()
	local(m).WaitHooks()
	if n := len(sso("on")); n != 1 {
		t.Errorf("ran again: %d", n)
	}

	if err := m.SetSSO(ctx, "forgejo", false); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	local(m).WaitHooks()
	offs := sso("off")
	if v, ok := offs[0]["CLIENT_SECRET"]; len(offs) != 1 || !ok || v != "" {
		t.Fatalf("off runs: %v", offs)
	}
	if _, kept := secrets.values["roostack-forgejo-sso-client-secret"]; kept {
		t.Error("the client secret must go once it's off")
	}
	if st := local(m).InstallStatus(ctx, "forgejo").SSO; st.On || st.Failed != "" {
		t.Errorf("status %+v", st)
	}
}

// A failed sso hook says why on the service's page, and Try again runs it
// once more.
func TestSSOHookFailsThenRetry(t *testing.T) {
	t.Parallel()
	m, _, _, ex := newHookManager(t)
	ctx := context.Background()
	failing := false
	ex.fail = func(int) bool { return failing }
	m.Install(ctx, "forgejo", forgejoRequest())
	m.Wait()
	local(m).WaitHooks()
	failing = true
	m.SetSSO(ctx, "forgejo", true)
	m.Wait()
	local(m).WaitHooks()
	st := local(m).InstallStatus(ctx, "forgejo")
	if !strings.Contains(st.SSO.Failed, "database not ready") || st.Failed != "" {
		t.Fatalf("status %+v", st)
	}
	calls := len(ex.calls)
	m.Apply() // the same values: waits for Try again
	m.Wait()
	local(m).WaitHooks()
	if len(ex.calls) != calls {
		t.Errorf("ran again without Try again")
	}
	failing = false
	if err := local(m).RetryInstall(ctx, "forgejo"); err != nil {
		t.Fatal(err)
	}
	local(m).WaitHooks()
	if st := local(m).InstallStatus(ctx, "forgejo").SSO; !st.On || st.Failed != "" {
		t.Errorf("after Try again: %+v", st)
	}
}

// Installing with single sign-on ticked turns it on from the start (SPEC.md
// §5.2), and its hook runs as when it's turned on later; on another
// server, or for a lab without an address, the tick is ignored.
func TestInstallWithSSO(t *testing.T) {
	m, _, _, _ := newHookManager(t)
	ctx := context.Background()
	p, err := m.Package("forgejo")
	if err != nil {
		t.Fatal(err)
	}
	if !m.SSOAtInstall(p, "") || m.SSOAtInstall(p, "n-0000000099") {
		t.Error("offered on another server, or not on this one")
	}
	req := forgejoRequest()
	req.SSO = true
	if err := m.Install(ctx, "forgejo", req); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if ok, _ := m.SSOAvailable("forgejo"); !ok {
		t.Fatal("not available")
	}
	if _, in, err := m.installedService("forgejo"); err != nil || !in.SSO {
		t.Errorf("installed without single sign-on: %v", err)
	}
	m.Lab.Domain = ""
	if m.SSOAtInstall(p, "") {
		t.Error("offered without the lab's address")
	}
}

// notes has an install-only password in its config file and no install
// hooks: the app makes its administrator from the file when it starts.
const notes = `schema: roostack/v1alpha1
name: notes
title: Notes
description: Keeps notes
version: 1.0.0-roostack.1
about: { maker: Notes Inc, licence: MIT, website: "https://notes.example.com", source: "https://notes.example.com/src", icon: icon.png, categories: [documents] }
requires: { roostack: "0.1" }
settings:
  - { key: admin_password, title: Administrator password, type: secret, min_length: 8, install_only: true }
containers:
  app:
    image: example.org/notes:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
    memory: 50 MB
    mounts: [{ storage: config, path: /config }]
storage:
  config: { class: replaceable }
config_files:
  main: { title: Settings, storage: config, path: notes.json, format: json, content: '{"password": {{ .Secrets.admin_password }}}' }
routes:
  web: { container: app, port: 8080, subdomain: notes, public: { login_paths: [/login] } }
`

// unitsStarting says a service's units are still starting until started.
type unitsStarting struct{ started atomic.Bool }

func (u *unitsStarting) Status(_ context.Context, units ...string) ([]systemd.UnitStatus, error) {
	state := map[bool]string{true: "active", false: "activating"}[u.started.Load()]
	var out []systemd.UnitStatus
	for _, n := range units {
		out = append(out, systemd.UnitStatus{Unit: n, ActiveState: state})
	}
	return out, nil
}

// A package with install-only secrets and no install hooks has finished
// setting up once it has started (SPEC.md §8.1): its addresses wait until
// then, and then the password is forgotten and its config file is written
// again without it.
func TestInstallOnlyWithoutHooks(t *testing.T) {
	m, secrets, _, _ := newHookManager(t)
	ctx := context.Background()
	dir := m.Lab.Paths.CatalogPackageDir("notes")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "1.0.0-roostack.1.yaml"), []byte(notes), 0o644)
	icon, _ := os.ReadFile(filepath.Join(filepath.Dir(pkgtest.File("immich")), "icon.png"))
	os.WriteFile(filepath.Join(dir, "icon.png"), icon, 0o644)
	units := &unitsStarting{}
	local(m).Units = units
	local(m).SetUpDone = m.SetUpDone

	if err := m.Install(ctx, "notes", InstallRequest{Secrets: map[string]string{"admin_password": "correct horse"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	file := filepath.Join(m.Lab.Paths.ServicesDir, "notes", "config", "notes.json")
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "correct horse") {
		t.Fatalf("before it has started, the file has the password: %q", b)
	}
	testwait.For(t, 5*time.Second, func() bool { return local(m).InstallStatus(ctx, "notes").Running })
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "notes")); in == nil || !in.SettingUp {
		t.Error("its addresses must wait for it to start")
	}
	if _, kept := secrets.values["roostack-notes-admin_password"]; !kept {
		t.Error("the password must be kept until it has started")
	}

	units.started.Store(true)
	local(m).WaitHooks()
	m.Wait()
	if st := local(m).InstallStatus(ctx, "notes"); !st.Done {
		t.Errorf("status = %+v, want done", st)
	}
	if _, kept := secrets.values["roostack-notes-admin_password"]; kept {
		t.Error("the password must be forgotten once it has started")
	}
	testwait.For(t, 5*time.Second, func() bool {
		m.Wait()
		b, _ := os.ReadFile(file)
		return string(b) == `{"password": ""}`+"\n"
	})
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "notes")); in == nil || in.SettingUp {
		t.Error("it has finished setting up")
	}
	setupLog, _ := m.Activity.Latest(activity.Setup)
	if logText := strings.Join(setupLog.Lines, "\n"); !strings.Contains(logText, "Waiting for Notes to start") ||
		setupLog.Status != "Notes has started" || strings.Contains(logText, "correct horse") {
		t.Errorf("setup log:\n%s", logText)
	}
}
