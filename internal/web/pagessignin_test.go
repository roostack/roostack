package web

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/store"
)

const pagesHost = "roost.lab.example.com"

// followTrip follows a trip from Roostack's pages to the sign-in page and
// back: it returns the sign-in page's answer to Roostack's request.
func followTrip(t *testing.T, h *harness, srv *httptest.Server, c *http.Client, path string) (*http.Response, string) {
	t.Helper()
	resp, _ := call(t, h.srv, c, "GET", pagesHost, path, nil)
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, pathPagesStart) {
		t.Fatalf("%s without a session: %d %q", path, resp.StatusCode, loc)
	}
	resp, _ = call(t, h.srv, c, "GET", pagesHost, loc, nil)
	loc = resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "https://"+signInHost+signInPagesPath+"?") {
		t.Fatalf("start: %d %q", resp.StatusCode, loc)
	}
	return call(t, srv, c, "GET", signInHost, strings.TrimPrefix(loc, "https://"+signInHost), nil)
}

// backToPages follows the sign-in page's code to Roostack's pages.
func backToPages(t *testing.T, h *harness, c *http.Client, resp *http.Response) *http.Response {
	t.Helper()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "https://"+pagesHost+pathPagesCallback+"?code=") {
		t.Fatalf("back to Roostack's pages: %d %q", resp.StatusCode, loc)
	}
	resp, _ = call(t, h.srv, c, "GET", pagesHost, strings.TrimPrefix(loc, "https://"+pagesHost), nil)
	return resp
}

// One sign-in (SPEC.md §5.2): Roostack's pages at their own address send
// someone to the sign-in page and back, to the page they asked for, with a
// session that ends with the sign-in; a Member's isn't signed out for
// inactivity; signing out of either ends both.
func TestPagesSignIn(t *testing.T) {
	h := newHarness(t, "")
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	ctx := context.Background()
	addPerson(t, h.store, "mum", "member")

	resp, _ := followTrip(t, h, srv, c, "/account")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusSeeOther || loc.Path != "/sign-in" {
		t.Fatalf("not signed in: %d %q", resp.StatusCode, loc)
	}
	if _, body := call(t, srv, c, "GET", signInHost, loc.RequestURI(), nil); !strings.Contains(body, "to continue to") || !strings.Contains(body, "Roostack") {
		t.Error("the sign-in page doesn't say it's for Roostack")
	}
	resp, _ = call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"mum"}, "password": {"a long password"},
		"next": {loc.Query().Get("next")}, "remember": {"on"}})
	resp, _ = call(t, srv, c, "GET", signInHost, resp.Header.Get("Location"), nil)
	code := resp.Header.Get("Location")
	resp = backToPages(t, h, c, resp)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/account" {
		t.Fatalf("signed in: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	var session *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == sessionCookie {
			session = ck
		}
	}
	if session == nil || session.Expires.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("session cookie %+v: should last as long as the sign-in", session)
	}
	if resp, _ := call(t, h.srv, c, "GET", pagesHost, "/", nil); resp.StatusCode != 200 {
		t.Fatalf("signed in to Roostack's pages: %d", resp.StatusCode)
	}
	// A code works once.
	c.Jar.(*plainJar).cookies[pagesStateCookie] = &http.Cookie{Name: pagesStateCookie, Value: "x"}
	delete(c.Jar.(*plainJar).cookies, sessionCookie)
	if resp, _ := call(t, h.srv, c, "GET", pagesHost, strings.TrimPrefix(code, "https://"+pagesHost), nil); resp.StatusCode != 400 {
		t.Errorf("a code used twice: %d", resp.StatusCode)
	}
	c.Jar.(*plainJar).cookies[sessionCookie] = session

	// Not signed out for inactivity: a Member's lasts as long as the sign-in.
	h.store.TouchSession(ctx, authkit.HashToken(session.Value), time.Now().Add(-5*time.Hour))
	if resp, _ := call(t, h.srv, c, "GET", pagesHost, "/", nil); resp.StatusCode != 200 {
		t.Errorf("a Member's session after 5 hours: %d", resp.StatusCode)
	}
	// The sign-in page lists Roostack's pages.
	if _, body := call(t, srv, c, "GET", signInHost, "/", nil); !strings.Contains(body, "https://"+pagesHost+"/") {
		t.Error("the sign-in page's home doesn't list Roostack's pages")
	}

	// Signing out of the sign-in page ends both.
	call(t, srv, c, "POST", signInHost, sso.PathSignOut, url.Values{})
	if resp, _ := call(t, h.srv, c, "GET", pagesHost, "/", nil); resp.StatusCode != http.StatusSeeOther {
		t.Errorf("Roostack's pages after signing out of the sign-in page: %d", resp.StatusCode)
	}

	// Signing out of Roostack's pages ends both too, and says so.
	resp, _ = followTrip(t, h, srv, c, "/")
	loc, _ = url.Parse(resp.Header.Get("Location"))
	resp, _ = call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"mum"}, "password": {"a long password"}, "next": {loc.Query().Get("next")}})
	signIn := authkit.HashToken(c.Jar.(*plainJar).cookies[signInCookie].Value)
	resp, _ = call(t, srv, c, "GET", signInHost, resp.Header.Get("Location"), nil)
	backToPages(t, h, c, resp)
	resp, _ = call(t, h.srv, c, "POST", pagesHost, "/logout", url.Values{})
	if loc := resp.Header.Get("Location"); loc != "https://"+signInHost+sso.PathSignOut {
		t.Errorf("signing out of Roostack's pages: %q", loc)
	}
	if _, err := h.store.SignInSession(ctx, signIn); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the sign-in after signing out of Roostack's pages: %v", err)
	}
}

