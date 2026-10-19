package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
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

// The records Roostack set under an old address: a problem keeping them
// up to date is said while it works; once it has ended, those it couldn't
// delete are listed, with why, until the Owner says they're gone; so are
// records under no address the lab has (SPEC.md §6.6).
func TestOldAddressRecords(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}}
	h.server.Setup = fs
	h.server.OldAddress = &config.OldAddress{Domain: "old.example.net", Until: time.Now().Add(20 * 24 * time.Hour),
		DNS: config.DNS{Provider: "cloudflare"}}
	fd := &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateOK, Old: &dnsrecords.OldRecords{Domain: "old.example.net",
		Provider: "Cloudflare", Records: []dnsrecords.Record{{Name: "*.old.example.net", Type: "A", Value: "192.168.1.20"}},
		Message: "Roostack couldn't update its records under old.example.net with that address's login at Cloudflare: 403 Forbidden"}}}
	h.server.DNS = fd
	if _, body := h.get("/settings/domain"); !strings.Contains(body, "403 Forbidden") || strings.Contains(body, `id="old-records"`) {
		t.Error("while it works: the problem, and nothing to delete by hand yet")
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "couldn&#39;t update the DNS records of your lab&#39;s old address") {
		t.Error("home: the problem")
	}

	h.server.OldAddress.Until = time.Now().Add(-time.Hour)
	fd.st.Old.Ended = true
	fd.st.Old.Message = "Roostack couldn't delete its records under old.example.net with that address's login at Cloudflare: 403 Forbidden"
	fd.st.Left = []dnsrecords.Record{{Name: "vault.older.example.org", Type: "A", Value: "81.2.69.160", Via: "porkbun"}}
	_, body := h.get("/settings/domain")
	for _, want := range []string{`id="old-records"`, "403 Forbidden", "<code>*.old.example.net</code>", "tries again every hour",
		"<code>vault.older.example.org</code>", "Delete them at Porkbun", "I&#39;ve deleted them"} {
		if !strings.Contains(body, want) {
			t.Errorf("ended: missing %q", want)
		}
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "couldn&#39;t delete the DNS records it set under your lab&#39;s old address, old.example.net") ||
		!strings.Contains(body, "an address your lab no longer uses are still there: delete them at Porkbun.") {
		t.Error("home: what's left")
	}

	// Records from before Roostack noted their provider: it can't say
	// where, and they may have been a roost.ac name's, which Cloud removed.
	fd.st.Left = append(fd.st.Left, dnsrecords.Record{Name: "*.smiths.roost.ac", Type: "A", Value: "192.168.1.20"})
	_, body = h.get("/settings/domain")
	for _, want := range []string{"Delete them at Porkbun", "roost.ac name, there's nothing to do there", "<code>*.smiths.roost.ac</code>",
		"I&#39;ve deleted them"} {
		if !strings.Contains(body, want) {
			t.Errorf("not noted: missing %q", want)
		}
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "still there: delete them at your DNS provider.") {
		t.Error("home: two providers, or one not known")
	}
	if code, body := h.post("/settings/domain/old/forget", nil, h.srv.URL); code != http.StatusOK || !fd.forgot || !fs.oldFinished ||
		!strings.Contains(body, "no longer looks after those records") || strings.Contains(body, `id="old-records"`) {
		t.Errorf("forget: %d, forgot %v, finished %v", code, fd.forgot, fs.oldFinished)
	}
}

// A record Roostack couldn't delete when its address went private is
// said on Public access, and on Home.
func TestNotDeletedRecords(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.server.DNS = &fakeDNS{st: dnsrecords.Status{State: dnsrecords.StateOK, NotDeleted: []dnsrecords.PublicRecord{{
		Host: "photos.lab.example.com", IP: "81.2.69.160", State: dnsrecords.PublicError,
		Message: "Roostack couldn't delete its record, which still points at 81.2.69.160: 500. It tries again in a few minutes."}}}}
	if _, body := h.get("/settings/public"); !strings.Contains(body, `id="not-deleted"`) || !strings.Contains(body, "<code>photos.lab.example.com</code>") {
		t.Error("public access: the record left")
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "couldn&#39;t delete the DNS record of an address that isn&#39;t public any more") {
		t.Error("home: the record left")
	}
}
