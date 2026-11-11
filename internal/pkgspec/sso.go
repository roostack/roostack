package pkgspec

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// SSO says how an app takes single sign-on (SPEC.md §5.2): through OIDC,
// or behind forward auth (a wall Caddy puts in front of it). The Owner
// turns it on per service; until then nothing here applies.
type SSO struct {
	OIDC        *SSOOIDC        `yaml:"oidc"`
	ForwardAuth *SSOForwardAuth `yaml:"forward_auth"`
	// Required says the app has no sign-in of its own (Outline), so nobody
	// could sign in without this: single sign-on is always on, no Owner
	// turns it off, and the service must run where it works (RequiresSSO).
	Required bool `yaml:"required"`
	// Env is added to the named containers while single sign-on is on,
	// replacing their own value of the same name: several apps fail on an
	// empty value, so a template that renders nothing isn't enough.
	// Templates have .SSO (generate.SSOContext).
	Env map[string]map[string]string `yaml:"env"`
	// Secrets are added the same way: container → variable → SSOClientSecret.
	Secrets map[string]map[string]string `yaml:"secrets"`
	// SecretEnv is env whose whole value holds the client secret, such as
	// Paperless-ngx's JSON list of providers: container → variable → a
	// template, in which .SSO.ClientSecret is the secret. Roostack makes
	// the value as a Podman secret, so it's never written into a file it
	// generates (SPEC.md §5.2, "Secrets inside JSON").
	SecretEnv map[string]map[string]string `yaml:"secret_env"`
	// Note says what still uses the app's own sign-in (its phone apps, its
	// API tokens), shown beside the switch.
	Note string `yaml:"note"`
}

// SSOClientSecret is the one secret a package's sso part can give a
// container: the app's own client secret, which Roostack makes.
const SSOClientSecret = "sso.client_secret"

// SSOOIDC is an app that signs people in through OIDC itself.
type SSOOIDC struct {
	// Route is the route whose address the redirect paths are on.
	Route string `yaml:"route"`
	// RedirectPaths are where the sign-in page may send people back to on
	// that address, exactly (e.g. /login).
	RedirectPaths []string `yaml:"redirect_paths"`
	// StartPath is where on that address opening a link starts the app's
	// sign-in through the lab, so that someone signed in on the sign-in
	// page lands signed in to the app (Gokapi's /oauth-login): the sign-in
	// page and Roostack's dashboard link there while single sign-on is on.
	// A path with an optional query, which may use templates like the
	// redirect paths. Left out, they link to the app's front page, where
	// the app shows its own button.
	StartPath string `yaml:"start_path"`
	// RedirectURIs are other addresses it may send them to, for the app's
	// own phone apps (app.immich:///oauth-callback): never http or https,
	// which could be anyone's site.
	RedirectURIs []string `yaml:"redirect_uris"`
	// Groups renames the two groups the app is told about, for apps that
	// expect their own words (Immich and Audiobookshelf: user and admin).
	Groups *SSOGroups `yaml:"groups"`
	// Admins says the app makes the lab's Owners and Admins its own
	// admins, from the groups claim; the service's card says so only then.
	Admins bool `yaml:"admins"`
	// ExistingAccounts says what the app does when someone who signs in
	// already has an account in it (an SSOExisting value). Left out, the
	// card says nothing about those accounts.
	ExistingAccounts string `yaml:"existing_accounts"`
}

// What an app does with an account it already has when its person first
// signs in through the lab (SSOOIDC.ExistingAccounts).
const (
	SSOExistingEmail           = "email"             // uses the one with their email address
	SSOExistingUsername        = "username"          // uses the one with their username
	SSOExistingUsernameOrEmail = "username_or_email" // their username, or else their email address
	SSOExistingSeparate        = "separate"          // makes a new one beside it
)

// SSOExisting are the values of sso.oidc.existing_accounts.
var SSOExisting = []string{SSOExistingEmail, SSOExistingUsername, SSOExistingUsernameOrEmail, SSOExistingSeparate}

// SSOGroups are the names of the groups in an app's "groups" claim.
type SSOGroups struct {
	// Users is everyone with access; Admins the lab's Owners and Admins
	// among them.
	Users  string `yaml:"users"`
	Admins string `yaml:"admins"`
}

