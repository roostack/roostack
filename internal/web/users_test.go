package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/setup"
	"github.com/mylab/mylab/internal/store"
)

func TestUsersPage(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.server.Setup = &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	ctx := context.Background()

	if _, body := h.get("/settings"); !strings.Contains(body, `href="/users"`) || !strings.Contains(body, "1 person can log in") {
		t.Error("settings should link to People")
	}

	// Add someone: a temporary password is shown once.
	for _, bad := range []string{"", "a b", "<script>", strings.Repeat("x", 33)} {
		if code, _ := h.post("/users", url.Values{"username": {bad}, "role": {"member"}}, h.srv.URL); code != http.StatusBadRequest {
			t.Errorf("username %q accepted", bad)
		}
	}
	code, body := h.post("/users", url.Values{"username": {"dad"}, "role": {"admin"}}, h.srv.URL)
	m := regexp.MustCompile(`<code class="secret">([^<]+)</code>`).FindStringSubmatch(body)
	if code != http.StatusOK || m == nil || !strings.Contains(body, "dad can now log in") {
		t.Fatalf("add: %d", code)
	}
	dad, _ := h.store.UserByUsername(ctx, "dad")
	if dad.Role != auth.RoleAdmin || !dad.MustChangePassword {
		t.Errorf("dad = %+v", dad)
	}
	if ok, _ := auth.VerifyPassword(dad.PasswordHash, m[1]); !ok {
		t.Error("the shown password doesn't work")
	}
	if code, _ := h.post("/users", url.Values{"username": {"Dad"}, "role": {"member"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Error("duplicate username accepted")
	}

	// Change a role, reset password and 2FA.
	path := fmt.Sprintf("/users/%d/", dad.ID)
	h.post(path+"role", url.Values{"role": {"member"}}, h.srv.URL)
	h.store.SetTOTP(ctx, dad.ID, "SECRET", 1)
	h.post(path+"reset-2fa", nil, h.srv.URL)
	_, body = h.post(path+"reset-password", nil, h.srv.URL)
	if dad, _ = h.store.UserByID(ctx, dad.ID); dad.Role != auth.RoleMember || dad.TOTPSecret != "" ||
		!strings.Contains(body, "New password for dad") {
		t.Errorf("after changes: %+v", dad)
	}

	// Not on yourself, and the last Owner stays.
	me, _ := h.store.UserByUsername(ctx, "u")
	if code, body := h.post(fmt.Sprintf("/users/%d/role", me.ID), url.Values{"role": {"member"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "can&#39;t change your own account") {
		t.Errorf("changing own role: %d", code)
	}
	if u, _ := h.store.UserByID(ctx, me.ID); u.Role != auth.RoleOwner {
		t.Error("own role changed")
	}

	// Remove.
	h.post(path+"remove", nil, h.srv.URL)
	if _, err := h.store.UserByID(ctx, dad.ID); err != store.ErrNotFound {
		t.Errorf("dad not removed: %v", err)
	}

	// Admins can't manage people.
	ha, _ := loggedIn(t, auth.RoleAdmin)
	if code, _ := ha.get("/users"); code != http.StatusForbidden {
		t.Errorf("admin users page: %d", code)
	}
}
