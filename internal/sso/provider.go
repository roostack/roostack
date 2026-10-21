package sso

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/store"
)

// Lifetimes (SPEC.md §5.2, "Security").
const (
	// CodeLifetime is how long an app has to swap a code for tokens, and a
	// walled service to take its hand-off. Each works once.
	CodeLifetime = time.Minute
	// AccessLifetime is an access token's and ID token's: an hour, as
	// Vaultwarden needs more than five minutes.
	AccessLifetime = time.Hour
	// RefreshLifetime is a refresh token's longest; it also ends with the
	// sign-in it came from, whichever is first.
	RefreshLifetime = 30 * 24 * time.Hour
	// RefreshGrace is how long a refresh token still works once used, for
	// an app whose answer was lost on the way (a timeout): its retry gets
	// new tokens, and those it never received are revoked. After that, a
	// used refresh token is refused (SPEC.md §5.2).
	RefreshGrace = time.Minute
)

// Groups an app is told a person is in (the "groups" claim): every person
// with access is in UserGroup, and the lab's Owners and Admins are in
// AdminGroup too, so apps that map a group to their admins can. The lab's
// own groups stay in Roostack.
const (
	UserGroup  = "users"
	AdminGroup = "admins"
)

// Paths on the sign-in host, login.<domain>.
const (
	PathDiscovery = "/.well-known/openid-configuration"
	PathAuthorize = "/authorize"
	PathToken     = "/token"
	PathUserInfo  = "/userinfo"
	PathJWKS      = "/jwks"
	PathSignOut   = "/sign-out"
	// PathStart sends a walled service's visitor through signing in.
	PathStart = "/start"
	// PathCheck is forward auth's check, which Caddy asks.
	PathCheck = "/check"
	// PathCallback is on each walled service's own address: Caddy passes
	// it to the sign-in listener, which sets the service's cookie there.
	PathCallback = "/.roostack/sign-in/callback"
)

// HostCookie is a walled service's cookie, on its address only. Caddy
// removes it before a request reaches the service (internal/caddy).
const HostCookie = "roostack_sso"

// Client is an app or walled service the sign-in page knows: each service
// with single sign-on on.
type Client struct {
	// ID is the service's name, its client ID.
	ID    string
	Title string
	// RedirectURIs are where an OIDC app may be sent back to, exactly;
	// none for a walled service.
	RedirectURIs []string
	// Host is a walled service's address (forward auth); "" for an app
	// that signs people in through OIDC.
	Host string
	// Public: its address is public, so the sign-in page is too.
	Public bool
	// UserGroup and AdminGroup rename the groups claim's users and admins
	// for an app that expects its own words (Immich: user and admin;
	// the package's sso.oidc.groups). Empty: UserGroup and AdminGroup.
	UserGroup, AdminGroup string
}

// Session is someone signed in on the sign-in page.
type Session struct {
	User *store.User
	// Hash is the session's token hash: what it hands out ends with it.
	Hash    string
	Expires time.Time
	// Remember: "keep me signed in". Otherwise walled services' cookies
	// end when the browser closes, like the sign-in's own.
	Remember bool
	// AuthTime is when they last typed their password for it (OIDC's
	// auth_time).
	AuthTime time.Time
}

// Provider is the identity provider.
type Provider struct {
	Store *store.Store
	Key   *Key
	Log   *slog.Logger
	// Issuer is the sign-in host's address, https://login.<domain>.
	Issuer func() string
	// Clients are the services with single sign-on on.
	Clients func() []Client
	// ClientSecret is an app's secret: the Podman secret Roostack made for
	// it, which the app is given too.
	ClientSecret func(ctx context.Context, id string) ([]byte, error)
	// Now is the time (tests set it).
	Now func() time.Time

	mu       sync.Mutex
	codes    map[string]*authCode
	rotated  map[string]*rotation
	handoffs map[string]*handoff
	secrets  map[string]cachedSecret
}

