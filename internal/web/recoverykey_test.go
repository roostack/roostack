package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/setup"
)

// Choosing "Set up backups later", then adding the first backup from the
// Backups page, shows the new recovery key just as the wizard does. Once
// it's saved, the setup page doesn't show it again.
func TestRecoveryKeyAfterBackupsLater(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{noKey: true, p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}}
	h.setSetup(fs)
	if _, body := h.get("/backups"); strings.Contains(body, "Show recovery key") {
		t.Error("no key yet, so nothing to show")
	}
	h.post("/backups/plan", url.Values{"add": {"2"}, "choice": {"online"}, "provider": {"provider:1"}}, h.srv.URL)
	if fm.destination != "provider:1" {
		t.Fatalf("backup 1 not set: %q", fm.destination)
	}
	resp, err := h.client.Get(h.srv.URL + "/backups")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Request.URL.Path != "/setup/recovery-key" {
		t.Fatalf("after the first backup: %s, want the recovery key page", resp.Request.URL.Path)
	}
	// Nothing waited for the end of the wizard: no restart, back to Backups.
	h.post("/setup/recovery-key", url.Values{"saved": {"on"}}, h.srv.URL)
	if !fs.p.RecoverySaved {
		t.Fatal("not confirmed")
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, _ := http.NewRequest(method, h.srv.URL+"/setup/recovery-key", strings.NewReader("saved=on"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", h.srv.URL)
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.Request.URL.Path != "/backups" {
			t.Errorf("%s /setup/recovery-key once saved: ended at %s, want /backups", method, resp.Request.URL.Path)
		}
	}
	// A second backup doesn't ask again.
	h.post("/backups/plan", url.Values{"add": {"3"}, "choice": {"online"}, "provider": {"provider:1"}}, h.srv.URL)
	if !fs.p.RecoverySaved {
		t.Error("a second backup asked for the key again")
	}
}

// The Owner sees the key again on the Backups page after re-entering their
// password and a fresh two-factor code; each code works once, it's
// audited, and too many wrong tries end the session.
func TestShowRecoveryKey(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}})
	u, _ := h.store.UserByUsername(context.Background(), "u")
	if _, body := h.get("/backups"); !strings.Contains(body, `href="/backups/recovery-key"`) {
		t.Fatal("the Backups page should offer to show the key")
	}
	if _, body := h.get("/backups/recovery-key"); strings.Contains(body, "the-recovery-key") {
		t.Fatal("the key must not show before checking it's you")
	}
	// The code that works, kept: worked out again later, it could be the
	// next 30-second step's, a new code that rightly works (the cause of
	// this test's failures under load, 2026-10-18).
	good, _ := authkit.TOTPCode(u.TOTPSecret, nowFunc().Add(30*time.Second))
	// Right code, wrong password; right password, wrong code.
	for _, form := range []url.Values{
		{"password": {"not my password"}, "code": {good}},
		{"password": {"a long password"}, "code": {wrongCode(u.TOTPSecret)}},
	} {
		if status, body := h.post("/backups/recovery-key", form, h.srv.URL); status != http.StatusBadRequest ||
			strings.Contains(body, "the-recovery-key") {
			t.Errorf("wrong password or code: %d", status)
		}
	}
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/backups/recovery-key",
		strings.NewReader(url.Values{"password": {"a long password"}, "code": {good}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", h.srv.URL)
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "the-recovery-key") || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("right password and code: key shown %v, cache %q", strings.Contains(string(body), "the-recovery-key"),
			resp.Header.Get("Cache-Control"))
	}
	// The same code can't be used twice.
	if status, _ := h.post("/backups/recovery-key", url.Values{"password": {"a long password"}, "code": {good}}, h.srv.URL); status != http.StatusBadRequest {
		t.Errorf("a used code: %d", status)
	}
	logs, _ := h.store.RecentAudit(context.Background(), 10)
	var shown, wrong int
	for _, e := range logs {
		if e.Action == "recovery key" && e.Detail == "shown" {
			shown++
		}
		if e.Action == "recovery key" && e.Detail == "wrong password or code" {
			wrong++
		}
	}
	if shown != 1 || wrong != 3 {
		t.Errorf("audit: shown %d, wrong %d", shown, wrong)
	}
	// Showing it reset the count: the used code was 1 wrong try, and four
	// more make five, which ends the session.
	for range 4 {
		h.post("/backups/recovery-key", url.Values{"password": {"guess"}, "code": {wrongCode(u.TOTPSecret)}}, h.srv.URL)
	}
	if _, body := h.get("/backups"); !strings.Contains(body, "Log in") {
		t.Error("five wrong tries should end the session")
	}
}

// Only the Owner can see the key, and the section isn't shown to Admins.
func TestShowRecoveryKeyOwnerOnly(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	h.setSetup(&fakeSetup{p: setup.Progress{BackupDone: true, RecoverySaved: true}})
	if _, body := h.get("/backups"); strings.Contains(body, "/backups/recovery-key") {
		t.Error("Admins shouldn't be offered the key")
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req, _ := http.NewRequest(method, h.srv.URL+"/backups/recovery-key", nil)
		req.Header.Set("Origin", h.srv.URL)
		resp, err := h.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s as an Admin: %d", method, resp.StatusCode)
		}
	}
}

// While the account's codes are locked (wrong codes at login, say), even
// the right code doesn't show the key.
func TestShowRecoveryKeyLocked(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}})
	ctx := context.Background()
	u, _ := h.store.UserByUsername(ctx, "u")
	for range authkit.TOTPFailuresPerLock {
		h.store.AddTOTPFailure(ctx, u.ID, time.Now())
	}
	code, _ := authkit.TOTPCode(u.TOTPSecret, nowFunc().Add(30*time.Second))
	status, body := h.post("/backups/recovery-key", url.Values{"password": {"a long password"}, "code": {code}}, h.srv.URL)
	if status != http.StatusTooManyRequests || strings.Contains(body, "the-recovery-key") || !strings.Contains(body, "no code will work") {
		t.Errorf("locked: %d, key shown %v", status, strings.Contains(body, "the-recovery-key"))
	}
}
