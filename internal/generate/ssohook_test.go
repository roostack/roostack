package generate

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// Forgejo's redirect path names its sign-in source, the lab's name:
// rendered and escaped as Forgejo escapes it (SPEC.md §5.2).
func TestSSORedirectsTemplated(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "forgejo-sso.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lab := testLab()
	lab.Name = "Smith family"
	got, err := SSORedirects(lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"https://git.lab.example.com/user/oauth2/Smith%20family/callback"}; !slices.Equal(got, want) {
		t.Errorf("redirects %q", got)
	}
}

// An sso hook is told on or off, gets the client secret only while it's
// on, and its fingerprint follows what it's given (the lab's address),
// never a secret's value.
func TestSSOHook(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "forgejo-sso.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	h := p.HooksOn(pkgspec.HookSSO)[0]
	lab := testLab()
	on, err := Hook(lab, p, in, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	if on.Env["ROOSTACK_SSO"] != "on" || on.Secrets["CLIENT_SECRET"] != SSOSecretName("forgejo") ||
		on.Env["DISCOVERY_URL"] != "https://login.lab.example.com/.well-known/openid-configuration" {
		t.Errorf("on: env %v, secrets %v", on.Env, on.Secrets)
	}
	in.SSO = false
	off, err := Hook(lab, p, in, h, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := off.Env["CLIENT_SECRET"]; off.Env["ROOSTACK_SSO"] != "off" || !ok || v != "" || len(off.Secrets) != 0 {
		t.Errorf("off: env %v, secrets %v", off.Env, off.Secrets)
	}
	if on.Fingerprint() == off.Fingerprint() {
		t.Error("on and off have the same fingerprint")
	}
	in.SSO = true
	lab.Domain = "elsewhere.example.com"
	moved, _ := Hook(lab, p, in, h, nil)
	if moved.Fingerprint() == on.Fingerprint() {
		t.Error("a new address has the same fingerprint")
	}
}

const jsonSecretPackage = `
schema: roostack/v1alpha1
name: docs
title: Docs
version: 1.0.0-roostack.1
containers:
  app:
    image: example.org/docs:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
    env: { PLAIN: "1" }
routes:
  web: { container: app, port: 8000, subdomain: docs, exposure: private }
sso:
  oidc:
    route: web
    redirect_paths: [/accounts/oidc/roostack/login/callback/]
  env:
    app: { DOCS_APPS: oidc }
  secret_env:
    app:
      DOCS_PROVIDERS: >-
        {"name": {{ json .SSO.ProviderName }}, "client_id": {{ json .SSO.ClientID }},
        "secret": {{ json .SSO.ClientSecret }}, "server_url": {{ json .SSO.DiscoveryURL }}}
`

// A secret inside JSON (Paperless-ngx's providers): its value is made as a
// Podman secret from the client secret, passed as env, and never in a
// file Roostack writes; its name changes with what's around the secret.
func TestSSOSecretEnv(t *testing.T) {
	p, err := pkgspec.Parse([]byte(jsonSecretPackage))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := Check(p); err != nil {
		t.Fatal(err)
	}
	lab := testLab()
	lab.Name = `The "Smith" family`
	in := &instance.Instance{Package: "docs", Version: p.Version, SSO: true, Updates: instance.UpdatesNotify,
		Routes: map[string]instance.RouteOverride{"web": {Port: 20030}}}
	out, err := Service(lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	var made *Secret
	for i, s := range out.Secrets {
		if strings.HasPrefix(s.Name, SSOSecretEnvPrefix("docs")+"docs-providers-") {
			made = &out.Secrets[i]
		}
	}
	if made == nil || made.Template == nil || made.Template.From != SSOSecretName("docs") || !made.Derived() {
		t.Fatalf("secrets %+v", out.Secrets)
	}
	if got := made.Template.Value("s3cr3t-Value_x"); got != `{"name": "The \"Smith\" family", "client_id": "docs", "secret": "s3cr3t-Value_x", "server_url": "https://login.lab.example.com/.well-known/openid-configuration"}` {
		t.Errorf("value %s", got)
	}
	for _, f := range out.Quadlets {
		if strings.Contains(f.Content, SSOSecretMarker) || strings.Contains(f.Content, "server_url") {
			t.Errorf("%s holds the value:\n%s", f.Name, f.Content)
		}
		if f.Name == "roostack-docs-app.container" && !strings.Contains(f.Content, "Secret="+made.Name+",type=env,target=DOCS_PROVIDERS") {
			t.Errorf("no secret env in\n%s", f.Content)
		}
	}
	lab.Domain = "elsewhere.example.com"
	out2, _ := Service(lab, p, in)
	if slices.ContainsFunc(out2.Secrets, func(s Secret) bool { return s.Name == made.Name }) {
		t.Error("a new address kept the same secret name")
	}

	// .SSO.ClientSecret anywhere else is refused.
	bad, _ := pkgspec.Parse([]byte(strings.Replace(jsonSecretPackage, `DOCS_APPS: oidc`, `DOCS_APPS: "{{ .SSO.ClientSecret }}"`, 1)))
	if err := Check(bad); err == nil || !strings.Contains(err.Error(), "only for sso.secret_env") {
		t.Errorf("client secret in env: %v", err)
	}
}

// Off, an app its sso hook set keeps reaching the sign-in page, so a
// button it keeps leads there (to "single sign-on is off"), not to an
// error; an app set by env doesn't.
func TestSSOReachesWhileOff(t *testing.T) {
	for _, tc := range []struct {
		pkg   string
		reach bool
	}{{"forgejo", true}, {"mealie", false}} {
		p, err := pkgspec.Load(pkgtest.File(tc.pkg))
		if err != nil {
			t.Fatal(err)
		}
		in := &instance.Instance{Package: tc.pkg, Version: p.Version, Updates: instance.UpdatesNotify,
			Routes: map[string]instance.RouteOverride{"web": {Port: 20060}}}
		out, err := Service(testLab(), p, in)
		if err != nil {
			t.Fatal(err)
		}
		hosts := false
		for _, f := range out.Quadlets {
			hosts = hosts || strings.Contains(f.Content, "AddHost=login.lab.example.com:host-gateway")
			if strings.Contains(f.Content, "oauth2_client") || strings.Contains(f.Content, "OIDC_") {
				t.Errorf("%s: single sign-on's settings while it's off:\n%s", tc.pkg, f.Content)
			}
		}
		if out.SSOReach != tc.reach || hosts != tc.reach || out.SSO {
			t.Errorf("%s: reach %v, AddHost %v, on %v", tc.pkg, out.SSOReach, hosts, out.SSO)
		}
	}
}