// authCode is a code an app swaps for tokens, kept in memory: a restart
// means signing in again, as codes last a minute.
type authCode struct {
	client, redirectURI, nonce, challenge, scope string
	userID                                       int64
	session                                      string
	sessionEnds, authTime                        time.Time
	expires                                      time.Time
	// used: swapped already. A code used again revokes the tokens it gave
	// (RFC 6749 §4.1.2), so it's kept, used, until it expires.
	used   bool
	tokens []string
}

// rotation is a refresh token just used, kept in memory for RefreshGrace:
// a restart means the app signs in again, as with codes.
type rotation struct {
	token store.SSOToken
	// tokens are the access and refresh tokens it was swapped for last,
	// revoked if it's used again.
	tokens []string
	until  time.Time
}

// handoff takes a walled service's visitor from the sign-in host back to
// the service's address, where its cookie is set.
type handoff struct {
	client, rd  string
	userID      int64
	session     string
	sessionEnds time.Time
	remember    bool
	expires     time.Time
}

type cachedSecret struct {
	value []byte
	until time.Time
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Client finds a service with single sign-on by its ID.
func (p *Provider) Client(id string) (Client, bool) {
	for _, c := range p.Clients() {
		if c.ID == id {
			return c, true
		}
	}
	return Client{}, false
}

// Discovery is the provider's configuration, which apps read from
// PathDiscovery.
func (p *Provider) Discovery(w http.ResponseWriter, r *http.Request) {
	iss := p.Issuer()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                iss + PathAuthorize,
		"token_endpoint":                        iss + PathToken,
		"userinfo_endpoint":                     iss + PathUserInfo,
		"jwks_uri":                              iss + PathJWKS,
		"end_session_endpoint":                  iss + PathSignOut,
		"response_types_supported":              []string{"code"},
		"response_modes_supported":              []string{"query"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"openid", "profile", "email", "groups", "offline_access"},
		"claims_supported": []string{"sub", "iss", "aud", "exp", "iat", "auth_time", "nonce", "azp", "name",
			"preferred_username", "email", "email_verified", "groups"},
		// Request objects and the claims parameter aren't supported; say so,
		// as request_uri_parameter_supported is true when left out.
		"request_parameter_supported":     false,
		"request_uri_parameter_supported": false,
		"claims_parameter_supported":      false,
	})
}

// JWKS is the public key apps check ID tokens with.
func (p *Provider) JWKS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "max-age=3600")
	w.Write(p.Key.JWKS())
}

// AuthRequest is an app asking to sign someone in, checked.
type AuthRequest struct {
	Client      Client
	RedirectURI string
	State       string
	Nonce       string
	Scope       string
	Challenge   string
	// PromptNone: the app asks not to show anything; if nobody is signed
	// in, it's told so.
	PromptNone bool
	// PromptLogin: the app asks for the password to be typed again, even
	// if someone is signed in.
	PromptLogin bool
	// MaxAge is how long ago, at most, the password may have been typed
	// (seconds); -1 if the app didn't say.
	MaxAge int
}

// SignInAgain says whether the person signed in must type their password
// again first (prompt=login, or max_age passed).
func (req *AuthRequest) SignInAgain(s Session, now time.Time) bool {
	if req.PromptLogin {
		return true
	}
	return req.MaxAge >= 0 && now.Sub(s.AuthTime) > time.Duration(req.MaxAge)*time.Second
}

// AuthError is a refused authorisation request. Once the client and its
// redirect address are known to be right, the app is told (Redirect);
// before that, only the person is (the sign-in page shows Message), so a
// wrong address never gets a code or an error to forward elsewhere.
type AuthError struct {
	Redirect string
	Message  string
}

func (e *AuthError) Error() string { return e.Message }

