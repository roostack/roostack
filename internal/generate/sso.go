package generate

import (
	"fmt"
	"maps"
	"slices"

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
	// one the lab's Owners and Admins are in too (the "groups" claim).
	UserGroup, AdminGroup string
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

// ContainerHostGateway is the server as its rootless containers see it
// (Podman's host-gateway, pasta's --map-guest-addr), and where their
// connections to its Caddy come from, as Caddy sees them
// (docs/ASSUMPTIONS.md V1).
const ContainerHostGateway = "169.254.1.2"

// SSOSecretName is the Podman secret holding an app's client secret,
// which the sign-in page checks the app's requests against.
func SSOSecretName(service string) string { return "roostack-" + service + "-sso-client-secret" }

func ssoContext(lab config.Lab, p *pkgspec.Package, in *instance.Instance) SSOContext {
	iss := "https://" + lab.SignInHost()
	c := SSOContext{Issuer: iss, DiscoveryURL: iss + SSOPathDiscovery, AuthorizeURL: iss + SSOPathAuthorize,
		TokenURL: iss + SSOPathToken, UserinfoURL: iss + SSOPathUserInfo, ClientID: in.ID(),
		ProviderName: lab.DisplayName(), ButtonText: "Sign in with " + lab.DisplayName(),
		UserGroup: SSOUserGroup, AdminGroup: SSOAdminGroup}
	c.Enabled = SSOOn(lab, p, in)
	return c
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
	if !g.sso || g.pkg.SSO.OIDC == nil {
		return
	}
	secrets := g.pkg.SSO.Secrets[name]
	for _, k := range slices.Sorted(maps.Keys(secrets)) {
		add("Secret", fmt.Sprintf("%s,type=env,target=%s", SSOSecretName(g.name), k))
	}
	add("AddHost", g.lab.SignInHost()+":host-gateway")
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
