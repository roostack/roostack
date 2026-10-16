package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
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
