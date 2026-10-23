package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/podman"
	"roostack.cloud/roostack/internal/systemd"
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
func (f *fakeSecrets) Read(_ context.Context, name string) ([]byte, error) {
	v, ok := f.values[name]
	if !ok {
		return nil, fmt.Errorf("no secret %s", name)
	}
	return v, nil
}

type env struct {
	lab     config.Lab
	r       *Reconciler
	systemd *fakeSystemd
	secrets *fakeSecrets
	pulled  []string // images downloaded
	events  []string // progress, as "<stage> <unit>[ done]"
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
		Pull: func(_ context.Context, image string, w io.Writer) error {
			e.pulled = append(e.pulled, image)
			fmt.Fprintln(w, "Copying blob sha256:0123")
			return nil
		},
		Progress: func(ev Event) {
			s := strings.TrimSpace(ev.Stage + " " + ev.Unit)
			if ev.Done {
				s += " done"
			}
			e.events = append(e.events, s)
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
		p, err := pkgspec.Load(pkgtest.File(name))
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

// An image with no build for this server's processor fails the apply with
// the Owner's sentence, not "downloading …" around Podman's (ARM64-PLAN.md 1.3).
func TestPullNoImageForArch(t *testing.T) {
	e := newEnv(t)
	e.apply(t, e.desired(t))
	e.r.Pull = func(_ context.Context, image string, _ io.Writer) error {
		return &podman.NoImageForArch{Image: image, Arch: "arm64"}
	}
	var failed error
	e.r.Progress = func(ev Event) {
		if ev.Err != nil {
			failed = ev.Err
		}
	}
	ctx := context.Background()
	plan, err := e.r.Plan(ctx, e.desired(t, "vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	err = e.r.Apply(ctx, plan)
	if err == nil || !strings.HasPrefix(err.Error(), "This service has no version for this server's kind of processor (arm64)") {
		t.Errorf("apply: %v", err)
	}
	if failed != err {
		t.Errorf("install step's error %v, want the apply's", failed)
	}
}

// A new container whose download failed (or Roostack restarted halfway)
// has its Quadlet file already: listed as unfinished, the next apply
// starts it as a new one again, downloading first.
func TestUnfinishedStartsAgain(t *testing.T) {
	e := newEnv(t)
	e.apply(t, e.desired(t))
	e.r.Pull = func(context.Context, string, io.Writer) error { return errors.New("connection reset by peer") }
	ctx := context.Background()
	plan, err := e.r.Plan(ctx, e.desired(t, "vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.r.Apply(ctx, plan); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("apply: %v", err)
	}
	e.r.Pull = func(_ context.Context, image string, _ io.Writer) error {
		e.pulled = append(e.pulled, image)
		return nil
	}
	e.pulled, e.events = nil, nil
	if plan := e.apply(t, e.desired(t, "vaultwarden")); !plan.Empty() || len(e.pulled) != 0 {
		t.Errorf("without being told, it's only started: %v", e.pulled)
	}
	e.r.Unfinished = map[string]bool{"roostack-vaultwarden-app.service": true}
	plan = e.apply(t, e.desired(t, "vaultwarden"))
	if plan.Empty() || len(plan.Write) != 0 || plan.Pulls["roostack-vaultwarden-app.service"] == "" || len(e.pulled) != 1 ||
		!slices.Contains(e.events, "download roostack-vaultwarden-app.service done") {
		t.Errorf("unfinished: plan\n%s\npulled %v, events %v", plan, e.pulled, e.events)
	}
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
	if n := plan.StartNotes["roostack-vaultwarden-app.service"]; !strings.HasPrefix(n, "downloading docker.io/vaultwarden/server:") ||
		strings.Contains(n, "sha256") {
		t.Errorf("vaultwarden's note = %q", n)
	}
	if n := plan.StartNotes["roostack-caddy.service"]; !strings.Contains(n, "building its image") {
		t.Errorf("caddy's note = %q", n)
	}
	// Caddy's image build starts in the background as soon as its file is
	// written; new services download and start one at a time; a new Caddy
	// starts last, since its first start waits for that build.
	if want := []string{"daemon-reload", "start --no-block roostack-caddy-build.service",
		"start roostack-vaultwarden-app.service", "reload-or-restart roostack-caddy.service"}; !slices.Equal(e.systemd.calls, want) {
		t.Errorf("calls %v, want %v", e.systemd.calls, want)
	}
	if want := []string{"docker.io/vaultwarden/server"}; len(e.pulled) != 1 || !strings.HasPrefix(e.pulled[0], want[0]) {
		t.Errorf("pulled %v", e.pulled)
	}
	if want := []string{"download roostack-vaultwarden-app.service", "download roostack-vaultwarden-app.service done",
		"start roostack-vaultwarden-app.service", "start roostack-vaultwarden-app.service done", "proxy", "proxy done"}; !slices.Equal(e.events, want) {
		t.Errorf("events %v", e.events)
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
	p, _ := pkgspec.Load(pkgtest.File("vaultwarden"))
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

// TestSystemFileRestarts: CrowdSec reads its files only when it starts, so
// a changed one (a new Hub commit) restarts it; a first start doesn't.
func TestSystemFileRestarts(t *testing.T) {
	e := newEnv(t)
	sys, err := generate.System(e.lab, true)
	if err != nil {
		t.Fatal(err)
	}
	if plan := e.apply(t, Desired{System: sys}); slices.Contains(plan.RestartUnits, "roostack-crowdsec.service") {
		t.Errorf("first start restarts CrowdSec:\n%s", plan)
	}
	local := filepath.Join(e.lab.Paths.CrowdSecDir(), "config.yaml.local")
	os.WriteFile(local, []byte("an older commit"), 0o644)
	plan := e.apply(t, Desired{System: sys})
	if !slices.Equal(plan.RestartUnits, []string{"roostack-crowdsec.service"}) {
		t.Errorf("a changed file doesn't restart CrowdSec:\n%s", plan)
	}
	if b, _ := os.ReadFile(local); !strings.Contains(string(b), generate.CrowdSecHub) {
		t.Errorf("not rewritten: %s", b)
	}
	if plan := e.apply(t, Desired{System: sys}); len(plan.RestartUnits) != 0 {
		t.Errorf("nothing changed:\n%s", plan)
	}
}

// A database URL secret is made from the database's password: one made
// in the same apply, or (made again, say after it was lost) the one
// Podman already has (SPEC.md §8.1).
func TestDatabaseURLSecret(t *testing.T) {
	e := newEnv(t)
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: app
title: App
version: "1"
databases:
  main: { type: postgres }
containers:
  web:
    image: example/app:1
    secrets: { DATABASE_URL: databases.main.url }
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := generate.Service(e.lab, p, &instance.Instance{Package: "app", Version: "1", Updates: "notify"})
	if err != nil {
		t.Fatal(err)
	}
	sys, err := generate.System(e.lab, false)
	if err != nil {
		t.Fatal(err)
	}
	d := Desired{System: sys, Services: []*generate.Output{out}}
	plan := e.apply(t, d)
	if s := plan.String(); !strings.Contains(s, "roostack-app-db-main-url (made from roostack-app-db-main-password)") {
		t.Errorf("plan:\n%s", s)
	}
	pw := string(e.secrets.values["roostack-app-db-main-password"])
	want := "postgresql://app:" + pw + "@roostack-app-db-main:5432/app"
	if pw == "" || string(e.secrets.values["roostack-app-db-main-url"]) != want {
		t.Fatalf("URL = %q, want %q", e.secrets.values["roostack-app-db-main-url"], want)
	}
	delete(e.secrets.values, "roostack-app-db-main-url")
	e.apply(t, d)
	if got := string(e.secrets.values["roostack-app-db-main-url"]); got != want {
		t.Errorf("made again from the existing password: %q", got)
	}
}

// Single sign-on's secret env is made from the client secret put into
// its template, made in the same apply or already in Podman (SPEC.md
// §5.2, "Secrets inside JSON").
func TestSecretFromTemplate(t *testing.T) {
	e := newEnv(t)
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: docs
title: Docs
version: "1"
containers:
  web: { image: example/docs:1 }
routes:
  web: { container: web, port: 8000, subdomain: docs }
sso:
  oidc: { route: web, redirect_paths: [/cb] }
  secret_env:
    web: { PROVIDERS: '{"secret": {{ json .SSO.ClientSecret }}}' }
`))
	if err != nil {
		t.Fatal(err)
	}
	e.lab.Domain = "lab.example.com"
	in := &instance.Instance{Package: "docs", Version: "1", Updates: "notify", SSO: true,
		Routes: map[string]instance.RouteOverride{"web": {Port: 20050}}}
	out, err := generate.Service(e.lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	sys, err := generate.System(e.lab, false)
	if err != nil {
		t.Fatal(err)
	}
	d := Desired{System: sys, Services: []*generate.Output{out}}
	plan := e.apply(t, d)
	var name string
	for _, s := range out.Secrets {
		if s.Template != nil {
			name = s.Name
		}
	}
	if s := plan.String(); name == "" || !strings.Contains(s, name+" (made from "+generate.SSOSecretName("docs")+")") {
		t.Errorf("plan:\n%s", s)
	}
	secret := string(e.secrets.values[generate.SSOSecretName("docs")])
	want := `{"secret": "` + secret + `"}`
	if secret == "" || string(e.secrets.values[name]) != want {
		t.Fatalf("value = %q, want %q", e.secrets.values[name], want)
	}
	delete(e.secrets.values, name)
	e.apply(t, d)
	if got := string(e.secrets.values[name]); got != want {
		t.Errorf("made again from the existing secret: %q", got)
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

// flakySystemd fails to start units, as at boot before the network is up,
// then reports them as it's told (systemd restarting them).
type flakySystemd struct {
	fakeSystemd
	state string
}

func (f *flakySystemd) Start(_ context.Context, u ...string) error {
	f.record("start", u)
	return errors.New("Job for roostack-crowdsec.service failed")
}
func (f *flakySystemd) Status(_ context.Context, units ...string) ([]systemd.UnitStatus, error) {
	var out []systemd.UnitStatus
	for _, u := range units {
		out = append(out, systemd.UnitStatus{Unit: u, ActiveState: f.state})
	}
	return out, nil
}

// A failed start that systemd's restart recovers from isn't a failure; one
// that stays down is (docs/ASSUMPTIONS.md C11).
func TestStartSettles(t *testing.T) {
	for _, c := range []struct {
		state   string
		wantErr bool
	}{{"active", false}, {"failed", true}} {
		f := &flakySystemd{state: c.state}
		r := &Reconciler{Systemd: f, Settle: 3 * time.Second}
		err := r.start(context.Background(), nil, "roostack-crowdsec.service")
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err %v", c.state, err)
		}
	}
}
