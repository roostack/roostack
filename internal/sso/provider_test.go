package sso

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/store"
)

type fixture struct {
	p       *Provider
	st      *store.Store
	mum     *store.User
	session Session
}

const (
	issuer   = "https://login.smiths.example"
	callback = "https://recipes.smiths.example/login"
	secret   = "s3cret-from-podman"
)

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key, err := LoadKey(filepath.Join(t.TempDir(), "sso", "signing-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, _ := st.CreateUser(ctx, store.User{Username: "mum", PasswordHash: "x", Role: "member", Email: "margaret@smith.family"})
	st.SetName(ctx, id, "Margaret Smith")
	mum, _ := st.UserByID(ctx, id)
	expires := time.Now().Add(12 * time.Hour)
	if err := st.CreateSessionAt(ctx, store.OriginSignIn, "sess-hash", id, expires, false); err != nil {
		t.Fatal(err)
	}
	p := &Provider{Store: st, Key: key, Log: slog.New(slog.DiscardHandler),
		Issuer: func() string { return issuer },
		Clients: func() []Client {
			return []Client{
				{ID: "mealie", Title: "Mealie", RedirectURIs: []string{callback}},
				{ID: "stirling-pdf", Title: "Stirling-PDF", Host: "pdf.smiths.example"},
			}
		},
		ClientSecret: func(_ context.Context, id string) ([]byte, error) { return []byte(secret + "\n"), nil },
	}
	si, err := st.SignInSession(ctx, "sess-hash")
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{p: p, st: st, mum: mum, session: Session{User: mum, Hash: "sess-hash", Expires: expires, AuthTime: si.At}}
}

func authorize(t *testing.T, f *fixture, q url.Values) (string, *AuthError) {
	t.Helper()
	req, aerr := f.p.ParseAuthorize(q)
	if aerr != nil {
		return "", aerr
	}
	to, err := f.p.Approve(req, f.session)
	if err != nil {
		t.Fatal(err)
	}
	return to, nil
}

func authQuery(extra ...string) url.Values {
	q := url.Values{"client_id": {"mealie"}, "redirect_uri": {callback}, "response_type": {"code"},
		"scope": {"openid profile email groups"}, "state": {"st8"}, "nonce": {"n0nce"}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return q
}

func codeFrom(t *testing.T, to string) string {
	t.Helper()
	u, err := url.Parse(to)
	if err != nil || !strings.HasPrefix(to, callback+"?") || u.Query().Get("state") != "st8" || u.Query().Get("iss") != issuer {
		t.Fatalf("redirect %q", to)
	}
	return u.Query().Get("code")
}

func token(f *fixture, form url.Values, basic bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, PathToken, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basic {
		r.SetBasicAuth("mealie", url.QueryEscape(secret))
	} else {
		form.Set("client_id", "mealie")
		form.Set("client_secret", secret)
		r = httptest.NewRequest(http.MethodPost, PathToken, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w := httptest.NewRecorder()
	f.p.Token(w, r)
	return w
}

type tokens struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	ID      string `json:"id_token"`
	Error   string `json:"error"`
}

func decode(t *testing.T, w *httptest.ResponseRecorder) tokens {
	t.Helper()
	var tk tokens
	if err := json.Unmarshal(w.Body.Bytes(), &tk); err != nil {
		t.Fatalf("%d %s: %v", w.Code, w.Body, err)
	}
	return tk
}

// verify checks an ID token's signature with the published key set, and
// returns its claims.
func verify(t *testing.T, f *fixture, jwt string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	f.p.JWKS(w, httptest.NewRequest(http.MethodGet, PathJWKS, nil))
	var set struct {
		Keys []struct{ Kid, N, E string }
	}
	json.Unmarshal(w.Body.Bytes(), &set)
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 || len(set.Keys) != 1 {
		t.Fatalf("token %q, keys %s", jwt, w.Body)
	}
	n, _ := base64.RawURLEncoding.DecodeString(set.Keys[0].N)
	e, _ := base64.RawURLEncoding.DecodeString(set.Keys[0].E)
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
	var header map[string]string
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	json.Unmarshal(h, &header)
	if header["alg"] != "RS256" || header["kid"] != set.Keys[0].Kid {
		t.Errorf("header %v", header)
	}
	var claims map[string]any
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(b, &claims)
	return claims
}

// The code flow, end to end: a code once, tokens, the ID token's claims,
// userinfo, and refresh tokens that each work once.
func TestCodeFlow(t *testing.T) {
	f := newFixture(t)
	code := codeFrom(t, must(authorize(t, f, authQuery())))
	w := token(f, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback}}, true)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	tk := decode(t, w)
	c := verify(t, f, tk.ID)
	if c["iss"] != issuer || c["aud"] != "mealie" || c["nonce"] != "n0nce" || c["email"] != "margaret@smith.family" ||
		c["email_verified"] != true || c["name"] != "Margaret Smith" || c["preferred_username"] != "mum" {
		t.Errorf("claims %v", c)
	}
	if sub, _ := c["sub"].(string); len(sub) != 32 {
		t.Errorf("sub %v", c["sub"])
	}
	if g := c["groups"].([]any); len(g) != 1 || g[0] != UserGroup {
		t.Errorf("a Member's groups: %v", g)
	}
	authTime := float64(f.session.AuthTime.Unix())
	if c["auth_time"] != authTime {
		t.Errorf("auth_time %v, want %v", c["auth_time"], authTime)
	}

	// Userinfo.
	r := httptest.NewRequest(http.MethodGet, PathUserInfo, nil)
	r.Header.Set("Authorization", "Bearer "+tk.Access)
	uw := httptest.NewRecorder()
	f.p.UserInfo(uw, r)
	if uw.Code != 200 || !strings.Contains(uw.Body.String(), `"preferred_username":"mum"`) {
		t.Errorf("userinfo %d %s", uw.Code, uw.Body)
	}

	// Refresh, with the secret in the form this time; the old refresh token
	// then doesn't work.
	w = token(f, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.Refresh}}, false)
	if w.Code != 200 {
		t.Fatalf("refresh %d %s", w.Code, w.Body)
	}
	if again := token(f, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.Refresh}}, true); again.Code != 400 {
		t.Errorf("a used refresh token: %d", again.Code)
	}
	// A refreshed ID token keeps when they signed in (OIDC Core §12.2).
	if c := verify(t, f, decode(t, w).ID); c["auth_time"] != authTime || c["sub"] != verify(t, f, tk.ID)["sub"] {
		t.Errorf("refreshed claims %v", c)
	}

	// Losing access stops refreshing and userinfo.
	tk2 := decode(t, w)
	f.st.SetServiceAccess(context.Background(), "mealie", store.Access{Chosen: true})
	if w := token(f, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk2.Refresh}}, true); w.Code != 400 {
		t.Errorf("refresh without access: %d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest(http.MethodGet, PathUserInfo, nil)
	r.Header.Set("Authorization", "Bearer "+tk2.Access)
	uw = httptest.NewRecorder()
	f.p.UserInfo(uw, r)
	if uw.Code != 401 {
		t.Errorf("userinfo without access: %d", uw.Code)
	}
}

// A code works once; trying it again revokes the tokens it gave, as it
// may have been stolen (RFC 6749 §4.1.2).
func TestCodeReuse(t *testing.T) {
	f := newFixture(t)
	code := codeFrom(t, must(authorize(t, f, authQuery())))
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback}}
	tk := decode(t, token(f, form, true))
	userinfo := func() int {
		r := httptest.NewRequest(http.MethodGet, PathUserInfo, nil)
		r.Header.Set("Authorization", "Bearer "+tk.Access)
		w := httptest.NewRecorder()
		f.p.UserInfo(w, r)
		return w.Code
	}
	if userinfo() != 200 {
		t.Fatal("the access token doesn't work")
	}
	if w := token(f, form, true); w.Code != 400 || decode(t, w).Error != "invalid_grant" {
		t.Errorf("code again: %d %s", w.Code, w.Body)
	}
	if code := userinfo(); code != 401 {
		t.Errorf("access token after the code was reused: %d", code)
	}
	if w := token(f, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tk.Refresh}}, true); w.Code != 400 {
		t.Errorf("refresh token after the code was reused: %d", w.Code)
	}
	// Still refused after that, and long after.
	if w := token(f, form, true); w.Code != 400 {
		t.Errorf("code a third time: %d", w.Code)
	}
	later := time.Now().Add(2 * CodeLifetime)
	f.p.Now = func() time.Time { return later }
	if w := token(f, form, true); w.Code != 400 {
		t.Errorf("code once expired: %d", w.Code)
	}
}