// Owners and Admins keep Roostack's idle limit: after it, the sign-in
// page asks for their two-factor code alone (or a password without one).
func TestPagesSignInConfirm(t *testing.T) {
	h := newHarness(t, "")
	srv, c := withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	ctx := context.Background()
	id := addPerson(t, h.store, "sam", "owner")
	secret, _ := authkit.NewTOTPSecret()
	h.store.SetTOTP(ctx, id, secret, 0)

	resp, _ := followTrip(t, h, srv, c, "/")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	next := loc.Query().Get("next")
	call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"sam"}, "password": {"a long password"}, "next": {next}})
	now, _ := authkit.TOTPCode(secret, nowFunc())
	resp, _ = call(t, srv, c, "POST", signInHost, "/sign-in/code", url.Values{"code": {now}, "next": {next}})
	resp, _ = call(t, srv, c, "GET", signInHost, resp.Header.Get("Location"), nil)
	// Just signed in: straight back.
	if resp := backToPages(t, h, c, resp); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("signed in: %d", resp.StatusCode)
	}

	// An hour later, past the idle limit.
	session := c.Jar.(*plainJar).cookies[sessionCookie].Value
	signIn := authkit.HashToken(c.Jar.(*plainJar).cookies[signInCookie].Value)
	h.store.TouchSession(ctx, authkit.HashToken(session), time.Now().Add(-time.Hour))
	h.store.SignedInAgain(ctx, signIn, time.Now().Add(-time.Hour))
	resp, body := followTrip(t, h, srv, c, "/backups")
	if resp.StatusCode != 200 || !strings.Contains(body, "Confirm it's you") || !strings.Contains(body, `name="code"`) ||
		strings.Contains(body, `name="password"`) || !strings.Contains(body, "been away") {
		t.Fatalf("confirm step: %d\n%s", resp.StatusCode, body)
	}
	trip := url.Values{"state": {between(body, `name="state" value="`, `"`)}, "next": {between(body, `name="next" value="`, `"`)}}
	if trip.Get("next") != "/backups" {
		t.Errorf("next %q", trip.Get("next"))
	}
	wrong := url.Values{"code": {wrongCode(secret)}}
	for k, v := range trip {
		wrong[k] = v
	}
	if resp, body := call(t, srv, c, "POST", signInHost, signInConfirmPath, wrong); resp.StatusCode != 401 || !strings.Contains(body, "That code didn") {
		t.Errorf("wrong code: %d", resp.StatusCode)
	}
	h.store.SetTOTP(ctx, id, secret, 0) // the code just used works again
	right := url.Values{"code": {now}}
	for k, v := range trip {
		right[k] = v
	}
	resp, _ = call(t, srv, c, "POST", signInHost, signInConfirmPath, right)
	if resp := backToPages(t, h, c, resp); resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/backups" {
		t.Fatalf("after the code: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	// The sign-in for apps carried on throughout.
	if _, err := h.store.SignInSession(ctx, signIn); err != nil {
		t.Errorf("the sign-in after confirming: %v", err)
	}
	// Audited.
	entries, _ := h.store.RecentAudit(ctx, 20)
	var got []string
	for _, e := range entries {
		got = append(got, e.Action+": "+e.Detail)
	}
	all := strings.Join(got, "\n")
	for _, want := range []string{"login: through the sign-in page (confirmed with a two-factor code)", "failed sign-in: a wrong code"} {
		if !strings.Contains(all, want) {
			t.Errorf("audit log missing %q:\n%s", want, all)
		}
	}

	// An Admin without two-factor types their password.
	addPerson(t, h.store, "ali", "admin")
	c2 := &http.Client{Jar: &plainJar{}, CheckRedirect: c.CheckRedirect}
	resp, _ = followTrip(t, h, srv, c2, "/")
	loc, _ = url.Parse(resp.Header.Get("Location"))
	call(t, srv, c2, "POST", signInHost, "/sign-in", url.Values{"username": {"ali"}, "password": {"a long password"}, "next": {loc.Query().Get("next")}})
	h.store.SignedInAgain(ctx, authkit.HashToken(c2.Jar.(*plainJar).cookies[signInCookie].Value), time.Now().Add(-time.Hour))
	if resp, body := call(t, srv, c2, "GET", signInHost, loc.Query().Get("next"), nil); resp.StatusCode != 200 || !strings.Contains(body, `name="password"`) {
		t.Errorf("an Admin without two-factor: %d", resp.StatusCode)
	}
}

