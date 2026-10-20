package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/store"
)

// Small hardening from the security review of 2026-10-16.

// Folders under /static/ aren't listed.
func TestNoDirectoryListings(t *testing.T) {
	h := newHarness(t, "")
	for _, path := range []string{"/static/", "/static/css/"} {
		if code, body := h.get(path); code != http.StatusNotFound || strings.Contains(body, "tokens.css") {
			t.Errorf("%s: %d, listed:\n%.300s", path, code, body)
		}
	}
	if code, _ := h.get("/static/css/tokens.css"); code != http.StatusOK {
		t.Errorf("a file: %d", code)
	}
}

// The restore page puts what was typed in data attributes, not into an
// Alpine expression, and isn't cached; the setup code counts only when
// it's posted, never from the address.
func TestRestorePageReflection(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	h.server.LabRestore = &fakeLabRestore{}
	resp, err := h.client.Get(h.srv.URL + "/setup/restore?token=the-setup-code")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("the restore page isn't cached")
	}
	if _, body := h.get("/setup/restore?token=the-setup-code"); !strings.Contains(body, `name="token"`) {
		t.Error("a setup code in the address doesn't count")
	}
	h.post("/setup/code", url.Values{"token": {"the-setup-code"}}, h.srv.URL)
	_, body := h.post("/setup/restore", url.Values{"kind": {"';alert(1);//"}, "drive": {"x' + alert(2) + '"},
		"recovery_key": {"k"}}, h.srv.URL)
	if strings.Contains(body, "kind: '") || strings.Contains(body, "drive: '") || !strings.Contains(body, "$el.dataset.kind") {
		t.Errorf("form values reach an Alpine expression:\n%.3000s", body)
	}
	// A choice of where that isn't one comes back as online storage, in
	// the segmented control.
	if !strings.Contains(body, `name="kind" value="s3" checked`) || strings.Contains(body, "alert(1)") {
		t.Errorf("an unknown kind:\n%.3000s", body)
	}
}

// The page with a two-factor key being set up isn't cached.
func TestTwoFactorPageNotCached(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	resp, err := h.client.Get(h.srv.URL + "/account/2fa")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("2FA page: %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
}

// Failed logins are audited, naming the account only when it exists (a
// password typed into the username box isn't kept); so are a person's own
// notification address and Cloud's cancel and dismiss.
func TestMoreAudited(t *testing.T) {
	h := newHarness(t, "")
	ctx := context.Background()
	hash, _ := authkit.HashPassword("a long password")
	h.store.CreateUser(ctx, store.User{Username: "sam", PasswordHash: hash, Role: auth.RoleMember})
	h.post("/login", url.Values{"username": {"sam"}, "password": {"wrong"}}, h.srv.URL)
	h.post("/login", url.Values{"username": {"hunter2-my-password"}, "password": {"x"}}, h.srv.URL)
	logs, _ := h.store.RecentAudit(ctx, 5)
	if len(logs) != 2 || logs[0].Action != "failed login" || !strings.HasPrefix(logs[0].Detail, "an unknown username from ") ||
		!strings.HasPrefix(logs[1].Detail, "sam from ") {
		t.Errorf("failed logins: %+v", logs)
	}

	h, _ = loggedIn(t, auth.RoleMember)
	h.post("/account/notifications", url.Values{"email": {"sam@example.com"}}, h.srv.URL)
	h.post("/account/notifications", url.Values{"email": {"sam@example.com"}}, h.srv.URL)
	logs, _ = h.store.RecentAudit(ctx, 5)
	n := 0
	for _, e := range logs {
		if e.Detail == "notification email changed to sam@example.com" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("a changed address is audited once: %+v", logs)
	}
}

// A restore that didn't finish can be started again, and the page says so
// (review M25).
func TestUnfinishedRestoreSaysSo(t *testing.T) {
	h := newHarness(t, "the-setup-code")
	fr := &fakeLabRestore{}
	h.server.LabRestore = fr
	const note = "A restore onto this server started and didn&#39;t finish"
	if _, body := h.get("/setup/restore"); strings.Contains(body, note) {
		t.Error("nothing unfinished")
	}
	fr.unfinished = true
	if _, body := h.get("/setup/restore"); !strings.Contains(body, note) {
		t.Error("an unfinished restore is offered again")
	}
}