// An app can ask for the password to be typed again: prompt=login always,
// max_age once that long has passed since it was.
func TestSignInAgain(t *testing.T) {
	f := newFixture(t)
	s := f.session
	s.AuthTime = time.Now().Add(-10 * time.Minute)
	for _, c := range []struct {
		q    url.Values
		want bool
	}{
		{authQuery(), false},
		{authQuery("prompt", "login"), true},
		{authQuery("prompt", "consent login"), true},
		{authQuery("prompt", "consent"), false},
		{authQuery("max_age", "0"), true},
		{authQuery("max_age", "60"), true},
		{authQuery("max_age", "3600"), false},
	} {
		req, err := f.p.ParseAuthorize(c.q)
		if err != nil {
			t.Fatalf("%v: %v", c.q, err.Message)
		}
		if got := req.SignInAgain(s, time.Now()); got != c.want {
			t.Errorf("prompt=%q max_age=%q: %v", c.q.Get("prompt"), c.q.Get("max_age"), got)
		}
	}
}

func must(s string, err *AuthError) string {
	if err != nil {
		panic(err.Message)
	}
	return s
}

// What's refused, and how: never a code for an unknown address.
func TestAuthorizeRefusals(t *testing.T) {
	f := newFixture(t)
	for name, q := range map[string]url.Values{
		"unknown app":      authQuery("client_id", "evil"),
		"walled service":   authQuery("client_id", "stirling-pdf"),
		"another address":  authQuery("redirect_uri", "https://evil.example/login"),
		"a longer address": authQuery("redirect_uri", callback+"/x"),
	} {
		if _, err := f.p.ParseAuthorize(q); err == nil || err.Redirect != "" {
			t.Errorf("%s: %+v", name, err)
		}
	}
	for name, q := range map[string]url.Values{
		"implicit":        authQuery("response_type", "token"),
		"no openid":       authQuery("scope", "profile"),
		"plain challenge": authQuery("code_challenge", "abc", "code_challenge_method", "plain"),
		"none and login":  authQuery("prompt", "none login"),
		"max_age words":   authQuery("max_age", "an hour"),
		"max_age < 0":     authQuery("max_age", "-1"),
	} {
		_, err := f.p.ParseAuthorize(q)
		if err == nil || !strings.HasPrefix(err.Redirect, callback+"?") || !strings.Contains(err.Redirect, "state=st8") {
			t.Errorf("%s: %+v", name, err)
		}
	}
}

