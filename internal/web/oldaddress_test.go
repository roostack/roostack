package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/setup"
)

// After switching address, every page that shows an address says the old
// one still works until when, and the Owner can stop it early (SPEC.md
// §6.6).
func TestOldAddress(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}}
	h.server.Setup = fs
	until := time.Now().Add(20 * 24 * time.Hour)
	h.server.OldAddress = &config.OldAddress{Domain: "old.example.net", Until: until, DNS: config.DNS{Provider: "cloudflare"}}
	day := until.Local().Format("2 January")

	if _, body := h.get("/"); !strings.Contains(body, "Your lab&#39;s address changed to lab.example.com.</strong><span>The old addresses (old.example.net) keep working until "+day) ||
		!strings.Contains(body, `href="/settings/domain#old-address"`) {
		t.Error("home: the old address's notice")
	}
	_, body := h.get("/settings/domain")
	for _, want := range []string{`id="old-address"`, "<code>https://roost.old.example.net</code>", "<code>https://roost.lab.example.com</code>",
		"<code>https://passwords.old.example.net</code>", "Cloudflare", "Stop the old addresses now"} {
		if !strings.Contains(body, want) {
			t.Errorf("settings missing %q", want)
		}
	}
	if _, body := h.get("/services/vaultwarden"); !strings.Contains(body, "also at its old address, https://passwords.old.example.net") {
		t.Error("service page: old address")
	}
	if _, body := h.get("/setup/domain"); !strings.Contains(body, "keep working for 30 days") ||
		!strings.Contains(body, "old.example.net, the address before, stops at once") {
		t.Error("change form: what happens to the old address")
	}
	if code, _ := h.post("/settings/domain/old/end", nil, h.srv.URL); code != http.StatusOK || !fs.oldEnded {
		t.Errorf("stop now: %d", code)
	}

	// Once it's over, nothing about it.
	h.server.OldAddress.Until = time.Now().Add(-time.Minute)
	if _, body := h.get("/"); strings.Contains(body, "old.example.net") {
		t.Error("ended: still mentioned")
	}
}
