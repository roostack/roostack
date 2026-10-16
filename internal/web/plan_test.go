package web

import (
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
