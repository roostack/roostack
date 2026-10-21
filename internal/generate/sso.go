package generate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
)

// Single sign-on (SPEC.md §5.2): what turning it on changes in a
// service's files. An OIDC app gets the package's sso env, its client
// secret, and the sign-in host in its hosts file; a walled app's route goes
// behind forward auth.

// SSOContext is single sign-on's values in package templates, .SSO: for
// an app's own settings, e.g. OIDC_CONFIGURATION_URL: "{{ .SSO.DiscoveryURL }}".
type SSOContext struct {
	// Enabled: single sign-on is on for the service.
	Enabled bool
	// Issuer is the sign-in host's address, https://login.<domain>;
	// DiscoveryURL its OIDC configuration, and the rest its endpoints, for
	// apps that don't read the configuration (HedgeDoc).
	Issuer, DiscoveryURL, AuthorizeURL, TokenURL, UserinfoURL string
	// ClientID is the app's ID at the sign-in page: the service's name.
	ClientID string
	// ProviderName is the lab's name, for an app's "Sign in with …"
	// button that adds its own words; ButtonText is the whole of it.
	ProviderName, ButtonText string
	// UserGroup is the group everyone with access is in, AdminGroup the
	// one the lab's Owners and Admins are in too (the "groups" claim),
	// under the names the package gives them (sso.oidc.groups).
	UserGroup, AdminGroup string

	// secretMarker is set while rendering sso.secret_env, the one place
	// .SSO.ClientSecret is allowed.
	secretMarker bool
}

// SSOSecretMarker stands for the client secret in a rendered secret_env
// value, until the reconciler puts the secret in its place: generation
// never has the secret. Plain letters, so json leaves it as it is.
const SSOSecretMarker = "ROOSTACKSSOCLIENTSECRETxq7Zp2"

// ClientSecret is the app's client secret, in sso.secret_env only:
// anywhere else it would end up in a file Roostack writes.
func (c SSOContext) ClientSecret() (string, error) {
	if !c.secretMarker {
		return "", fmt.Errorf(".SSO.ClientSecret is only for sso.secret_env (or an sso hook's secrets: sso.client_secret)")
	}
	return SSOSecretMarker, nil
}

// Sign-in paths and groups: internal/sso's, which tests they match.
const (
	SSOPathDiscovery = "/.well-known/openid-configuration"
	SSOPathAuthorize = "/authorize"
	SSOPathToken     = "/token"
	SSOPathUserInfo  = "/userinfo"
	SSOUserGroup     = "users"
	SSOAdminGroup    = "admins"
)

// SSOOn reports whether single sign-on is on for a service: the Owner
// turned it on, its package takes it, and the lab has an address for the
// sign-in page. Only the controller runs it for now.
func SSOOn(lab config.Lab, p *pkgspec.Package, in *instance.Instance) bool {
	return in.SSO && p.SSO != nil && lab.HasAddress() && !lab.IsNode()
}

// SSOReaches reports whether an app's containers reach the sign-in page:
// while single sign-on is on, and for an OIDC app set by its sso hook as
// long as the lab has an address, on or off. Such an app keeps its sign-in
// settings in its own database, and may keep its button after single
// sign-on is turned off (Forgejo won't delete a sign-in source accounts
// were made through); the button then leads to the sign-in page, which
// says single sign-on is off, rather than to an error (SPEC.md §5.2).
func SSOReaches(lab config.Lab, p *pkgspec.Package, in *instance.Instance) bool {
	if SSOOn(lab, p, in) {
		return p.SSO.OIDC != nil
	}
	return p.SSO != nil && p.SSO.OIDC != nil && len(p.HooksOn(pkgspec.HookSSO)) > 0 && lab.HasAddress() && !lab.IsNode()
}

// ContainerHostGateway is the server as its rootless containers see it
// (Podman's host-gateway, pasta's --map-guest-addr), and where their
// connections to its Caddy come from, as Caddy sees them
// (docs/ASSUMPTIONS.md V1).
const ContainerHostGateway = "169.254.1.2"

// SSOSecretName is the Podman secret holding an app's client secret,
// which the sign-in page checks the app's requests against.
func SSOSecretName(service string) string { return "roostack-" + service + "-sso-client-secret" }

// SSOSecretEnvPrefix starts the names of a service's secret_env secrets.
// Each name ends in a hash of its value without the secret, so a change
// (the lab's address) gives a new name, the Quadlet changes and the app
// restarts with it; the server removes the ones no longer used.
func SSOSecretEnvPrefix(service string) string { return "roostack-" + service + "-sso-env-" }

func ssoContext(lab config.Lab, p *pkgspec.Package, in *instance.Instance) SSOContext {
	iss := "https://" + lab.SignInHost()
	c := SSOContext{Issuer: iss, DiscoveryURL: iss + SSOPathDiscovery, AuthorizeURL: iss + SSOPathAuthorize,
		TokenURL: iss + SSOPathToken, UserinfoURL: iss + SSOPathUserInfo, ClientID: in.ID(),
		ProviderName: lab.DisplayName(), ButtonText: "Sign in with " + lab.DisplayName(),
		UserGroup: SSOUserGroup, AdminGroup: SSOAdminGroup}
	if p.SSO != nil && p.SSO.OIDC != nil && p.SSO.OIDC.Groups != nil {
		c.UserGroup, c.AdminGroup = p.SSO.OIDC.Groups.Users, p.SSO.OIDC.Groups.Admins
	}
	c.Enabled = SSOOn(lab, p, in)
	return c
}

