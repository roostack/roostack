package generate

import (
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// configPackage is a package with a Seafile-like block and a whole file.
func configPackage(t *testing.T) *pkgspec.Package {
	t.Helper()
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: files
title: Files
version: 1.0.0-roostack.1
settings:
  - { key: days, title: Days, type: int, default: 7 }
  - { key: token, title: API token, type: secret, generate: true }
  - { key: first_password, title: First password, type: secret, install_only: true }
containers:
  app:
    image: example.com/files:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
    mounts: [{ storage: data, path: /shared }]
  worker:
    image: example.com/files:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
    mounts: [{ storage: data, path: /shared, readonly: true }]
  other:
    image: example.com/files:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
storage:
  data: { class: critical }
config_files:
  web:
    title: Its web settings
    storage: data
    path: conf/settings.py
    format: python
    block: true
    content: |
      DAYS = {{ .Settings.days }}
      TOKEN = {{ .Secrets.token }}
      {{- if .SSO.Enabled }}
      OAUTH_SECRET = {{ .SSO.ClientSecret }}
      {{- end }}
  json:
    title: Its JSON
    storage: data
    path: app.json
    format: json
    content: |
      {"first": {{ .Secrets.first_password }}, "days": {{ .Settings.days }}}
hooks:
  - { on: install, container: app, command: ["true"] }
routes:
  web: { container: app, port: 80, subdomain: files, exposure: private }
sso:
  oidc: { route: web, redirect_paths: [/oauth/callback] }
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigFiles(t *testing.T) {
	p := configPackage(t)
	in := ExampleInstance(p)
	in.SSO = true
	out, err := Service(testLab(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.ConfigFiles) != 2 {
		t.Fatalf("config files = %+v", out.ConfigFiles)
	}
	j, py := out.ConfigFiles[0], out.ConfigFiles[1]
	if py.Name != "web" || py.HostPath() != "/srv/roostack/services/files/data/conf/settings.py" || !py.Block {
		t.Errorf("block file = %+v", py)
	}
	// Generation never has a secret: placeholders, named by Podman secret.
	want := "# BEGIN Roostack: Roostack writes the lines down to END Roostack from the package and the service's settings, and will replace any change made to them.\n" +
		"DAYS = 7\nTOKEN = " + SecretPlaceholder("roostack-files-token") + "\n" +
		"OAUTH_SECRET = " + SecretPlaceholder(SSOSecretName("files")) + "\n# END Roostack\n"
	if py.Content != want {
		t.Errorf("content =\n%q\nwant\n%q", py.Content, want)
	}
	if got := py.Shown(); !strings.Contains(got, "TOKEN = ‹secret: API token›") || !strings.Contains(got, "‹secret: Single sign-on's client secret›") {
		t.Errorf("shown =\n%s", got)
	}
	// Restarted with it: the containers mounting its storage.
	if got := strings.Join(py.Restarts, " "); got != "roostack-files-app.container roostack-files-worker.container" {
		t.Errorf("restarts = %s", got)
	}
	// Filled in as JSON writes a string, which Python reads too.
	filled := py.Fill(map[string]string{"roostack-files-token": `a"b\c`, SSOSecretName("files"): "s3"})
	if !strings.Contains(filled, `TOKEN = "a\"b\\c"`) || !strings.Contains(filled, `OAUTH_SECRET = "s3"`) {
		t.Errorf("filled =\n%s", filled)
	}
	// JSON has no comments, so no header; an install-only secret deleted
	// since is empty.
	if !j.Secrets[0].InstallOnly || j.Fill(nil) != `{"first": "", "days": 7}`+"\n" {
		t.Errorf("json = %+v, filled %q", j, j.Fill(nil))
	}
	if err := checkConfigFile(j); err != nil {
		t.Error(err)
	}
	// With single sign-on off, no client secret.
	in.SSO = false
	out, err = Service(testLab(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.ConfigFiles[1].Content, "OAUTH") || len(out.ConfigFiles[1].Secrets) != 1 {
		t.Errorf("without single sign-on: %+v", out.ConfigFiles[1])
	}
	// Stopped, or waiting for its data: no app reads them.
	Stop(out, p, true)
	if len(out.ConfigFiles) != 0 {
		t.Errorf("stopped: %+v", out.ConfigFiles)
	}
}

func TestConfigFileTemplates(t *testing.T) {
	for _, tc := range []struct{ content, want string }{
		// Secrets only through .Secrets: settings never hold them.
		{"X = {{ .Settings.token }}", `map has no entry for key "token"`},
		{"X = {{ .Secrets.nope }}", `map has no entry for key "nope"`},
		// The client secret only while single sign-on is on.
		{"X = {{ .SSO.ClientSecret }}", "only while single sign-on is on"},
	} {
		p := configPackage(t)
		p.ConfigFiles["web"].Content = tc.content
		_, err := Service(testLab(), p, ExampleInstance(p))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.content, err, tc.want)
		}
	}
	// Anywhere else, .Secrets isn't there.
	p := configPackage(t)
	p.Containers["app"].Env = map[string]string{"X": "{{ .Secrets.token }}"}
	if _, err := Service(testLab(), p, ExampleInstance(p)); err == nil {
		t.Error("a container's env read .Secrets")
	}
	// A YAML file the template breaks fails the package's check.
	p = configPackage(t)
	p.ConfigFiles["json"].Format = pkgspec.FormatYAML
	p.ConfigFiles["json"].Content = "a: [{{ .Settings.days }}\n"
	if err := Check(p); err == nil || !strings.Contains(err.Error(), "isn't valid YAML") {
		t.Errorf("check = %v", err)
	}
}

func TestSetBlock(t *testing.T) {
	f := ConfigFile{Format: pkgspec.FormatPython}
	block := "# BEGIN Roostack: x\nA = 1\n# END Roostack\n"
	for _, tc := range []struct{ have, want string }{
		{"", block},
		{"SECRET_KEY = 'k'\n", "SECRET_KEY = 'k'\n\n" + block},
		{"SECRET_KEY = 'k'", "SECRET_KEY = 'k'\n\n" + block},
		// Replaced where it is, the app's lines after it kept.
		{"K = 1\n\n# BEGIN Roostack: old\nA = 0\nB = 2\n# END Roostack\nTIME_ZONE = 'UTC'\n", "K = 1\n\n" + block + "TIME_ZONE = 'UTC'\n"},
		{"K = 1\n# BEGIN Roostack: old\n# END Roostack", "K = 1\n" + block},
	} {
		got, ok := f.SetBlock(tc.have, block)
		if !ok || got != tc.want {
			t.Errorf("SetBlock(%q) = %q, %v; want %q", tc.have, got, ok, tc.want)
		}
	}
	// A block that has lost its end is left alone.
	if got, ok := f.SetBlock("# BEGIN Roostack\nA = 0\n", block); ok || got != "# BEGIN Roostack\nA = 0\n" {
		t.Errorf("without its end: %q, %v", got, ok)
	}
}

// The test package's config file, as the golden file shows it with
// single sign-on on, holds no secret, only placeholders.
func TestConfigFileFixture(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("pingvin-share-x"))
	if err != nil {
		t.Fatal(err)
	}
	in := ExampleInstance(p)
	in.SSO = true
	in.Settings = map[string]any{"admin_email": "me@example.com"}
	out, err := Service(testLab(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	f := out.ConfigFiles[0]
	if len(f.Secrets) != 2 || !strings.Contains(f.Shown(), "oidc-clientSecret: ‹secret: Single sign-on's client secret›") {
		t.Errorf("%+v\n%s", f.Secrets, f.Shown())
	}
	if err := checkConfigFile(f); err != nil {
		t.Error(err)
	}
}
