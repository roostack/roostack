package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
)

// The plan counts a backup as safe from ransomware only where this
// server's credential can't delete it, and says so on each card that it
// can (SPEC.md §6.9, §10.10).
func TestPlanSafeFromRansomware(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	const safe = "1 safe from ransomware"
	// A USB drive, and online storage with the account's key: both deletable.
	fm.destination = "drive:usb1"
	fm.copies = []manager.BackupCopy{{Ref: "provider:1", Title: "Wasabi", Repository: "s3:https://s3.wasabisys.com/b/roostack/l/n"}}
	_, body := h.get("/backups")
	for _, want := range []string{`pill-warn plan-warn">` + safe,
		"its backups here could be deleted too. Keep another backup it can&#39;t delete",
		"it has the account&#39;s key. To protect them, turn on versioning",
		"A backup safe from ransomware (optional)", "<strong>1 safe from ransomware.</strong>"} {
		if !strings.Contains(body, want) {
			t.Errorf("deletable plan missing %q", want)
		}
	}

	// Backblaze with a key of its own: deletes only hide. A bucket that
	// erases hidden files after a day doesn't count.
	fm.backupAccess = map[string]manager.Access{"provider:1": {HidesOnly: true, KeepsHidden: 1}}
	_, body = h.get("/backups")
	for _, want := range []string{`pill-warn plan-warn">` + safe, "anything it deletes is only hidden",
		"This bucket deletes hidden files after a day"} {
		if !strings.Contains(body, want) {
			t.Errorf("short lifecycle missing %q", want)
		}
	}
	if strings.Contains(body, "it has the account&#39;s key") {
		t.Error("a scoped key isn't the account's")
	}
	// Kept for good, or long enough: safe, with no warning.
	for _, days := range []int{0, 30} {
		fm.backupAccess["provider:1"] = manager.Access{HidesOnly: true, KeepsHidden: days}
		_, body = h.get("/backups")
		if !strings.Contains(body, `pill-ok plan-ok">`+safe) || strings.Contains(body, "This bucket deletes hidden files") ||
			strings.Contains(body, "A backup safe from ransomware") {
			t.Errorf("keeping hidden files %d days: should count as safe", days)
		}
	}
	// Not known: not counted, and said.
	fm.backupAccess["provider:1"] = manager.Access{HidesOnly: true, KeepsHidden: -1}
	if _, body = h.get("/backups"); !strings.Contains(body, `pill-warn plan-warn">`+safe) ||
		!strings.Contains(body, "couldn't check how long this bucket keeps hidden files") {
		t.Error("unknown lifecycle")
	}

	// Another server's disk: append-only, safe; no warning on it.
	fm.backupAccess = nil
	fm.copies = []manager.BackupCopy{{Ref: "server:n-0123456789", Title: "nuc's disk", Repository: "rest:http://100.96.0.1:8000/x"}}
	if _, body = h.get("/backups"); !strings.Contains(body, `pill-ok plan-ok">`+safe) {
		t.Error("a backup on another server is safe from ransomware")
	}
}

// Removing a server says which shared credentials it still holds (M22).
func TestRemoveServerSharedCredentials(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true}, {ID: "n-1111111111", Name: "pi"}}
	fm.shared = []manager.SharedCredential{{What: "the account key for Wasabi", Anywhere: true},
		{What: "the password for Alice's lab's storage"}}
	code, body := h.post("/settings/servers/n-1111111111/remove", url.Values{}, h.srv.URL)
	if code != http.StatusOK {
		t.Fatalf("remove: %d", code)
	}
	for _, want := range []string{"pi is out of the lab, but still holds the account key for Wasabi", "make a new key with the provider",
		"pi still knows the password for Alice&#39;s lab&#39;s storage, but can&#39;t reach it"} {
		if !strings.Contains(body, want) {
			t.Errorf("removed page missing %q", want)
		}
	}
	// Nothing shared: back to the Servers page as before.
	fm.shared = nil
	if _, body := h.post("/settings/servers/n-1111111111/remove", url.Values{}, h.srv.URL); strings.Contains(body, "is out of the lab") {
		t.Error("nothing shared, nothing to say")
	}
}