// SSORedirects are an OIDC app's exact redirect addresses: its paths
// (templates rendered) on its route's address, then its phone apps' own.
func SSORedirects(lab config.Lab, p *pkgspec.Package, in *instance.Instance) ([]string, error) {
	o := p.SSO.OIDC
	ctx := buildContext(lab, p, in)
	r, ok := ctx.Routes[o.Route]
	if !ok {
		return nil, fmt.Errorf("sso.oidc.route: unknown route %q", o.Route)
	}
	var out []string
	for i, rp := range o.RedirectPaths {
		path, err := ctx.render(fmt.Sprintf("sso.oidc.redirect_paths[%d]", i), rp)
		if err != nil {
			return nil, err
		}
		if !pkgspec.ValidRedirectPath(path) {
			return nil, fmt.Errorf("sso.oidc.redirect_paths[%d]: %q isn't a plain path once rendered", i, path)
		}
		out = append(out, "https://"+r.Host+path)
	}
	return append(out, o.RedirectURIs...), nil
}

// ssoEnv is a container's env with the package's sso env added while it's
// on, replacing values of the same name.
func (g *gen) ssoEnv(name string, env map[string]string) (map[string]string, error) {
	if !g.sso || g.pkg.SSO.Env[name] == nil {
		return env, nil
	}
	out := maps.Clone(env)
	if out == nil {
		out = map[string]string{}
	}
	for _, k := range slices.Sorted(maps.Keys(g.pkg.SSO.Env[name])) {
		out[k] = g.pkg.SSO.Env[name][k]
	}
	return out, nil
}

// ssoContainer adds an OIDC app's container its client secret, and the
// sign-in host in its hosts file, pointing at this server's Caddy as seen
// from the container: the app swaps codes for tokens there itself
// (docs/ASSUMPTIONS.md V1).
func (g *gen) ssoContainer(name string, add func(key, value string)) {
	if !g.ssoReach {
		return
	}
	g.out.SSOReach = true
	if !g.sso {
		add("AddHost", g.lab.SignInHost()+":host-gateway")
		return
	}
	secrets := g.pkg.SSO.Secrets[name]
	for _, k := range slices.Sorted(maps.Keys(secrets)) {
		add("Secret", fmt.Sprintf("%s,type=env,target=%s", SSOSecretName(g.name), k))
	}
	for _, k := range slices.Sorted(maps.Keys(g.ssoEnvSecrets[name])) {
		add("Secret", fmt.Sprintf("%s,type=env,target=%s", g.ssoEnvSecrets[name][k], k))
	}
	add("AddHost", g.lab.SignInHost()+":host-gateway")
}

// ssoSecretEnv renders the package's secret_env with a marker for the
// client secret, and lists each value as a secret the reconciler makes
// from the client secret (SPEC.md §5.2, "Secrets inside JSON").
func (g *gen) ssoSecretEnv() error {
	if !g.sso || g.pkg.SSO.OIDC == nil {
		return nil
	}
	ctx := *g.ctx
	ctx.SSO.secretMarker = true
	g.ssoEnvSecrets = map[string]map[string]string{}
	for _, c := range slices.Sorted(maps.Keys(g.pkg.SSO.SecretEnv)) {
		env := g.pkg.SSO.SecretEnv[c]
		for _, k := range slices.Sorted(maps.Keys(env)) {
			text, err := ctx.render(fmt.Sprintf("sso.secret_env.%s.%s", c, k), env[k])
			if err != nil {
				return err
			}
			if strings.ContainsAny(text, "\n\r\x00") {
				return fmt.Errorf("sso.secret_env.%s.%s: must render to one line (env can't hold line breaks)", c, k)
			}
			sum := sha256.Sum256([]byte(text))
			name := SSOSecretEnvPrefix(g.name) + strings.ToLower(strings.ReplaceAll(k, "_", "-")) + "-" + hex.EncodeToString(sum[:])[:12]
			if g.ssoEnvSecrets[c] == nil {
				g.ssoEnvSecrets[c] = map[string]string{}
			}
			g.ssoEnvSecrets[c][k] = name
			g.out.Secrets = append(g.out.Secrets, Secret{Name: name,
				Template: &SecretTemplate{Text: text, From: SSOSecretName(g.name)}})
		}
	}
	return nil
}

// ssoSecrets is the app's client secret, made by Roostack.
func (g *gen) ssoSecrets() {
	if g.sso && g.pkg.SSO.OIDC != nil {
		g.out.Secrets = append(g.out.Secrets, Secret{Name: SSOSecretName(g.name), Generate: true})
	}
}

// ssoWall is a walled route's forward auth, nil for any other.
func (g *gen) ssoWall(route string) *caddy.Wall {
	f := g.pkg.SSO
	if !g.sso || f.ForwardAuth == nil || f.ForwardAuth.Route != route {
		return nil
	}
	w := &caddy.Wall{Upstream: fmt.Sprintf("%s:%d", CaddyHostLoopback, config.SignInPort), Service: g.name}
	for _, bp := range f.ForwardAuth.BypassPaths {
		w.Bypass = append(w.Bypass, bp.Path)
	}
	return w
}
