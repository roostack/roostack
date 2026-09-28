package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"roostack.cloud/roostack/internal/pkgspec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
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

func (f *fakeExec) exec(_ context.Context, container string, env map[string]string, cmd []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, env)
	if container != "roostack-forgejo-app" || cmd[0] != "sh" {
		return nil, errors.New("unexpected command")
	}
	if f.fail != nil && f.fail(len(f.calls)) {
		return []byte("database not ready"), errors.New("exit status 1")
	}
	return []byte("ok"), nil
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

	var fe *FormError
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
	var fe *FormError
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

	var fe *FormError
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
	var fe *FormError
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