// PKCE: the verifier must match; a wrong secret or redirect is refused.
func TestTokenChecks(t *testing.T) {
	f := newFixture(t)
	verifier := strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	code := codeFrom(t, must(authorize(t, f, authQuery("code_challenge", challenge, "code_challenge_method", "S256"))))
	if w := token(f, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback},
		"code_verifier": {strings.Repeat("w", 50)}}, true); w.Code != 400 {
		t.Errorf("wrong verifier: %d", w.Code)
	}
	// The failed try used the code up.
	code = codeFrom(t, must(authorize(t, f, authQuery("code_challenge", challenge, "code_challenge_method", "S256"))))
	if w := token(f, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback},
		"code_verifier": {verifier}}, true); w.Code != 200 {
		t.Errorf("right verifier: %d %s", w.Code, w.Body)
	}

	code = codeFrom(t, must(authorize(t, f, authQuery())))
	r := httptest.NewRequest(http.MethodPost, PathToken, strings.NewReader(url.Values{"grant_type": {"authorization_code"},
		"code": {code}, "redirect_uri": {callback}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.SetBasicAuth("mealie", "wrong")
	w := httptest.NewRecorder()
	f.p.Token(w, r)
	if w.Code != 401 || decode(t, w).Error != "invalid_client" {
		t.Errorf("wrong secret: %d %s", w.Code, w.Body)
	}
	if w := token(f, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback + "x"}}, true); w.Code != 400 {
		t.Errorf("another redirect: %d", w.Code)
	}

	// A code is no good once too old.
	code = codeFrom(t, must(authorize(t, f, authQuery())))
	later := time.Now().Add(2 * CodeLifetime)
	f.p.Now = func() time.Time { return later }
	if w := token(f, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback}}, true); w.Code != 400 {
		t.Errorf("old code: %d", w.Code)
	}
}

