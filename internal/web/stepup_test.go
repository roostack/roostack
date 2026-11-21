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

// Actions that reach the whole server ask for the password, and a code
// with two-factor, then not again for five minutes in the same session
// (the security review of 2026-11-19, L4).
func TestStepUpActions(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	ctx := context.Background()
	_, body := h.get("/services/vaultwarden/remove")
	if !strings.Contains(body, `name="password" type="password"`) || !strings.Contains(body, `name="code"`) ||
		!strings.Contains(body, "Roostack will check it's you first: your password and a code") {
		t.Error("removing a service doesn't ask for the password and code")
	}
	if code, body := h.post("/services/vaultwarden/remove", url.Values{}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "That password or code didn&#39;t work") || len(fm.removed) != 0 {
		t.Errorf("removing without them: %d %v", code, fm.removed)
	}
	u, _ := h.store.UserByUsername(ctx, "u")
	good, _ := authkit.TOTPCode(u.TOTPSecret, nowFunc().Add(30*time.Second))
	h.post("/services/vaultwarden/remove", url.Values{"password": {"a long password"}, "code": {good}}, h.srv.URL)
	if len(fm.removed) != 1 {
		t.Fatalf("removing with them: %v", fm.removed)
	}
	// Within the grace: neither asked nor needed.
	if _, body := h.get("/catalog/add"); strings.Contains(body, `name="password"`) {
		t.Error("asked again within five minutes")
	}
	h.post("/services/vaultwarden/remove", url.Values{}, h.srv.URL)
	if len(fm.removed) != 2 {
		t.Errorf("within the grace: %v", fm.removed)
	}
	// After it: asked again.
	h.server.stepUps.mu.Lock()
	for k := range h.server.stepUps.confirmed {
		h.server.stepUps.confirmed[k] = time.Now().Add(-time.Second)
	}
	h.server.stepUps.mu.Unlock()
	if code, _ := h.post("/services/vaultwarden/remove", url.Values{}, h.srv.URL); code != http.StatusBadRequest || len(fm.removed) != 2 {
		t.Errorf("after the grace: %d %v", code, fm.removed)
	}
}

// An account without two-factor (an Admin's, while Roostack's pages
// aren't public) gives its password alone.
func TestStepUpWithoutTwoFactor(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	_, body := h.get("/services/vaultwarden/remove")
	if !strings.Contains(body, `name="password" type="password"`) || strings.Contains(body, `name="code"`) {
		t.Error("an Admin without two-factor is asked for a code")
	}
	if code, body := h.post("/services/vaultwarden/remove", url.Values{"password": {"not it"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "That password didn&#39;t work.") || len(fm.removed) != 0 {
		t.Errorf("a wrong password: %d", code)
	}
	h.post("/services/vaultwarden/remove", url.Values{"password": {"a long password"}}, h.srv.URL)
	if len(fm.removed) != 1 {
		t.Errorf("the right password: %v", fm.removed)
	}
}