// SSOForwardAuth is an app behind the wall: everyone signs in on the
// lab's sign-in page before a request reaches it. Its containers may read
// who someone is from the Remote-User, Remote-Email, Remote-Name and
// Remote-Groups headers.
type SSOForwardAuth struct {
	Route string `yaml:"route"`
	// BypassPaths stay outside the wall (an API phone apps use with the
	// app's own tokens, share links), each with the reason it's safe.
	BypassPaths []PrivatePath `yaml:"bypass_paths"`
	// OwnSignIn says the app keeps its own sign-in behind the wall, which
	// protects the way in but doesn't sign anyone in to it, so people are
	// told so (SPEC.md §5.2, "A wall in front of an app's own sign-in").
	OwnSignIn bool `yaml:"own_sign_in"`
}

var (
	// redirectPathRe allows a trailing slash, which some apps' callbacks
	// have (Paperless-ngx's /accounts/oidc/<id>/login/callback/), and
	// escapes (%20), which a templated path's values get from pathescape
	// (Forgejo's /user/oauth2/<the lab's name>/callback).
	redirectPathRe = regexp.MustCompile(`^(/([A-Za-z0-9._:~-]|%[0-9A-F]{2})*)+$`)
	// startQueryRe is a start path's query or fragment: plain letters,
	// digits, the unreserved signs, = & + / : ? and percent escapes.
	startQueryRe = regexp.MustCompile(`^([A-Za-z0-9._:~/=&+?-]|%[0-9A-F]{2})*$`)
	// groupNameRe is a group's name in the groups claim.
	groupNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	// appSchemeRe is a phone app's own address scheme (RFC 8252 §7.1).
	appSchemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.-]*:/[^\s]*$`)
)

// ValidRedirectPath says whether a redirect path, once its templates are
// rendered, is a plain path.
func ValidRedirectPath(p string) bool {
	return redirectPathRe.MatchString(p) && !strings.Contains(p, "//")
}

// ValidStartPath says whether a rendered sso.oidc.start_path is a plain
// path, as a redirect path is, with an optional query and fragment (an
// app whose own scripts route by it: Vaultwarden's /#/sso?identifier=…).
func ValidStartPath(p string) bool {
	rest, fragment, _ := strings.Cut(p, "#")
	path, query, _ := strings.Cut(rest, "?")
	return ValidRedirectPath(path) && startQueryRe.MatchString(query) && startQueryRe.MatchString(fragment)
}

// KeepsOwnSignIn says the app keeps its own sign-in behind a wall
// (SSOForwardAuth.OwnSignIn).
func (p *Package) KeepsOwnSignIn() bool {
	return p.SSO != nil && p.SSO.ForwardAuth != nil && p.SSO.ForwardAuth.OwnSignIn
}

// RequiresSSO says the app has no sign-in of its own, so single sign-on
// is always on for it (SSO.Required).
func (p *Package) RequiresSSO() bool { return p.SSO != nil && p.SSO.Required }

// SSORoute is the route single sign-on is on ("" without it).
func (p *Package) SSORoute() string {
	switch {
	case p.SSO == nil:
		return ""
	case p.SSO.OIDC != nil:
		return p.SSO.OIDC.Route
	case p.SSO.ForwardAuth != nil:
		return p.SSO.ForwardAuth.Route
	}
	return ""
}

func (p *Package) validateSSO(v *validator) {
	s := p.SSO
	if s == nil {
		return
	}
	switch {
	case s.OIDC == nil && s.ForwardAuth == nil:
		v.addf("sso: needs oidc or forward_auth")
	case s.OIDC != nil && s.ForwardAuth != nil:
		v.addf("sso: oidc or forward_auth, not both")
	}
	if o := s.OIDC; o != nil {
		if p.Routes[o.Route] == nil {
			v.addf("sso.oidc.route: unknown route %q", o.Route)
		}
		if len(o.RedirectPaths)+len(o.RedirectURIs) == 0 {
			v.addf("sso.oidc: needs redirect_paths (or redirect_uris)")
		}
		for i, rp := range o.RedirectPaths {
			// A templated path is checked once it's rendered (generate).
			if templated := strings.Contains(rp, "{{"); templated && !strings.HasPrefix(rp, "/") ||
				!templated && !ValidRedirectPath(rp) {
				v.addf("sso.oidc.redirect_paths[%d]: %q must be a path starting with /, with only letters, digits, and - _ . : ~ /", i, rp)
			}
		}
		// Templated, it's checked once it's rendered (generate).
		if sp := o.StartPath; sp != "" && (strings.Contains(sp, "{{") && !strings.HasPrefix(sp, "/") ||
			!strings.Contains(sp, "{{") && !ValidStartPath(sp)) {
			v.addf("sso.oidc.start_path: %q must be a path starting with /, with only letters, digits, and - _ . : ~ /, and an optional query after ? and fragment after #", sp)
		}
		if g := o.Groups; g != nil {
			if !groupNameRe.MatchString(g.Users) || !groupNameRe.MatchString(g.Admins) {
				v.addf("sso.oidc.groups: users and admins must each be a lowercase name (letters, digits, -, and _)")
			} else if g.Users == g.Admins {
				v.addf("sso.oidc.groups: users and admins must differ")
			}
		}
		if o.ExistingAccounts != "" && !slices.Contains(SSOExisting, o.ExistingAccounts) {
			v.addf("sso.oidc.existing_accounts: %q must be one of %s", o.ExistingAccounts, strings.Join(SSOExisting, ", "))
		}
		for i, u := range o.RedirectURIs {
			if !appSchemeRe.MatchString(u) || strings.HasPrefix(u, "http:") || strings.HasPrefix(u, "https:") {
				v.addf("sso.oidc.redirect_uris[%d]: %q must be a phone app's own address (like app.example:/callback), not a web address", i, u)
			}
		}
	}
	if f := s.ForwardAuth; f != nil {
		if f.OwnSignIn && s.Required {
			v.addf("sso.forward_auth.own_sign_in: an app with a sign-in of its own can't need single sign-on (required)")
		}
		if p.Routes[f.Route] == nil {
			v.addf("sso.forward_auth.route: unknown route %q", f.Route)
		}
		seen := map[string]bool{}
		for i, bp := range f.BypassPaths {
			if !ValidPath(bp.Path) {
				v.addf("sso.forward_auth.bypass_paths[%d].path: %q must start with / and have only letters, digits, and - _ . : / (no * or trailing /)", i, bp.Path)
			}
			if strings.TrimSpace(bp.Reason) == "" {
				v.addf("sso.forward_auth.bypass_paths[%d].reason: say why it's safe without signing in", i)
			}
			if seen[bp.Path] {
				v.addf("sso.forward_auth.bypass_paths[%d].path: %q is listed twice", i, bp.Path)
			}
			seen[bp.Path] = true
		}
	}
	for _, c := range slices.Sorted(maps.Keys(s.Env)) {
		if p.Containers[c] == nil {
			v.addf("sso.env.%s: unknown container", c)
		}
		for _, k := range slices.Sorted(maps.Keys(s.Env[c])) {
			if !envNameRe.MatchString(k) {
				v.addf("sso.env.%s.%s: invalid variable name", c, k)
			}
		}
	}
	for _, c := range slices.Sorted(maps.Keys(s.Secrets)) {
		if p.Containers[c] == nil {
			v.addf("sso.secrets.%s: unknown container", c)
		}
		for _, k := range slices.Sorted(maps.Keys(s.Secrets[c])) {
			if !envNameRe.MatchString(k) {
				v.addf("sso.secrets.%s.%s: invalid variable name", c, k)
			}
			if s.Secrets[c][k] != SSOClientSecret {
				v.addf("sso.secrets.%s.%s: %s", c, k, fmt.Sprintf("must be %s", SSOClientSecret))
			}
		}
	}
	for _, c := range slices.Sorted(maps.Keys(s.SecretEnv)) {
		if p.Containers[c] == nil {
			v.addf("sso.secret_env.%s: unknown container", c)
		}
		for _, k := range slices.Sorted(maps.Keys(s.SecretEnv[c])) {
			if !envNameRe.MatchString(k) {
				v.addf("sso.secret_env.%s.%s: invalid variable name", c, k)
			}
			if _, dup := s.Env[c][k]; dup {
				v.addf("sso.secret_env.%s.%s: also set in sso.env", c, k)
			}
			if _, dup := s.Secrets[c][k]; dup {
				v.addf("sso.secret_env.%s.%s: also set in sso.secrets", c, k)
			}
			if !strings.Contains(s.SecretEnv[c][k], ".SSO.ClientSecret") {
				v.addf("sso.secret_env.%s.%s: must use {{ .SSO.ClientSecret }} (otherwise put it in sso.env)", c, k)
			}
		}
	}
	if s.ForwardAuth != nil && (len(s.Secrets) > 0 || len(s.SecretEnv) > 0) {
		v.addf("sso.secrets: only an oidc app has a client secret")
	}
}
