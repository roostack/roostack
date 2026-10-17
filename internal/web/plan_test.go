package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/store"
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
	fm.backupAccess = map[string]manager.Access{"provider:1": {B2: true, HidesOnly: true, KeepsHidden: 1}}
	_, body = h.get("/backups")
	for _, want := range []string{`pill-warn plan-warn">` + safe, "anything this server deletes is only hidden",
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
	for _, want := range []string{"pi is out of the lab, but still holds the account key for Wasabi", "make a new key with the provider and give it to Roostack", "then Change the key",
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

// An online storage account's key changes in place, from any backup there
// (SPEC.md §10.10).
func TestPlanChangeKey(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.destination = "drive:usb1"
	fm.copies = []manager.BackupCopy{{Ref: "provider:1", Title: "Wasabi", Repository: "s3:https://s3.wasabisys.com/b/roostack/l/n"}}
	_, body := h.get("/backups")
	for _, want := range []string{"Change the key for Wasabi…", `action="/backups/plan/key"`, `name="key" value="provider:1"`,
		"Every server backing up to Wasabi uses the new key", ">Access key <input"} {
		if !strings.Contains(body, want) {
			t.Errorf("key form missing %q", want)
		}
	}
	if strings.Contains(body, "If it can make keys") || strings.Contains(body, `id="plan-key-3" open`) {
		t.Error("Wasabi isn't Backblaze, and the form opens when asked")
	}
	// Backblaze names its keys differently, and can give each server one.
	fm.backupAccess = map[string]manager.Access{"provider:1": {B2: true, CanDelete: true}}
	_, body = h.get("/backups?key=provider:1")
	for _, want := range []string{">Key ID <input", "If it can make keys, each server gets a key of its own", `id="plan-key-3" open`} {
		if !strings.Contains(body, want) {
			t.Errorf("Backblaze key form missing %q", want)
		}
	}

	h.post("/backups/plan/key", url.Values{"key": {"provider:1"}, "access_key": {"new-id"}, "secret_key": {"new-key"}}, h.srv.URL)
	if len(fm.s3Keys) != 1 || fm.s3Keys[0] != "provider:1 new-id new-key" {
		t.Errorf("keys given: %v", fm.s3Keys)
	}
	// Only for an account this server backs up to.
	if code, _ := h.post("/backups/plan/key", url.Values{"key": {"provider:2"}, "access_key": {"a"}, "secret_key": {"b"}}, h.srv.URL); code != http.StatusNotFound {
		t.Errorf("another account: %d", code)
	}
	// A key that doesn't work: said, with the form still open.
	fm.keyErr = &manager.FormError{Msg: "Backblaze didn't accept this key: wrong key"}
	_, body = h.post("/backups/plan/key", url.Values{"key": {"provider:1"}, "access_key": {"x"}, "secret_key": {"y"}}, h.srv.URL)
	if !strings.Contains(body, "Backblaze didn&#39;t accept this key") || !strings.Contains(body, `id="plan-key-3" open`) {
		t.Error("a refused key: the message, and the form open")
	}
}

// Backblaze with the account's key: the card says how to make a key that
// can make keys, links to the form, and the plan suggests it; the Online
// storage form says so once the endpoint is Backblaze's
// (docs/ASSUMPTIONS.md N6).
func TestPlanB2KeyHelp(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.destination = "drive:usb1"
	fm.copies = []manager.BackupCopy{{Ref: "provider:1", Title: "Backblaze", Repository: "s3:https://s3.us-west-004.backblazeb2.com/b/roostack/l/n"}}
	fm.backupAccess = map[string]manager.Access{"provider:1": {B2: true, CanDelete: true}}
	_, body := h.get("/backups")
	const command = "b2 key create roostack listBuckets,listFiles,readFiles,writeFiles,deleteFiles,writeKeys,deleteKeys,listKeys"
	for _, want := range []string{"as the key Roostack was given can&#39;t make other keys", `href="/backups?key=provider:1#plan-key-3"`,
		">Change the key<", command, "b2 account authorize", "give them to Roostack\n  with Change the key",
		"Then delete the old key under App Keys", "Suggested: give Roostack a Backblaze key that can make keys (see Backblaze)"} {
		if !strings.Contains(body, want) {
			t.Errorf("Backblaze card missing %q", want)
		}
	}
	if strings.Contains(body, "Or use Backblaze B2") {
		t.Error("a Backblaze backup mustn't suggest Backblaze")
	}
	// Its own key: no help needed.
	fm.backupAccess["provider:1"] = manager.Access{B2: true, HidesOnly: true}
	if _, body = h.get("/backups"); strings.Contains(body, command) {
		t.Error("a server with a key of its own needs no new one")
	}

	// The Online storage form, while adding a backup.
	_, body = h.get("/backups?add=4")
	for _, want := range []string{`x-model="endpoint"`, `x-show="endpoint.includes('.backblazeb2.com')"`,
		"Backblaze: give Roostack a key that can make keys", command, "enter them above as the access key and secret key"} {
		if !strings.Contains(body, want) {
			t.Errorf("Online storage form missing %q", want)
		}
	}
}

// A main backup on a server that left the lab: the plan and "needs
// attention" say so at once, rather than each nightly failure.
func TestPlanLostMainBackup(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.copies = []manager.BackupCopy{{Ref: "provider:1", Title: "Wasabi", Repository: "s3:https://s3.wasabisys.com/b/roostack/l/n"}}
	h.node().backups = &fakeBackups{status: backup.Status{Configured: true, Progress: -1,
		Last: &store.BackupRun{Finished: time.Now(), Error: "connection refused"}}}
	fm.lostMain = map[string]store.LostBackup{"n-0123456789": {ServerID: "n-0123456789", HostName: "pi",
		Lost: time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local)}}
	_, body := h.get("/backups")
	for _, want := range []string{"The main backup was on pi&#39;s disk, and pi left the lab on 17 October",
		"nightly backups fail, though the other backups still run", `href="/backups?add=3#plan"`, "Choose a new place"} {
		if !strings.Contains(body, want) {
			t.Errorf("plan missing %q", want)
		}
	}
	if _, body = h.get("/backups?add=3"); !strings.Contains(body, "Where should your main backup go?") {
		t.Error("the chooser is for the main backup")
	}
	_, body = h.get("/")
	if !strings.Contains(body, "The main backup was on pi&#39;s disk, and pi has left the lab") {
		t.Error("needs attention says why")
	}
	if strings.Contains(body, "The last backup didn") {
		t.Error("not each failure as well")
	}

	// A new main backup: nothing more to say.
	fm.destination, fm.lostMain = "provider:2", nil
	if _, body = h.get("/backups"); strings.Contains(body, "left the lab") {
		t.Error("a new main backup ends it")
	}
}

// A backup at storage that keeps old versions can be put back as it was
// at a time, under Manage: checked first, then confirmed (SPEC.md §10.10).
func TestPlanRollback(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.destination = "drive:usb1"
	fm.copies = []manager.BackupCopy{{Ref: "provider:1", Title: "Wasabi", Repository: "s3:https://s3.wasabisys.com/b/roostack/l/n"}}
	_, body := h.get("/backups")
	if strings.Contains(body, "Bring back what was hidden") {
		t.Error("not where old versions aren't kept")
	}
	fm.backupAccess = map[string]manager.Access{"provider:1": {HidesOnly: true, KeepsHidden: 7, Versioned: true}}
	_, body = h.get("/backups")
	for _, want := range []string{"Bring back what was hidden or changed…", `action="/backups/plan/rollback"`, `type="datetime-local" name="before"`,
		"The bucket keeps old versions, and the account's key can't erase them",
		"In the bucket&#39;s lifecycle rules, keep old versions for at least 30 days"} {
		if !strings.Contains(body, want) {
			t.Errorf("plan missing %q", want)
		}
	}
	if strings.Contains(body, `id="plan-rollback-3" open`) || strings.Contains(body, `name="step" value="run"`) {
		t.Error("closed, and nothing to confirm before a check")
	}

	h.post("/backups/plan/rollback", url.Values{"destination": {"provider:1"}, "before": {"2026-10-16T09:00"}, "step": {"check"}}, h.srv.URL)
	if len(fm.rollbacks) != 1 || fm.rollbacks[0] != "check n-0123456789 provider:1 2026-10-16T09:00" {
		t.Fatalf("rollbacks = %v", fm.rollbacks)
	}
	// Checked: what it changes, and a confirmation for that time.
	before := time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local)
	fm.rollbackPlan = &manager.RollbackPlan{Before: before, Changed: 3,
		Summary: []string{"1 backup (a snapshot) hidden since then comes back."}}
	_, body = h.get("/backups?rollback=provider:1")
	for _, want := range []string{`id="plan-manage-3" open`, `id="plan-rollback-3" open`, `value="2026-10-16T09:00"`, "Go back to 16 October 09:00",
		"Go back to 16 October 09:00 in these backups? 1 backup (a snapshot) hidden since then comes back. Nothing is erased",
		`name="step" value="run"`} {
		if !strings.Contains(body, want) {
			t.Errorf("checked plan missing %q", want)
		}
	}
	h.post("/backups/plan/rollback", url.Values{"destination": {"provider:1"}, "before": {"2026-10-16T09:00"}, "step": {"run"}}, h.srv.URL)
	if len(fm.rollbacks) != 2 || fm.rollbacks[1] != "run n-0123456789 provider:1 2026-10-16T09:00" {
		t.Errorf("rollbacks = %v", fm.rollbacks)
	}

	// Refused: said on the page; a time that isn't one; another place.
	fm.rollbackErr = &manager.FormError{Msg: "Choose a time that has passed."}
	if _, body = h.post("/backups/plan/rollback", url.Values{"destination": {"provider:1"}, "before": {"2030-01-01T00:00"}}, h.srv.URL); !strings.Contains(body, "Choose a time that has passed.") {
		t.Error("a refusal is shown")
	}
	if _, body = h.post("/backups/plan/rollback", url.Values{"destination": {"provider:1"}, "before": {"yesterday"}}, h.srv.URL); !strings.Contains(body, "Choose the date and time to go back to.") {
		t.Error("a time that isn't one")
	}
	if code, _ := h.post("/backups/plan/rollback", url.Values{"destination": {"provider:2"}, "before": {"2026-10-16T09:00"}}, h.srv.URL); code != http.StatusNotFound {
		t.Errorf("another place: %d", code)
	}
	// Only Owners.
	ha, _ := loggedIn(t, auth.RoleAdmin)
	if code, _ := ha.post("/backups/plan/rollback", url.Values{"destination": {"provider:1"}, "before": {"2026-10-16T09:00"}}, ha.srv.URL); code != http.StatusForbidden {
		t.Errorf("an admin: %d", code)
	}

	// Versions kept, but the key can erase them: the card says so.
	fm.backupAccess = map[string]manager.Access{"provider:1": {CanDelete: true, Versioned: true}}
	if _, body = h.get("/backups"); !strings.Contains(body, "the bucket keeps old versions, but the account&#39;s key") ||
		!strings.Contains(body, "Bring back what was hidden") {
		t.Error("versioned, with a key that can erase")
	}
}