// ParseAuthorize checks an authorisation request (PathAuthorize's query).
func (p *Provider) ParseAuthorize(q url.Values) (*AuthRequest, *AuthError) {
	c, ok := p.Client(q.Get("client_id"))
	if !ok || len(c.RedirectURIs) == 0 {
		return nil, &AuthError{Message: "This app isn't set up to sign in here. Turn on single sign-on on its page in Roostack."}
	}
	redirect := q.Get("redirect_uri")
	if !slices.Contains(c.RedirectURIs, redirect) {
		return nil, &AuthError{Message: c.Title + " asked to come back to an address Roostack doesn't know for it, so it wasn't signed in."}
	}
	prompt := strings.Fields(q.Get("prompt"))
	req := &AuthRequest{Client: c, RedirectURI: redirect, State: q.Get("state"), Nonce: q.Get("nonce"),
		Scope: q.Get("scope"), Challenge: q.Get("code_challenge"), PromptNone: slices.Contains(prompt, "none"),
		PromptLogin: slices.Contains(prompt, "login"), MaxAge: -1}
	fail := func(code, desc string) (*AuthRequest, *AuthError) {
		return nil, &AuthError{Redirect: errorRedirect(redirect, req.State, code, desc), Message: desc}
	}
	if v := q.Get("max_age"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return fail("invalid_request", "max_age must be a number of seconds")
		}
		req.MaxAge = n
	}
	switch {
	case req.PromptNone && len(prompt) > 1:
		return fail("invalid_request", "prompt=none can't be used with other prompt values")
	case q.Get("response_type") != "code":
		return fail("unsupported_response_type", "only the code flow is supported")
	case !slices.Contains(strings.Fields(req.Scope), "openid"):
		return fail("invalid_scope", "the openid scope is required")
	case q.Get("request") != "" || q.Get("request_uri") != "":
		return fail("request_not_supported", "request objects aren't supported")
	case req.Challenge != "" && q.Get("code_challenge_method") != "S256":
		return fail("invalid_request", "only the S256 code challenge method is supported")
	case q.Get("response_mode") != "" && q.Get("response_mode") != "query":
		return fail("invalid_request", "only the query response mode is supported")
	}
	return req, nil
}

// LoginRequired is the redirect telling an app that asked to show nothing
// (prompt=none) that nobody is signed in.
func (req *AuthRequest) LoginRequired() string {
	return errorRedirect(req.RedirectURI, req.State, "login_required", "nobody is signed in")
}

// AccessDenied is the redirect telling an app the person may not use it.
func (req *AuthRequest) AccessDenied() string {
	return errorRedirect(req.RedirectURI, req.State, "access_denied", "this person may not use this app")
}

func errorRedirect(redirect, state, code, desc string) string {
	q := url.Values{"error": {code}, "error_description": {desc}}
	if state != "" {
		q.Set("state", state)
	}
	return addQuery(redirect, q)
}

func addQuery(u string, q url.Values) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + q.Encode()
}