// An app is told only what its scopes ask for, in the ID token and from
// userinfo alike.
func TestClaimsByScope(t *testing.T) {
	f := newFixture(t)
	for scope, want := range map[string]string{
		"openid":                      "sub",
		"openid email":                "email email_verified sub",
		"openid profile":              "name preferred_username sub",
		"openid groups":               "groups sub",
		"openid email profile groups": "email email_verified groups name preferred_username sub",
	} {
		code := codeFrom(t, must(authorize(t, f, authQuery("scope", scope))))
		tk := decode(t, token(f, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {callback}}, true))
		var person []string
		for k := range verify(t, f, tk.ID) {
			if !slices.Contains([]string{"iss", "aud", "azp", "iat", "exp", "auth_time", "nonce"}, k) {
				person = append(person, k)
			}
		}
		slices.Sort(person)
		if got := strings.Join(person, " "); got != want {
			t.Errorf("%q: ID token has %s", scope, got)
		}
		r := httptest.NewRequest(http.MethodGet, PathUserInfo, nil)
		r.Header.Set("Authorization", "Bearer "+tk.Access)
		w := httptest.NewRecorder()
		f.p.UserInfo(w, r)
		var info map[string]any
		json.Unmarshal(w.Body.Bytes(), &info)
		keys := slices.Sorted(maps.Keys(info))
		if got := strings.Join(keys, " "); got != want {
			t.Errorf("%q: userinfo has %s", scope, got)
		}
	}
}

// Owners and Admins are in the admins group.
func TestAdminsGroup(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, _ := f.st.CreateUser(ctx, store.User{Username: "robin", PasswordHash: "x", Role: "admin"})
	robin, _ := f.st.UserByID(ctx, id)
	c, err := f.p.claims(ctx, robin, allScopes)
	if err != nil {
		t.Fatal(err)
	}
	if g := c["groups"].([]string); len(g) != 2 || g[1] != AdminGroup {
		t.Errorf("an Admin's groups: %v", g)
	}
	if _, ok := c["email"]; ok {
		t.Error("no email, no email claim")
	}
	if c["name"] != "robin" {
		t.Errorf("name without a full name: %v", c["name"])
	}
}

