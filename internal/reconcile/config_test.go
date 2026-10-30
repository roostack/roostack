package reconcile

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// configOutput generates a service with config files: the test package
// (a whole YAML file) or a package with a block.
func (e *env) configOutput(t *testing.T, p *pkgspec.Package, in *instance.Instance) Desired {
	t.Helper()
	sys, err := generate.System(e.lab, false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := generate.Service(e.lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	return Desired{System: sys, Services: []*generate.Output{out}}
}

func pingvin(t *testing.T, days int) (*pkgspec.Package, *instance.Instance) {
	t.Helper()
	p, err := pkgspec.Load(pkgtest.File("pingvin-share-x"))
	if err != nil {
		t.Fatal(err)
	}
	return p, &instance.Instance{Package: p.Name, Version: p.Version, Updates: "notify", SSO: true,
		Settings: map[string]any{"admin_email": "me@example.com", "max_share_days": days},
		Routes:   map[string]instance.RouteOverride{"web": {Port: 20001}}}
}

// A whole config file is written before its app starts, with its secrets
// filled in (one made in the same apply), only Roostack's user able to
// read it; written again, restarting the app, when a setting changes;
// and without an install-only secret once it's deleted.
func TestConfigFileWhole(t *testing.T) {
	e := newEnv(t)
	password := secretOf("pingvin-share-x", "admin_password")
	e.r.UserSecret = func(name string) ([]byte, bool) { return []byte(`pa"ss`), name == password }
	p, in := pingvin(t, 30)
	plan := e.apply(t, e.configOutput(t, p, in))
	if len(plan.Configs) != 1 || slices.Contains(plan.RestartUnits, "roostack-pingvin-share-x-app.service") {
		t.Fatalf("plan: %+v", plan)
	}
	if strings.Contains(plan.String(), `pa"ss`) {
		t.Errorf("the plan shows a secret:\n%s", plan)
	}
	path := filepath.Join(e.lab.Paths.ServicesDir, "pingvin-share-x", "config", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	client := string(e.secrets.values[generate.SSOSecretName("pingvin-share-x")])
	for _, want := range []string{`password: "pa\"ss"`, `oidc-clientSecret: "` + client + `"`, `maxExpiration: "30 days"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("file lacks %s:\n%s", want, data)
		}
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}

	// Nothing changed: nothing to write.
	if plan, err := e.r.Plan(context.Background(), e.configOutput(t, p, in)); err != nil || !plan.Empty() || len(plan.Configs) != 0 {
		t.Errorf("again: %+v %v", plan, err)
	}

	// A setting changed: written again, and the app restarted.
	p, in = pingvin(t, 7)
	e.systemd.calls = nil
	plan = e.apply(t, e.configOutput(t, p, in))
	if len(plan.Configs) != 1 || !slices.Contains(e.systemd.calls, "restart roostack-pingvin-share-x-app.service") {
		t.Errorf("after a change: %+v, calls %v", plan, e.systemd.calls)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), `maxExpiration: "7 days"`) {
		t.Errorf("after a change:\n%s", data)
	}

	// Setup finished, so the install-only password is gone: so is its copy.
	delete(e.secrets.values, password)
	e.r.UserSecret = nil
	e.apply(t, e.configOutput(t, p, in))
	if data, _ := os.ReadFile(path); strings.Contains(string(data), `pa\"ss`) || !strings.Contains(string(data), `password: ""`) {
		t.Errorf("after setup:\n%s", data)
	}
}

// A symlink the app puts where its file goes can't lead the write out of
// its storage: a symlinked file is replaced, a symlinked folder is left
// alone, and the rest of the apply goes ahead.
func TestConfigFileSymlink(t *testing.T) {
	e := newEnv(t)
	p, in := pingvin(t, 30)
	e.apply(t, e.configOutput(t, p, in))
	dir := filepath.Join(e.lab.Paths.ServicesDir, "pingvin-share-x", "config")
	outside := filepath.Join(t.TempDir(), "quadlet.container")
	os.WriteFile(outside, []byte("mine"), 0o600)

	os.Remove(filepath.Join(dir, "config.yaml"))
	if err := os.Symlink(outside, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	plan := e.apply(t, e.configOutput(t, p, in))
	if b, _ := os.ReadFile(outside); string(b) != "mine" || len(plan.ConfigsLeft) != 1 {
		t.Errorf("outside: %q, left %v", b, plan.ConfigsLeft)
	}

	// Written through the root: a symlink inside it is replaced, not followed.
	if err := writeInRoot(dir, "config.yaml", []byte("x"), 0o600); err == nil {
		if b, _ := os.ReadFile(outside); string(b) != "mine" {
			t.Errorf("written through the symlink: %q", b)
		}
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(dir, "conf")); err != nil {
		t.Fatal(err)
	}
	if err := writeInRoot(dir, "conf/quadlet.container", []byte("x"), 0o600); err == nil {
		t.Error("wrote through a symlinked folder")
	}
	if b, _ := os.ReadFile(outside); string(b) != "mine" {
		t.Errorf("outside: %q", b)
	}
}

// A block waits for its app to make the file, goes at its end once it
// has, restarting the app, and leaves the app's own lines and the file's
// permissions as they are.
func TestConfigFileBlock(t *testing.T) {
	e := newEnv(t)
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: files
title: Files
version: 1.0.0-roostack.1
settings:
  - { key: days, title: Days, type: int, default: 7 }
  - { key: key, title: Key, type: secret, generate: true }
containers:
  app:
    image: example.com/files:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
    mounts: [{ storage: data, path: /shared }]
storage:
  data: { class: critical }
config_files:
  web:
    title: Its web settings
    storage: data
    path: seafile/conf/seahub_settings.py
    format: python
    block: true
    content: |
      DAYS = {{ .Settings.days }}
      KEY = {{ .Secrets.key }}
`))
	if err != nil {
		t.Fatal(err)
	}
	in := &instance.Instance{Package: "files", Version: p.Version, Updates: "notify"}
	plan := e.apply(t, e.configOutput(t, p, in))
	if len(plan.ConfigsWaiting) != 1 || len(plan.Configs) != 0 {
		t.Fatalf("before the app has made it: %+v", plan)
	}
	f := plan.ConfigsWaiting[0]
	if ConfigThere(f) || HasBlock(f) {
		t.Error("there before the app made it")
	}

	// The app makes it on its first start.
	path := f.HostPath()
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("SECRET_KEY = 'k'\nTIME_ZONE = 'UTC'"), 0o644)
	e.systemd.calls = nil
	plan = e.apply(t, e.configOutput(t, p, in))
	if len(plan.Configs) != 1 || !slices.Contains(e.systemd.calls, "restart roostack-files-app.service") {
		t.Errorf("once it's there: %+v, calls %v", plan, e.systemd.calls)
	}
	data, _ := os.ReadFile(path)
	key := string(e.secrets.values[secretOf("files", "key")])
	want := "SECRET_KEY = 'k'\nTIME_ZONE = 'UTC'\n\n# BEGIN Roostack: " +
		"Roostack writes the lines down to END Roostack from the package and the service's settings, and will replace any change made to them.\n" +
		"DAYS = 7\nKEY = \"" + key + "\"\n# END Roostack\n"
	if string(data) != want {
		t.Errorf("file =\n%s\nwant\n%s", data, want)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o644 || !HasBlock(f) {
		t.Errorf("mode %v, has block %v", fi.Mode(), HasBlock(f))
	}

	// A setting changed: only the block changes.
	in.Settings = map[string]any{"days": 3}
	os.WriteFile(path, append(data, "EXTRA = 1\n"...), 0o644)
	e.apply(t, e.configOutput(t, p, in))
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "DAYS = 3\n") || strings.Contains(string(data), "DAYS = 7") ||
		!strings.HasSuffix(string(data), "# END Roostack\nEXTRA = 1\n") {
		t.Errorf("after a change:\n%s", data)
	}

	// Its last line gone: left alone, and said why.
	cut, _ := os.ReadFile(path)
	broken := strings.Replace(string(cut), "# END Roostack\n", "", 1)
	os.WriteFile(path, []byte(broken), 0o644)
	plan = e.apply(t, e.configOutput(t, p, in))
	if data, _ := os.ReadFile(path); string(data) != broken || len(plan.ConfigsLeft) != 1 || !strings.Contains(plan.String(), "lost their last line") {
		t.Errorf("without its end: %+v\n%s", plan.ConfigsLeft, data)
	}
}

// secretOf is a secret setting's Podman name (node.SecretName).
func secretOf(svc, key string) string { return "roostack-" + svc + "-" + key }
