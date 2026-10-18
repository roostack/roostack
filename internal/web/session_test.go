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
	"roostack.cloud/roostack/internal/store"
)

// sessionHash is the harness browser's session, as stored.
func sessionHash(t *testing.T, h *harness) string {
	t.Helper()
	u, _ := url.Parse(h.srv.URL)
	for _, c := range h.client.Jar.Cookies(u) {
		if c.Name == sessionCookie {
			return authkit.HashToken(c.Value)
		}
	}
	t.Fatal("no session cookie")
	return ""
}

// get with htmx's header: a part of a page refreshing itself.
func (h *harness) poll(path string) *http.Response {
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+path, nil)
	req.Header.Set("HX-Request", "true")
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// After 30 minutes without activity a session ends: a page refreshing
// itself is sent to the login page (which says why), and refreshing
// doesn't count as activity; opening a page does.
func TestIdleSignOut(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	ctx := context.Background()
	hash := sessionHash(t, h)
	seen := func() time.Time { _, at, _, _ := h.store.SessionInfo(ctx, hash); return at }

	h.store.TouchSession(ctx, hash, time.Now().Add(-20*time.Minute))
	before := seen()
	if resp := h.poll("/partials/home"); resp.Header.Get("HX-Redirect") != "" || !seen().Equal(before) {
		t.Errorf("a refresh isn't activity: redirect %q, seen %v → %v", resp.Header.Get("HX-Redirect"), before, seen())
	}
	if h.get("/services"); !seen().After(before) {
		t.Error("opening a page is activity")
	}

	// Expired: the next page opened goes to the login page, which says why.
	h.store.TouchSession(ctx, hash, time.Now().Add(-31*time.Minute))
	if _, body := h.get("/services"); !strings.Contains(body, "You were signed out after 30 minutes without activity") {
		t.Errorf("signed out: %s", body)
	}
	// A page refreshing itself is sent there too (htmx follows HX-Redirect).
	h.post("/login", url.Values{"username": {"u"}, "password": {"a long password"}}, h.srv.URL)
	h.store.TouchSession(ctx, sessionHash(t, h), time.Now().Add(-31*time.Minute))
	if resp := h.poll("/partials/home"); resp.Header.Get("HX-Redirect") != "/login?expired=1" {
		t.Errorf("expired: refresh goes to %q", resp.Header.Get("HX-Redirect"))
	}
}

// "Keep me signed in" lasts on this browser without the idle limit; the
// Owner sets the limit and whether it's offered.
func TestKeepMeSignedIn(t *testing.T) {
	h := newHarness(t, "")
	ctx := context.Background()
	hash, _ := authkit.HashPassword("a long password")
	h.store.CreateUser(ctx, storeUser("sam", hash, auth.RoleAdmin))
	if _, body := h.get("/login"); !strings.Contains(body, `name="remember"`) {
		t.Fatal("offered by default")
	}
	h.post("/login", url.Values{"username": {"sam"}, "password": {"a long password"}, "remember": {"on"}}, h.srv.URL)
	sh := sessionHash(t, h)
	if _, _, remember, err := h.store.SessionInfo(ctx, sh); err != nil || !remember {
		t.Fatalf("remembered: %v %v", remember, err)
	}
	h.store.TouchSession(ctx, sh, time.Now().Add(-5*time.Hour))
	if resp := h.poll("/partials/home"); resp.Header.Get("HX-Redirect") != "" {
		t.Error("no idle limit when kept signed in")
	}

	// The Owner's choices: never sign out, and don't offer it.
	ho, _ := loggedIn(t, auth.RoleOwner)
	ho.post("/settings/signing-in", url.Values{"idle": {"0"}}, ho.srv.URL)
	if ho.server.idleLimit(ctx) != 0 || ho.server.offerRemember(ctx) {
		t.Errorf("saved: %v %v", ho.server.idleLimit(ctx), ho.server.offerRemember(ctx))
	}
	if _, body := ho.get("/settings/people"); !strings.Contains(body, `<option value="0" selected>never`) {
		t.Error("the People page shows the choice")
	}
	if code, _ := ho.post("/settings/signing-in", url.Values{"idle": {"7"}}, ho.srv.URL); code != http.StatusBadRequest {
		t.Errorf("not a choice: %d", code)
	}
}

func storeUser(name, hash, role string) store.User {
	u := store.User{Username: name, PasswordHash: hash, Role: role}
	if role == auth.RoleOwner {
		u.Email = name + "@example.com" // every Owner has one (ownerNeedsEmail)
	}
	return u
}