// Someone the sign-in page turns away (a temporary password) goes on to
// Roostack's pages for that step alone; the code must come back to the
// browser that asked for it; and Roostack's own login page stays.
func TestPagesSignInEdges(t *testing.T) {
	h := newHarness(t, "")
	fs := &fakeSSO{on: map[string]bool{"mealie": true}}
	srv, c := withSignIn(t, h, fs)
	ctx := context.Background()
	jo := addPerson(t, h.store, "jo", "member")
	u, _ := h.store.UserByID(ctx, jo)
	h.store.SetPassword(ctx, u.Username, u.PasswordHash, true)

	resp, _ := followTrip(t, h, srv, c, "/")
	loc, _ := url.Parse(resp.Header.Get("Location"))
	resp, _ = call(t, srv, c, "POST", signInHost, "/sign-in", url.Values{"username": {"jo"}, "password": {"a long password"}, "next": {loc.Query().Get("next")}})
	if resp := backToPages(t, h, c, resp); resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("temporary password: %d", resp.StatusCode)
	}
	if _, ok := c.Jar.(*plainJar).cookies[signInCookie]; ok {
		t.Error("signed in to the sign-in page with a temporary password")
	}
	if resp, _ := call(t, h.srv, c, "GET", pagesHost, "/", nil); resp.Header.Get("Location") != "/account" {
		t.Errorf("Roostack's pages with a temporary password: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// Someone else's code, in a browser that didn't start the trip.
	addPerson(t, h.store, "eve", "member")
	eve := &http.Client{Jar: &plainJar{}, CheckRedirect: c.CheckRedirect}
	resp, _ = followTrip(t, h, srv, eve, "/")
	loc, _ = url.Parse(resp.Header.Get("Location"))
	resp, _ = call(t, srv, eve, "POST", signInHost, "/sign-in", url.Values{"username": {"eve"}, "password": {"a long password"}, "next": {loc.Query().Get("next")}})
	resp, _ = call(t, srv, eve, "GET", signInHost, resp.Header.Get("Location"), nil)
	victim := &http.Client{Jar: &plainJar{}, CheckRedirect: c.CheckRedirect}
	if resp := backToPages(t, h, victim, resp); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a code in another browser: %d", resp.StatusCode)
	}

	// Roostack's own login page: at /login?here, and wherever the sign-in
	// page isn't the way in.
	if resp, body := call(t, h.srv, victim, "GET", pagesHost, "/login?here", nil); resp.StatusCode != 200 || !strings.Contains(body, "sign in with the lab") {
		t.Errorf("own login page: %d", resp.StatusCode)
	}
	if resp, _ := call(t, h.srv, victim, "GET", "localhost:18481", "/", nil); resp.Header.Get("Location") != "/login" {
		t.Errorf("through the tunnel: %q", resp.Header.Get("Location"))
	}
	// Not until Caddy serves the sign-in page: an apply turning single
	// sign-on on reloads Caddy last.
	served := false
	h.server.SignInServed = func(context.Context) bool { return served }
	h.server.served = servedCheck{}
	if resp, _ := call(t, h.srv, victim, "GET", pagesHost, "/", nil); resp.Header.Get("Location") != "/login" {
		t.Errorf("before Caddy serves the sign-in page: %q", resp.Header.Get("Location"))
	}
	if resp, body := call(t, h.srv, victim, "GET", pagesHost, "/login", nil); resp.StatusCode != 200 || strings.Contains(body, "sign in with the lab") {
		t.Errorf("own login page before Caddy serves the sign-in page: %d", resp.StatusCode)
	}
	served = true
	if resp, _ := call(t, h.srv, victim, "GET", pagesHost, "/", nil); resp.Header.Get("Location") != "/login" {
		t.Errorf("the answer isn't kept a few seconds: %q", resp.Header.Get("Location"))
	}
	h.server.served = servedCheck{}
	if resp, _ := call(t, h.srv, victim, "GET", pagesHost, "/", nil); !strings.HasPrefix(resp.Header.Get("Location"), pathPagesStart) {
		t.Errorf("once Caddy serves the sign-in page: %q", resp.Header.Get("Location"))
	}
	fs.on["mealie"] = false
	if resp, _ := call(t, h.srv, victim, "GET", pagesHost, "/", nil); resp.Header.Get("Location") != "/login" {
		t.Errorf("without single sign-on: %q", resp.Header.Get("Location"))
	}

	for _, next := range []string{"https://evil.example/", "//evil.example/", "/\\evil.example", "/login/sign-in"} {
		if got := localNext(next); got != "/" {
			t.Errorf("localNext(%q) = %q", next, got)
		}
	}
}

// Roostack asks Caddy whether it serves the sign-in page: yes only when
// the answer comes from its own sign-in listener.
func TestCaddyServesSignIn(t *testing.T) {
	h := newHarness(t, "")
	withSignIn(t, h, &fakeSSO{on: map[string]bool{"mealie": true}})
	ctx := context.Background()
	caddy := httptest.NewTLSServer(h.server.SignInHandler()) // as Caddy forwards login.<domain>
	t.Cleanup(caddy.Close)
	if !h.server.CaddyServesSignIn(caddy.Listener.Addr().String())(ctx) {
		t.Error("Caddy serving the sign-in page: no")
	}
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Caddy's own page"))
	}))
	t.Cleanup(other.Close)
	if h.server.CaddyServesSignIn(other.Listener.Addr().String())(ctx) {
		t.Error("Caddy without the sign-in page: yes")
	}
	other.Close()
	if h.server.CaddyServesSignIn(other.Listener.Addr().String())(ctx) {
		t.Error("no Caddy: yes")
	}
}

// between is the text of s between before and after.
func between(s, before, after string) string {
	_, rest, _ := strings.Cut(s, before)
	v, _, _ := strings.Cut(rest, after)
	return v
}
