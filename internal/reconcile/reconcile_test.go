package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
)

type fakeSystemd struct{ calls []string }

func (f *fakeSystemd) record(verb string, units []string) error {
	if len(units) > 0 || verb == "daemon-reload" {
		f.calls = append(f.calls, strings.TrimSpace(verb+" "+strings.Join(units, " ")))
	}
	return nil
}
func (f *fakeSystemd) DaemonReload(context.Context) error { return f.record("daemon-reload", nil) }
func (f *fakeSystemd) Start(_ context.Context, u ...string) error {
	return f.record("start", u)
}
func (f *fakeSystemd) StartNoBlock(_ context.Context, u ...string) error {
	return f.record("start --no-block", u)
}
func (f *fakeSystemd) Restart(_ context.Context, u ...string) error {
	return f.record("restart", u)
}
func (f *fakeSystemd) Stop(_ context.Context, u ...string) error { return f.record("stop", u) }
func (f *fakeSystemd) ReloadOrRestart(_ context.Context, u ...string) error {
	return f.record("reload-or-restart", u)
}

type fakeSecrets struct{ values map[string][]byte }

func (f *fakeSecrets) List(context.Context) (map[string]bool, error) {
	names := map[string]bool{}
	for k := range f.values {
		names[k] = true
	}
	return names, nil
}
func (f *fakeSecrets) Create(_ context.Context, name string, v []byte) error {
	f.values[name] = v
	return nil
}

type env struct {
	lab     config.Lab
	r       *Reconciler
	systemd *fakeSystemd
	secrets *fakeSecrets
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	lab := config.Default()
	lab.Domain = "lab.example.com"
	lab.DNS.Provider = "cloudflare"
	lab.Paths = config.Paths{
		StateDir:    filepath.Join(root, "state"),
		HomeDir:     filepath.Join(root, "home"),
		ServicesDir: filepath.Join(root, "services"),
		MediaDir:    filepath.Join(root, "media"),
	}
	e := &env{lab: lab, systemd: &fakeSystemd{}, secrets: &fakeSecrets{values: map[string][]byte{}}}
	e.r = &Reconciler{
		Paths: lab.Paths, Systemd: e.systemd, Secrets: e.secrets,
		UserSecret: func(name string) ([]byte, bool) {
			return []byte("dns-token"), name == dns.SecretName("api_token")
		},
	}
	return e
}

