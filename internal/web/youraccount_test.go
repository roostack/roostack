package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
)

// Your account at Roostack's own address, as for a lab without an address
// (SPEC.md §5.3): each tab, and what its forms save.
func TestYourAccountTabs(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	ctx := context.Background()
	h.server.Services = func(context.Context) ([]ServiceView, error) {
		return []ServiceView{{Name: "vaultwarden", Title: "Vaultwarden", Status: "running", URLs: []string{"https://vault.lab.example.com"}},
			{Name: "mealie", Title: "Mealie", Status: StatusOff, URLs: []string{"https://recipes.lab.example.com"}}}, nil
	}

	code, body := h.get(acctPagesServices)
	if code != 200 || !strings.Contains(body, "<title>Your services") || !strings.Contains(body, `href="https://vault.lab.example.com"`) ||
		!strings.Contains(body, "Its own sign-in") || !strings.Contains(body, "Turned off for now") || strings.Contains(body, "Run the lab") ||
		!strings.Contains(body, `action="/logout"`) || strings.Contains(body, "<script") {
		t.Errorf("Services: %d\n%s", code, body)
	}
	for _, tab := range []string{"/account", "/account/security", "/account/notifications"} {
		if code, body := h.get(tab); code != 200 || !strings.Contains(body, `aria-current="page"`) || strings.Contains(body, `class="sidebar"`) {
			t.Errorf("%s: %d", tab, code)
		}
	}

	// Account: name and email; a bad address is refused.
	if code, _ := h.post("/account", url.Values{"name": {"  Jo   Smith "}, "email": {"jo@example.com"}}, h.srv.URL); code != 200 {
		t.Errorf("details: %d", code)
	}
	u, _ := h.store.UserByUsername(ctx, "u")
	if u.Name != "Jo Smith" || u.Email != "jo@example.com" {
		t.Errorf("saved %q %q", u.Name, u.Email)
	}
	if code, body := h.post("/account", url.Values{"email": {"nope"}}, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "Enter an email address") {
		t.Errorf("a bad address: %d", code)
	}
	if code, _ := h.post("/account", url.Values{"name": {strings.Repeat("x", 101)}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("a long name: %d", code)
	}
	h.post("/account/mode", url.Values{"mode": {"dark"}}, h.srv.URL)
	if _, body := h.get("/account"); !strings.Contains(body, `data-mode="dark"`) {
		t.Error("dark isn't applied")
	}

	// Notifications: a Member has no groups to choose, only their own
	// security emails.
	_, body = h.get("/account/notifications")
	if !strings.Contains(body, "jo@example.com") || strings.Contains(body, `name="notify"`) || !strings.Contains(body, "Security emails about your own account") {
		t.Errorf("a Member's notifications:\n%s", body)
	}
	h.post("/account/notifications", url.Values{"notify": {"problems"}}, h.srv.URL)
	if u, _ := h.store.UserByUsername(ctx, "u"); u.Notify != "none" {
		t.Errorf("chosen %q: a Member can't have Problems", u.Notify)
	}

	// Security: the password, with every other session signed out.
	other := otherSession(t, h, u.ID, "Safari on iPhone")
	if code, body := h.post("/account/password", url.Values{"current": {"wrong"}, "password": {"another long password"},
		"confirm": {"another long password"}}, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "current password is wrong") {
		t.Errorf("a wrong password: %d", code)
	}
	if _, body := h.post("/account/password", url.Values{"current": {"a long password"}, "password": {"another long password"},
		"confirm": {"another long password"}}, h.srv.URL); !strings.Contains(body, "Password changed") {
		t.Error("the password wasn't changed")
	}
	if _, err := h.store.UserSessions(ctx, u.ID); err != nil || sessionExists(t, h, u.ID, other) {
		t.Error("the other session wasn't signed out")
	}
	if code, _ := h.get("/account/security"); code != 200 {
		t.Errorf("this browser was signed out too: %d", code)
	}
}

// otherSession signs u in on another browser, and returns its hash.
func otherSession(t *testing.T, h *harness, userID int64, device string) string {
	t.Helper()
	token, _ := authkit.NewToken()
	hash := authkit.HashToken(token)
	ctx := context.Background()
	if err := h.store.CreateSessionAt(ctx, "", hash, userID, time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	h.store.SetSessionDevice(ctx, hash, device)
	return hash
}

func sessionExists(t *testing.T, h *harness, userID int64, hash string) bool {
	t.Helper()
	list, err := h.store.UserSessions(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.Hash == hash {
			return true
		}
	}
	return false
}

// Where you're signed in: each device, Sign out for one, and everywhere
// else; someone else's session can't be signed out from here.
func TestYourAccountSessions(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	u, _ := h.store.UserByUsername(context.Background(), "u")
	phone := otherSession(t, h, u.ID, "Safari on iPhone")
	laptop := otherSession(t, h, u.ID, "Chrome on Windows")
	eve := addPerson(t, h.store, "eve", auth.RoleMember)
	theirs := otherSession(t, h, eve, "Firefox on Linux")

	_, body := h.get("/account/security")
	if !strings.Contains(body, "Safari on iPhone") || !strings.Contains(body, "Chrome on Windows") || !strings.Contains(body, "this device") ||
		strings.Contains(body, "Firefox on Linux") || !strings.Contains(body, "Sign out everywhere else") {
		t.Errorf("where you're signed in:\n%s", body)
	}
	h.post("/account/sign-out", url.Values{"session": {theirs}}, h.srv.URL)
	if !sessionExists(t, h, eve, theirs) {
		t.Error("someone else's session was signed out")
	}
	h.post("/account/sign-out", url.Values{"session": {phone}}, h.srv.URL)
	if sessionExists(t, h, u.ID, phone) || !sessionExists(t, h, u.ID, laptop) {
		t.Error("signing out the phone")
	}
	h.post("/account/sign-out-others", nil, h.srv.URL)
	if sessionExists(t, h, u.ID, laptop) {
		t.Error("signing out everywhere else left the laptop")
	}
	if code, body := h.get("/account/security"); code != 200 || !strings.Contains(body, "this device") {
		t.Errorf("this browser was signed out: %d", code)
	}
}

// Two-factor in Your account: turned on with a code; moved to a new app
// only with the password too.
func TestYourAccountTwoFactor(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	ctx := context.Background()
	_, body := h.get("/account/two-factor")
	secret, _ := h.enrollTOTP(body)
	if userSecret(h, "u") != secret {
		t.Fatal("two-factor isn't on")
	}
	_, body = h.get("/account/two-factor")
	if !strings.Contains(body, "Use a new authenticator app") || !strings.Contains(body, `name="current"`) {
		t.Fatalf("changing the app:\n%s", body)
	}
	fresh := totpSecretOf(t, body)
	code, _ := authkit.TOTPCode(fresh, time.Now())
	if c, _ := h.post("/account/two-factor", url.Values{"secret": {fresh}, "code": {code}, "current": {"wrong"}}, h.srv.URL); c != http.StatusBadRequest ||
		userSecret(h, "u") != secret {
		t.Errorf("a new app without the password: %d", c)
	}
	if _, body := h.post("/account/two-factor", url.Values{"secret": {fresh}, "code": {code}, "current": {"a long password"}}, h.srv.URL); !strings.Contains(body, "Your new authenticator app") ||
		userSecret(h, "u") != fresh {
		t.Error("the new app wasn't saved")
	}
	logs, _ := h.store.RecentAudit(ctx, 1)
	if len(logs) == 0 || logs[0].Action != "2fa changed" {
		t.Errorf("not audited: %+v", logs)
	}
}

// At my.<domain>: signing in first, then the page asked for; the sign-in's
// browser is listed; Roostack's own address sends people there.
func TestYourAccountAtSignIn(t *testing.T) {
	h := newHarness(t, "")
	fs := &fakeSSO{on: map[string]bool{"mealie": true}}
	srv, c := withSignIn(t, h, fs)
	addPerson(t, h.store, "mum", auth.RoleMember)

	resp, _ := call(t, srv, c, "GET", signInHost, "/account/security", nil)
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != "/sign-in?next=%2Faccount%2Fsecurity" {
		t.Fatalf("not signed in: %d %q", resp.StatusCode, loc)
	}
	req := url.Values{"username": {"mum"}, "password": {"a long password"}, "next": {"/account/security"}}
	resp, _ = call(t, srv, c, "POST", signInHost, "/sign-in", req)
	if loc := resp.Header.Get("Location"); loc != "/account/security" {
		t.Fatalf("after signing in: %q", loc)
	}
	_, body := call(t, srv, c, "GET", signInHost, "/account/security", nil)
	if !strings.Contains(body, "Where you're signed in") || !strings.Contains(body, "this device") || !strings.Contains(body, `action="/sign-out"`) ||
		!strings.Contains(body, `src="/look/`) && !strings.Contains(body, "brand-mark") {
		t.Errorf("Security at my.:\n%s", body)
	}
	if safeNext("//evil.example/account") != "/" || safeNext("/account/../x") == "" || safeNext("https://evil.example/account") != "/" {
		t.Error("safeNext lets another site through")
	}

	// Roostack's own address sends a page of Your account there, once
	// Caddy serves it.
	id := addPerson(t, h.store, "jo", auth.RoleMember)
	pages := &http.Client{Jar: &plainJar{cookies: map[string]*http.Cookie{sessionCookie: {Name: sessionCookie, Value: pagesSession(t, h, id)}}},
		CheckRedirect: c.CheckRedirect}
	for path, want := range map[string]string{"/account/notifications": "/account/notifications", acctPagesServices: "/"} {
		if resp, _ := call(t, h.srv, pages, "GET", pagesHost, path, nil); resp.Header.Get("Location") != "https://"+signInHost+want {
			t.Errorf("roost.%s: %d %q", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// A step Roostack asks for first stays here.
	u, _ := h.store.UserByID(context.Background(), id)
	h.store.SetPassword(context.Background(), u.Username, u.PasswordHash, true)
	pages.Jar.(*plainJar).cookies[sessionCookie].Value = pagesSession(t, h, id)
	if resp, body := call(t, h.srv, pages, "GET", pagesHost, "/account/security", nil); resp.StatusCode != 200 || !strings.Contains(body, "temporary password") {
		t.Errorf("a temporary password's page: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// pagesSession signs someone in to Roostack's own pages, and returns its
// cookie's value.
func pagesSession(t *testing.T, h *harness, userID int64) string {
	t.Helper()
	token, _ := authkit.NewToken()
	if err := h.store.CreateSessionAt(context.Background(), "", authkit.HashToken(token), userID, time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	return token
}
