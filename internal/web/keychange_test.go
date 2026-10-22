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
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/setup"
)

// Changing the recovery key (SPEC.md §6.9): the same check as showing it,
// then the new key, which must be confirmed saved before anything
// changes; progress shows on the Backups page.
func TestKeyChangePages(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}})
	u, _ := h.store.UserByUsername(context.Background(), "u")
	code := func(step int) string {
		c, _ := authkit.TOTPCode(u.TOTPSecret, nowFunc().Add(time.Duration(step)*30*time.Second))
		return c
	}
	if _, body := h.get("/backups"); !strings.Contains(body, `href="/backups/recovery-key/change"`) {
		t.Fatal("the Backups page should offer to change the key")
	}
	if _, body := h.get("/backups/recovery-key/change"); !strings.Contains(body, "Make a new key") || !strings.Contains(body, "copied your backups") {
		t.Error("the page explains what a change protects against")
	}
	// A wrong code makes nothing.
	if status, _ := h.post("/backups/recovery-key/change", url.Values{"password": {"a long password"}, "code": {wrongCode(userSecret(h, "u"))}}, h.srv.URL); status != http.StatusBadRequest || len(fm.keyOps) != 0 {
		t.Errorf("wrong code: %d %v", status, fm.keyOps)
	}
	status, body := h.post("/backups/recovery-key/change", url.Values{"password": {"a long password"}, "code": {code(1)}}, h.srv.URL)
	if status != http.StatusOK || !strings.Contains(body, "the-new-recovery-key") || len(fm.keyOps) != 1 {
		t.Fatalf("new key: %d, ops %v", status, fm.keyOps)
	}
	// Coming back later needs the check again to see it.
	if _, body := h.get("/backups/recovery-key/change"); strings.Contains(body, "the-new-recovery-key") || !strings.Contains(body, "Show the new key") {
		t.Error("the new key must not show without the check")
	}
	if _, body := h.get("/backups"); !strings.Contains(body, "waiting for you to confirm") {
		t.Error("the Backups page should say a new key is waiting")
	}
	// Confirming needs the tick.
	if status, _ := h.post("/backups/recovery-key/change/confirm", nil, h.srv.URL); status != http.StatusBadRequest {
		t.Errorf("without the tick: %d", status)
	}
	h.post("/backups/recovery-key/change/confirm", url.Values{"saved": {"on"}}, h.srv.URL)
	if fm.keyOps[len(fm.keyOps)-1] != "confirm" {
		t.Fatalf("ops %v", fm.keyOps)
	}
	if _, body := h.get("/backups"); !strings.Contains(body, "1 of 2 backups have the new key") {
		t.Error("progress on the Backups page")
	}
	if _, body := h.get("/backups/recovery-key/change"); !strings.Contains(body, "waiting (unreachable)") {
		t.Error("each backup's state")
	}
	logs, _ := h.store.RecentAudit(context.Background(), 10)
	var shown bool
	for _, e := range logs {
		shown = shown || e.Action == "recovery key change" && e.Detail == "new key shown"
	}
	if !shown {
		t.Error("showing the new key should be audited")
	}
}

// A new key not confirmed yet can be dropped; Admins get none of it.
func TestKeyChangeCancelAndOwnerOnly(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{BackupDone: true, RecoverySaved: true}})
	fm.keyChange = &manager.KeyChange{Phase: manager.KeyConfirm}
	h.post("/backups/recovery-key/change/cancel", nil, h.srv.URL)
	if fm.keyChange != nil {
		t.Error("cancel")
	}
	ha, _ := loggedIn(t, auth.RoleAdmin)
	ha.setSetup(&fakeSetup{p: setup.Progress{BackupDone: true, RecoverySaved: true}})
	if status, _ := ha.get("/backups/recovery-key/change"); status != http.StatusForbidden {
		t.Errorf("as an Admin: %d", status)
	}
}