func (e *env) desired(t *testing.T, services ...string) Desired {
	t.Helper()
	sys, err := generate.System(e.lab, false)
	if err != nil {
		t.Fatal(err)
	}
	d := Desired{System: sys}
	for _, name := range services {
		p, err := pkgspec.Load(filepath.Join("..", "..", "docs", "examples", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		out, err := generate.Service(e.lab, p, &instance.Instance{Package: name, Version: p.Version, Updates: "notify",
			Routes: map[string]instance.RouteOverride{"web": {Port: 20001}}})
		if err != nil {
			t.Fatal(err)
		}
		d.Services = append(d.Services, out)
	}
	return d
}

func (e *env) apply(t *testing.T, d Desired) *Plan {
	t.Helper()
	ctx := context.Background()
	plan, err := e.r.Plan(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.r.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestInstallUpdateRemove(t *testing.T) {
	e := newEnv(t)

	// First apply: everything is created and started.
	plan := e.apply(t, e.desired(t, "vaultwarden"))
	if !plan.DaemonReload || !plan.ReloadCaddy || len(plan.RestartUnits) != 0 {
		t.Errorf("first apply plan unexpected:\n%s", plan)
	}
	// New containers say what their first start involves, and start one by
	// one after the rest.
	if n := plan.StartNotes["roostack-vaultwarden-app.service"]; !strings.HasPrefix(n, "downloads docker.io/vaultwarden/server:") ||
		strings.Contains(n, "sha256") {
		t.Errorf("vaultwarden's note = %q", n)
	}
	if n := plan.StartNotes["roostack-caddy.service"]; !strings.Contains(n, "builds its image") {
		t.Errorf("caddy's note = %q", n)
	}
	if !slices.Contains(e.systemd.calls, "start roostack-caddy.service") || !slices.Contains(e.systemd.calls, "start roostack-vaultwarden-app.service") {
		t.Errorf("new services start one at a time: %v", e.systemd.calls)
	}
	// Caddy's image build starts in the background as soon as its file is
	// written, before anything waits for it.
	if i := slices.Index(e.systemd.calls, "start --no-block roostack-caddy-build.service"); i < 0 ||
		i > slices.Index(e.systemd.calls, "start roostack-caddy.service") {
		t.Errorf("the build should start in the background first: %v", e.systemd.calls)
	}
	if got := string(e.secrets.values[dns.SecretName("api_token")]); got != "dns-token" {
		t.Errorf("DNS secret = %q, want the user-provided value", got)
	}
	if v := e.secrets.values["roostack-vaultwarden-admin_token"]; len(v) < 32 {
		t.Errorf("generated secret too short: %q", v)
	}
	if _, err := os.Stat(filepath.Join(e.lab.Paths.QuadletDir(), "roostack-vaultwarden-app.container")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(e.lab.Paths.ServicesDir, "vaultwarden", "data")); err != nil {
		t.Error(err)
	}

	// Second apply with the same state: nothing to do.
	e.systemd.calls = nil
	plan = e.apply(t, e.desired(t, "vaultwarden"))
	if !plan.Empty() {
		t.Errorf("second apply should be empty, got:\n%s", plan)
	}
	if slices.Contains(e.systemd.calls, "daemon-reload") || slices.Contains(e.systemd.calls, "start --no-block roostack-caddy-build.service") {
		t.Errorf("unexpected daemon-reload or build on a no-op apply: %v", e.systemd.calls)
	}

	// Changing a setting restarts only the affected unit.
	d := e.desired(t)
	p, _ := pkgspec.Load(filepath.Join("..", "..", "docs", "examples", "vaultwarden.yaml"))
	out, err := generate.Service(e.lab, p, &instance.Instance{
		Package: "vaultwarden", Version: p.Version, Updates: "notify",
		Settings: map[string]any{"signups_allowed": true},
		Routes:   map[string]instance.RouteOverride{"web": {Port: 20001}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Services = []*generate.Output{out}
	plan = e.apply(t, d)
	if want := []string{"roostack-vaultwarden-app.service"}; !reflect.DeepEqual(plan.RestartUnits, want) {
		t.Errorf("restart = %v, want %v", plan.RestartUnits, want)
	}

	// Removing the service stops its units and removes its files, but keeps data.
	e.systemd.calls = nil
	plan = e.apply(t, e.desired(t))
	wantStop := []string{"roostack-vaultwarden-app.service", "roostack-vaultwarden-network.service"}
	if !reflect.DeepEqual(plan.StopUnits, wantStop) {
		t.Errorf("stop = %v, want %v", plan.StopUnits, wantStop)
	}
	if e.systemd.calls[0] != "stop "+strings.Join(wantStop, " ") {
		t.Errorf("units must be stopped before files are removed; calls: %v", e.systemd.calls)
	}
	if _, err := os.Stat(filepath.Join(e.lab.Paths.QuadletDir(), "roostack-vaultwarden-app.container")); !os.IsNotExist(err) {
		t.Error("quadlet file should be removed")
	}
	if _, err := os.Stat(filepath.Join(e.lab.Paths.CaddySitesDir(), "vaultwarden.caddy")); !os.IsNotExist(err) {
		t.Error("caddy site should be removed")
	}
	if _, err := os.Stat(filepath.Join(e.lab.Paths.ServicesDir, "vaultwarden", "data")); err != nil {
		t.Error("service data must never be deleted by reconcile")
	}
}

func TestLeavesUnownedFilesAlone(t *testing.T) {
	e := newEnv(t)
	dir := e.lab.Paths.QuadletDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, "personal.container")
	if err := os.WriteFile(mine, []byte("[Container]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.apply(t, e.desired(t))
	if _, err := os.Stat(mine); err != nil {
		t.Error("files not named roostack-* must not be touched")
	}
}

// Until the domain step gives Caddy its DNS credentials, Caddy waits and
// the rest of the lab is applied.
func TestMissingUserSecret(t *testing.T) {
	e := newEnv(t)
	userSecret := e.r.UserSecret
	e.r.UserSecret = nil
	ctx := context.Background()
	plan, err := e.r.Plan(ctx, e.desired(t))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Waiting, []string{"roostack-caddy.container"}) || slices.Contains(plan.StartUnits, "roostack-caddy.service") ||
		plan.ReloadCaddy || !strings.Contains(plan.String(), "Waiting for a secret") {
		t.Errorf("Caddy should wait: %+v", plan)
	}
	for _, s := range plan.Secrets {
		if s.Name == dns.SecretName("api_token") {
			t.Error("the DNS secret can't be created without a value")
		}
	}
	if err := e.r.Apply(ctx, plan); err != nil {
		t.Fatalf("the rest of the lab should apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.r.Paths.QuadletDir(), "roostack-caddy.container")); err == nil {
		t.Error("Caddy's unit must not be written yet")
	}
	// Once the secret is given, Caddy follows.
	e.r.UserSecret = userSecret
	plan, err = e.r.Plan(ctx, e.desired(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Waiting) != 0 || !slices.Contains(plan.StartUnits, "roostack-caddy.service") {
		t.Errorf("Caddy should start once its secret is given: %+v", plan)
	}
}
