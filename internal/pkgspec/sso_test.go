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
		{"existing accounts", "sso: { oidc: { route: web, redirect_paths: [/cb], existing_accounts: by_email } }", `"by_email" must be one of email, username`},
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
    admins: true
    existing_accounts: username_or_email
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
	if o := p.SSO.OIDC; !o.Admins || o.ExistingAccounts != SSOExistingUsernameOrEmail {
		t.Errorf("admins %v, existing accounts %q", o.Admins, o.ExistingAccounts)
	}
	if p.SSORoute() != "web" {
		t.Errorf("route %q", p.SSORoute())
	}
	for path, want := range map[string]bool{"/login": true, "/user/oauth2/Smith%20family/callback": true,
		"/a/": true, "//x": false, "/a b": false, "/%2g": false, "login": false} {
		if ValidRedirectPath(path) != want {
			t.Errorf("ValidRedirectPath(%q) = %v", path, !want)
		}
	}
}