// The wall: no cookie sends a page load to sign in; the hand-off sets the
// service's cookie on its address only; then Caddy is told who it is.
func TestForwardAuth(t *testing.T) {
	f := newFixture(t)
	check := func(cookie, host, method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, PathCheck+"?service=stirling-pdf", nil)
		r.Header.Set("X-Forwarded-Host", host)
		r.Header.Set("X-Forwarded-Uri", "/merge?x=1")
		r.Header.Set("X-Forwarded-Method", method)
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: HostCookie, Value: cookie})
		}
		w := httptest.NewRecorder()
		f.p.Check(w, r)
		return w
	}
	w := check("", "pdf.smiths.example", "GET")
	loc := w.Header().Get("Location")
	if w.Code != 302 || !strings.HasPrefix(loc, issuer+PathStart+"?") {
		t.Fatalf("no cookie: %d %q", w.Code, loc)
	}
	if w := check("", "pdf.smiths.example", "POST"); w.Code != 401 {
		t.Errorf("no cookie, a POST: %d", w.Code)
	}
	if w := check("", "login.smiths.example", "GET"); w.Code != 403 {
		t.Errorf("asked about another address: %d", w.Code)
	}

	u, _ := url.Parse(loc)
	req, ok := f.p.ParseStart(u.Query())
	if !ok || req.Back != "https://pdf.smiths.example/merge?x=1" {
		t.Fatalf("start %+v %v", req, ok)
	}
	// Somewhere else to go back to is replaced by the service's own address.
	if evil, _ := f.p.ParseStart(url.Values{"service": {"stirling-pdf"}, "rd": {"https://evil.example/"}}); evil.Back != "https://pdf.smiths.example/" {
		t.Errorf("open redirect: %q", evil.Back)
	}
	to, err := f.p.Handoff(req, f.session)
	if err != nil || !strings.HasPrefix(to, "https://pdf.smiths.example"+PathCallback+"?code=") {
		t.Fatalf("%q %v", to, err)
	}
	cb, _ := url.Parse(to)

	// The code at another address doesn't work (and is used up).
	r := httptest.NewRequest(http.MethodGet, cb.RequestURI(), nil)
	r.Host = "recipes.smiths.example"
	cw := httptest.NewRecorder()
	f.p.Callback(cw, r)
	if cw.Code != 400 {
		t.Errorf("callback at another address: %d", cw.Code)
	}
	to, _ = f.p.Handoff(req, f.session)
	cb, _ = url.Parse(to)
	r = httptest.NewRequest(http.MethodGet, cb.RequestURI(), nil)
	r.Host = "pdf.smiths.example"
	cw = httptest.NewRecorder()
	f.p.Callback(cw, r)
	if cw.Code != 302 || cw.Header().Get("Location") != "https://pdf.smiths.example/merge?x=1" {
		t.Fatalf("callback %d %q", cw.Code, cw.Header().Get("Location"))
	}
	ck := cw.Result().Cookies()
	if len(ck) != 1 || ck[0].Name != HostCookie || ck[0].Domain != "" || !ck[0].HttpOnly || !ck[0].Secure || !ck[0].Expires.IsZero() {
		t.Fatalf("cookie %+v", ck)
	}
	// Used once.
	r = httptest.NewRequest(http.MethodGet, cb.RequestURI(), nil)
	r.Host = "pdf.smiths.example"
	cw = httptest.NewRecorder()
	f.p.Callback(cw, r)
	if cw.Code != 400 {
		t.Errorf("callback twice: %d", cw.Code)
	}

	w = check(ck[0].Value, "pdf.smiths.example", "GET")
	if w.Code != 200 || w.Header().Get("Remote-User") != "mum" || w.Header().Get("Remote-Groups") != "users" ||
		w.Header().Get("Remote-Email") != "margaret@smith.family" || w.Header().Get("Remote-Name") != "Margaret Smith" {
		t.Errorf("signed in: %d %v", w.Code, w.Header())
	}
	// Losing access, then signing out, each stop it.
	f.st.SetServiceAccess(context.Background(), "stirling-pdf", store.Access{Chosen: true})
	if w := check(ck[0].Value, "pdf.smiths.example", "GET"); w.Code != 302 {
		t.Errorf("without access: %d", w.Code)
	}
	f.st.SetServiceAccess(context.Background(), "stirling-pdf", store.Access{})
	f.st.DeleteSession(context.Background(), "sess-hash")
	if w := check(ck[0].Value, "pdf.smiths.example", "GET"); w.Code != 302 {
		t.Errorf("signed out: %d", w.Code)
	}
}

// The key is kept: loading it again gives the same one.
func TestKeyKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "k.pem")
	a, err := LoadKey(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadKey(path)
	if err != nil || a.ID != b.ID {
		t.Fatalf("%v %v %v", a.ID, b, err)
	}
}

// The discovery document names this provider's endpoints.
func TestDiscovery(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	f.p.Discovery(w, httptest.NewRequest(http.MethodGet, PathDiscovery, nil))
	body, _ := io.ReadAll(w.Body)
	var d map[string]any
	json.Unmarshal(body, &d)
	if d["issuer"] != issuer || d["token_endpoint"] != issuer+PathToken || d["jwks_uri"] != issuer+PathJWKS {
		t.Errorf("%s", body)
	}
	// Left out, request_uri_parameter_supported would mean true.
	for _, k := range []string{"request_parameter_supported", "request_uri_parameter_supported", "claims_parameter_supported"} {
		if v, ok := d[k]; !ok || v != false {
			t.Errorf("%s: %v", k, v)
		}
	}
}