// Approve gives the app a code for the person signed in (whose access the
// caller checked) and returns where to send the browser.
func (p *Provider) Approve(req *AuthRequest, s Session) (string, error) {
	code, err := authkit.NewToken()
	if err != nil {
		return "", err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.codes == nil {
		p.codes = map[string]*authCode{}
	}
	now := p.now()
	for k, c := range p.codes {
		if now.After(c.expires) {
			delete(p.codes, k)
		}
	}
	p.codes[authkit.HashToken(code)] = &authCode{client: req.Client.ID, redirectURI: req.RedirectURI, nonce: req.Nonce,
		challenge: req.Challenge, scope: req.Scope, userID: s.User.ID, session: s.Hash, sessionEnds: s.Expires,
		authTime: s.AuthTime, expires: now.Add(CodeLifetime)}
	q := url.Values{"code": {code}}
	if req.State != "" {
		q.Set("state", req.State)
	}
	q.Set("iss", p.Issuer()) // RFC 9207: which provider answered
	return addQuery(req.RedirectURI, q), nil
}

// takeCode returns a code's request and marks it used: a code works once.
// A code used before comes back with used set, so its tokens can be
// revoked.
func (p *Provider) takeCode(code string) *authCode {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := authkit.HashToken(code)
	c := p.codes[key]
	if c == nil || p.now().After(c.expires) {
		delete(p.codes, key)
		return nil
	}
	if c.used {
		return c
	}
	first := *c
	c.used = true
	return &first
}

// codeTokens records the tokens a code gave, to revoke if it's used again.
func (p *Provider) codeTokens(code string, tokens []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.codes[authkit.HashToken(code)]; c != nil {
		c.tokens = tokens
	}
}

// revokeCode ends the tokens a code gave, when someone tries it again
// (RFC 6749 §4.1.2): the code may have been stolen.
func (p *Provider) revokeCode(ctx context.Context, ac *authCode) {
	p.mu.Lock()
	tokens := ac.tokens
	ac.tokens = nil
	p.mu.Unlock()
	for _, h := range tokens {
		if _, err := p.Store.DeleteSSOToken(ctx, h); err != nil {
			p.Log.Error("revoking a reused code's tokens", "service", ac.client, "err", err)
		}
	}
	if len(tokens) > 0 {
		p.Log.Warn("an app's code was used twice; its tokens are revoked", "service", ac.client)
	}
}

// tokenError is the token endpoint's refusal (RFC 6749 §5.2).
func tokenError(w http.ResponseWriter, status int, code, desc string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Basic realm="sign-in"`)
	}
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

// Token swaps a code or a refresh token for tokens (PathToken).
func (p *Provider) Token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		tokenError(w, http.StatusMethodNotAllowed, "invalid_request", "use POST")
		return
	}
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request", "the form can't be read")
		return
	}
	c, ok := p.authenticate(r)
	if !ok {
		tokenError(w, http.StatusUnauthorized, "invalid_client", "the app's ID or secret is wrong")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		p.codeGrant(w, r, c)
	case "refresh_token":
		p.refreshGrant(w, r, c)
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type", "only authorization_code and refresh_token are supported")
	}
}

func (p *Provider) codeGrant(w http.ResponseWriter, r *http.Request, c Client) {
	code := r.PostForm.Get("code")
	ac := p.takeCode(code)
	if ac != nil && ac.used {
		p.revokeCode(r.Context(), ac)
		ac = nil
	}
	switch {
	case ac == nil || ac.client != c.ID:
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the code is wrong, used or too old")
		return
	case ac.redirectURI != r.PostForm.Get("redirect_uri"):
		tokenError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri isn't the one the code was given for")
		return
	case ac.challenge != "" && !verifierMatches(r.PostForm.Get("code_verifier"), ac.challenge):
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the code verifier doesn't match")
		return
	case ac.challenge == "" && r.PostForm.Get("code_verifier") != "":
		tokenError(w, http.StatusBadRequest, "invalid_grant", "a code verifier for a code given without a challenge")
		return
	}
	u, err := p.Store.UserByID(r.Context(), ac.userID)
	if err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "that person is gone")
		return
	}
	p.codeTokens(code, p.issue(w, r, c, u, ac.session, ac.sessionEnds, ac.authTime, ac.scope, ac.nonce))
}

// refreshGrant swaps a refresh token for new tokens. Each refresh token is
// swapped once, then works again only for RefreshGrace, for an app retrying
// after a lost answer: the retry revokes what the last swap gave.
func (p *Provider) refreshGrant(w http.ResponseWriter, r *http.Request, c Client) {
	ctx := r.Context()
	hash := authkit.HashToken(r.PostForm.Get("refresh_token"))
	var rot *rotation
	t, _, err := p.Store.LookupSSOToken(ctx, hash, store.TokenRefresh)
	if err == nil && t.Service == c.ID {
		if ok, err := p.Store.DeleteSSOToken(ctx, hash); err == nil && ok {
			rot = p.rotate(hash, *t)
		}
	}
	if rot == nil {
		// Used already (or two requests raced): a retry within the grace.
		rot = p.retry(hash, c.ID)
		if rot == nil {
			tokenError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is wrong, used or ended")
			return
		}
		p.revoke(ctx, rot, "a refresh token used again")
	}
	t = &rot.token
	u, err := p.Store.UserByID(ctx, t.UserID)
	if err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "that person is gone")
		return
	}
	si, err := p.Store.SignInSession(ctx, t.Session)
	if err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "the sign-in has ended")
		return
	}
	tokens := p.issue(w, r, c, u, t.Session, si.Expires, si.At, t.Scope, "")
	p.mu.Lock()
	rot.tokens = tokens
	p.mu.Unlock()
}

// rotate keeps a refresh token just used for RefreshGrace.
func (p *Provider) rotate(hash string, t store.SSOToken) *rotation {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for k, rt := range p.rotated {
		if now.After(rt.until) {
			delete(p.rotated, k)
		}
	}
	if p.rotated == nil {
		p.rotated = map[string]*rotation{}
	}
	rot := &rotation{token: t, until: now.Add(RefreshGrace)}
	p.rotated[hash] = rot
	return rot
}

// retry is a refresh token used within RefreshGrace by its own app, or
// nil. The grace counts from the first use, however many retries.
func (p *Provider) retry(hash, client string) *rotation {
	p.mu.Lock()
	defer p.mu.Unlock()
	rot := p.rotated[hash]
	if rot == nil || p.now().After(rot.until) || rot.token.Service != client {
		return nil
	}
	return rot
}

// revoke ends the tokens rot was last swapped for.
func (p *Provider) revoke(ctx context.Context, rot *rotation, why string) {
	p.mu.Lock()
	tokens := rot.tokens
	rot.tokens = nil
	p.mu.Unlock()
	for _, h := range tokens {
		if _, err := p.Store.DeleteSSOToken(ctx, h); err != nil {
			p.Log.Error("revoking tokens", "service", rot.token.Service, "why", why, "err", err)
		}
	}
}

// issue answers with an access token, a refresh token and an ID token,
// once the person is checked to still have access. It returns the tokens'
// hashes (none if it refused).
func (p *Provider) issue(w http.ResponseWriter, r *http.Request, c Client, u *store.User, session string, sessionEnds, authTime time.Time, scope, nonce string) []string {
	ctx := r.Context()
	if ok, err := p.Store.CanUse(ctx, c.ID, u.ID); err != nil || !ok {
		tokenError(w, http.StatusBadRequest, "invalid_grant", "this person may no longer use this app")
		return nil
	}
	claims, err := p.claims(ctx, c, u, scope)
	if err != nil {
		p.serverError(w, err)
		return nil
	}
	now := p.now()
	access, err := p.newToken(ctx, store.TokenAccess, c.ID, u.ID, session, scope, earliest(now.Add(AccessLifetime), sessionEnds))
	if err != nil {
		p.serverError(w, err)
		return nil
	}
	refresh, err := p.newToken(ctx, store.TokenRefresh, c.ID, u.ID, session, scope, earliest(now.Add(RefreshLifetime), sessionEnds))
	if err != nil {
		p.serverError(w, err)
		return nil
	}
	tokens := []string{authkit.HashToken(access), authkit.HashToken(refresh)}
	claims["iss"] = p.Issuer()
	claims["aud"] = c.ID
	claims["azp"] = c.ID
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(AccessLifetime).Unix()
	// Always there, as apps may ask with max_age (OIDC Core §2), and the
	// same after a refresh (§12.2).
	claims["auth_time"] = authTime.Unix()
	if nonce != "" {
		claims["nonce"] = nonce
	}
	idToken, err := p.Key.Sign(claims)
	if err != nil {
		p.serverError(w, err)
		return tokens
	}
	p.Store.Audit(ctx, u, "single sign-on", "signed in to "+c.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": access, "token_type": "Bearer", "expires_in": int(AccessLifetime.Seconds()),
		"refresh_token": refresh, "id_token": idToken, "scope": scope,
	})
	return tokens
}

func earliest(a, b time.Time) time.Time {
	if b.IsZero() || a.Before(b) {
		return a
	}
	return b
}

func (p *Provider) newToken(ctx context.Context, kind, client string, userID int64, session, scope string, expires time.Time) (string, error) {
	tok, err := authkit.NewToken()
	if err != nil {
		return "", err
	}
	err = p.Store.AddSSOToken(ctx, store.SSOToken{Hash: authkit.HashToken(tok), Kind: kind, Service: client,
		UserID: userID, Session: session, Scope: scope, Expires: expires})
	return tok, err
}

// allScopes are every scope with claims: what a walled service is told.
const allScopes = "openid profile email groups"

// claims are what an app is told about a person, in its ID token and from
// userinfo: sub always, and the rest by the scopes it asked for (profile:
// name and username; email: the address; groups: the groups claim), so an
// app learns only what it needs (SPEC.md §5.2, decided 2026-10-20 after
// the conformance suite).
func (p *Provider) claims(ctx context.Context, cl Client, u *store.User, scope string) (map[string]any, error) {
	sub, err := p.Store.EnsureSSOID(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	c := map[string]any{"sub": sub}
	scopes := strings.Fields(scope)
	if slices.Contains(scopes, "profile") {
		name := u.Name
		if name == "" {
			name = u.Username
		}
		c["preferred_username"], c["name"] = u.Username, name
	}
	if slices.Contains(scopes, "email") && u.Email != "" {
		// Typed by the Owner or by the person while signed in.
		c["email"], c["email_verified"] = u.Email, true
	}
	if slices.Contains(scopes, "groups") {
		groups := []string{cmp.Or(cl.UserGroup, UserGroup)}
		if u.Role == auth.RoleOwner || u.Role == auth.RoleAdmin {
			groups = append(groups, cmp.Or(cl.AdminGroup, AdminGroup))
		}
		c["groups"] = groups
	}
	return c, nil
}

// UserInfo tells an app who an access token's person is (PathUserInfo).
func (p *Provider) UserInfo(w http.ResponseWriter, r *http.Request) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok && r.Method == http.MethodPost {
		tok = r.PostFormValue("access_token")
	}
	t, u, err := p.Store.LookupSSOToken(r.Context(), authkit.HashToken(strings.TrimSpace(tok)), store.TokenAccess)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	if ok, err := p.Store.CanUse(r.Context(), t.Service, u.ID); err != nil || !ok {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
		return
	}
	cl, _ := p.Client(t.Service)
	claims, err := p.claims(r.Context(), cl, u, t.Scope)
	if err != nil {
		p.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, claims)
}

// authenticate checks the app's ID and secret, sent as HTTP Basic
// (client_secret_basic, form-encoded first) or in the form
// (client_secret_post).
func (p *Provider) authenticate(r *http.Request) (Client, bool) {
	id, secret, basic := r.BasicAuth()
	if basic {
		var err1, err2 error
		id, err1 = url.QueryUnescape(id)
		secret, err2 = url.QueryUnescape(secret)
		if err1 != nil || err2 != nil {
			return Client{}, false
		}
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	c, ok := p.Client(id)
	if !ok || len(c.RedirectURIs) == 0 || secret == "" {
		return Client{}, false
	}
	if p.secretMatches(r.Context(), id, secret, false) {
		return c, true
	}
	// The secret may have changed since it was read (single sign-on turned
	// off and on): read it again once.
	return c, p.secretMatches(r.Context(), id, secret, true)
}

// secretCache is how long an app's secret is kept in memory once read.
const secretCache = 5 * time.Minute

func (p *Provider) secretMatches(ctx context.Context, id, secret string, fresh bool) bool {
	p.mu.Lock()
	cached, ok := p.secrets[id]
	p.mu.Unlock()
	if fresh || !ok || p.now().After(cached.until) {
		v, err := p.ClientSecret(ctx, id)
		if err != nil {
			p.Log.Error("reading an app's single sign-on secret", "service", id, "err", err)
			return false
		}
		cached = cachedSecret{value: []byte(strings.TrimSpace(string(v))), until: p.now().Add(secretCache)}
		p.mu.Lock()
		if p.secrets == nil {
			p.secrets = map[string]cachedSecret{}
		}
		p.secrets[id] = cached
		p.mu.Unlock()
	}
	return len(cached.value) > 0 && subtle.ConstantTimeCompare(cached.value, []byte(secret)) == 1
}

// verifierMatches checks a PKCE code verifier against its S256 challenge.
func verifierMatches(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

func (p *Provider) serverError(w http.ResponseWriter, err error) {
	p.Log.Error("single sign-on", "err", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// ErrNoAccess means the person signed in may not use the service.
var ErrNoAccess = errors.New("sso: no access")
