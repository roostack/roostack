package pkgspec

import (
	"strings"
	"testing"
)

const ssoBase = `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: example/app:1 }
routes:
  web: { container: web, port: 80, subdomain: app }
`

// What the sso part refuses, and what it accepts.
func TestSSOValidation(t *testing.T) {
	for _, tc := range []struct{ name, sso, want string }{
		{"neither", "sso: { note: x }", "needs oidc or forward_auth"},
		{"both", "sso: { oidc: { route: web, redirect_paths: [/cb] }, forward_auth: { route: web } }", "not both"},
		{"unknown route", "sso: { oidc: { route: api, redirect_paths: [/cb] } }", `unknown route "api"`},
		{"no redirects", "sso: { oidc: { route: web } }", "needs redirect_paths"},
		{"relative path", "sso: { oidc: { route: web, redirect_paths: [cb] } }", "must be a path"},
		{"double slash", "sso: { oidc: { route: web, redirect_paths: [//evil.example/cb] } }", "must be a path"},
		{"a web address", "sso: { oidc: { route: web, redirect_uris: ['https://evil.example/cb'] } }", "not a web address"},
		{"bypass reason", "sso: { forward_auth: { route: web, bypass_paths: [{ path: /api }] } }", "say why"},
		{"bypass path", "sso: { forward_auth: { route: web, bypass_paths: [{ path: '/api/*', reason: r }] } }", "must start with /"},
		{"env container", "sso: { oidc: { route: web, redirect_paths: [/cb] }, env: { db: { X: y } } }", "sso.env.db: unknown container"},
		{"another secret", "sso: { oidc: { route: web, redirect_paths: [/cb] }, secrets: { web: { X: admin_token } } }", "must be sso.client_secret"},
		{"a wall's secret", "sso: { forward_auth: { route: web }, secrets: { web: { X: sso.client_secret } } }", "only an oidc app"},
	} {
		p, err := Parse([]byte(ssoBase + tc.sso))
		if err == nil {
			err = p.Validate()
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
	}

	p, err := Parse([]byte(ssoBase + `
sso:
  oidc:
    route: web
    redirect_paths: [/login, /accounts/oidc/lab/login/callback/]
    redirect_uris: ['app.example:///oauth-callback']
  env:
    web: { OIDC_ENABLED: "true" }
  secrets:
    web: { OIDC_SECRET: sso.client_secret }
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.SSORoute() != "web" {
		t.Errorf("route %q", p.SSORoute())
	}
	got := p.SSO.OIDC.SSORedirects("https://app.lab.example")
	if len(got) != 3 || got[0] != "https://app.lab.example/login" || got[2] != "app.example:///oauth-callback" {
		t.Errorf("redirects %v", got)
	}
}
