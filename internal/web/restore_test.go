package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/setup"
)

func TestRestoreFlow(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupLater: true}})
	fr := &fakeRestores{
		snaps: []backup.Snapshot{{ID: "9a6817f7aaaa", Short: "9a6817f7", Time: time.Now().Add(-time.Hour)},
			{ID: "8550fc0abbbb", Short: "8550fc0a", Time: time.Now().Add(-26 * time.Hour)}},
		kept: []backup.KeptCopy{{Path: "/srv/roostack/services/vaultwarden/data.before-restore-20260926-130602",
			Of: "/srv/roostack/services/vaultwarden/data", Time: time.Now().Add(-2 * time.Hour)}},
	}
	h.node().restores = fr
	fm := h.node()
	// The newest moment is also in a copy; an old one is in a copy only.
	copyOf := func(id string, at time.Time) manager.ServiceBackup {
		return manager.ServiceBackup{Ref: "copy:n-0123456789:provider:1", ServerID: "n-0123456789", ServerName: "nuc",
			Where: "Backblaze", Snapshot: backup.Snapshot{ID: id, Short: id[:8], Time: at}}
	}
	fr.snaps = fr.snaps[1:]
	newest := time.Now().Add(-time.Hour)
	fm.otherBackups = []manager.BackupMoment{{ServerID: "n-0123456789", ServerName: "nuc", Time: newest,
		Places: []manager.ServiceBackup{{Ref: "backup:n-0123456789", ServerID: "n-0123456789", ServerName: "nuc", Where: "USB drive BACKUP",
			Main: true, Snapshot: backup.Snapshot{ID: "9a6817f7aaaa", Short: "9a6817f7", Time: newest}}, copyOf("77aa00bbcccc", newest)}},
		{ServerID: "n-0123456789", ServerName: "nuc", Time: time.Now().Add(-40 * 24 * time.Hour),
			Places: []manager.ServiceBackup{copyOf("66bb11cccccc", time.Now().Add(-40*24*time.Hour))}}}
	fm.backupNotes = []string{"Couldn't read vps's backups at Backblaze: timeout"}

	if _, body := h.get("/backups"); !strings.Contains(body, `href="/backups/restore/vaultwarden">Restore…<span class="sr-only"> Vaultwarden</span>`) {
		t.Error("backups page should link to restoring each service")
	}
	_, body := h.get("/backups/restore/vaultwarden")
	for _, want := range []string{`<option value="backup:n-0123456789@9a6817f7aaaa">USB drive BACKUP (main backup)</option>`,
		`<option value="copy:n-0123456789:provider:1@77aa00bbcccc">Backblaze (copy)</option>`, "(newest)", "This week", "Older",
		`name="backup" value="copy:n-0123456789:provider:1@66bb11cccccc"`, "Kept copies",
		"data.before-restore-20260926-130602", "Couldn&#39;t read vps&#39;s backups"} {
		if !strings.Contains(body, want) {
			t.Errorf("restore page missing %q", want)
		}
	}
	if code, _ := h.get("/backups/restore/nope"); code != http.StatusNotFound {
		t.Errorf("unknown service: %d", code)
	}

	// The newest moment is chosen, read from its main backup.
	if !strings.Contains(body, `value="backup:n-0123456789@9a6817f7aaaa" data-label="Today&#39;s backup`) &&
		!strings.Contains(body, `value="backup:n-0123456789@9a6817f7aaaa" data-label="Last night&#39;s backup`) {
		t.Error("the newest moment should be listed by its main backup")
	}
	if !strings.Contains(body, `(read from <span x-text="place">USB drive BACKUP (main backup)</span>)`) {
		t.Error("the page should say where it's read from")
	}

	if code, body := h.post("/backups/restore/vaultwarden", url.Values{"backup": {"backup:n-0123456789@8550fc0abbbb"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "Tick the box") || len(fr.restored) != 0 {
		t.Errorf("unconfirmed restore: %d", code)
	}
	h.post("/backups/restore/vaultwarden", url.Values{"backup": {"backup:n-0123456789@8550fc0abbbb"}, "confirm": {"on"}}, h.srv.URL)
	if len(fr.restored) != 1 || fr.restored[0] != "vaultwarden 8550fc0abbbb" {
		t.Errorf("restored %v", fr.restored)
	}
	// From a copy: the controller does it.
	h.post("/backups/restore/vaultwarden", url.Values{"backup": {"copy:n-0123456789:provider:1@77aa00bbcccc"}, "confirm": {"on"}}, h.srv.URL)
	if len(fm.labRestores) != 1 || fm.labRestores[0] != "vaultwarden copy:n-0123456789:provider:1@77aa00bbcccc → " {
		t.Errorf("lab restores %v", fm.labRestores)
	}

	// While it runs, the page shows its progress instead of the form.
	h.acts().Start(activity.Restore, "Restoring Vaultwarden").Step("Restoring files from the backup")
	_, body = h.get("/backups/restore/vaultwarden")
	if !strings.Contains(body, "Restoring Vaultwarden: Restoring files from the backup…") || strings.Contains(body, `name="confirm"`) {
		t.Error("running restore: want progress, no form")
	}

	// The service runs on another server, which has no restore of its own:
	// the controller's restore still shows.
	fm.serviceNode = otherServer{fm}
	_, body = h.get("/backups/restore/vaultwarden")
	if !strings.Contains(body, "Restoring Vaultwarden: Restoring files from the backup…") {
		t.Error("a restore the controller runs should show for a service on another server")
	}
	fm.serviceNode = nil

	// Once it's done, the page says so; restoring again is a click away.
	h.acts().Start(activity.Restore, "Restoring Vaultwarden").Finish(nil)
	_, body = h.get("/backups/restore/vaultwarden")
	if !strings.Contains(body, "Vaultwarden is restored.") || !strings.Contains(body, "<summary>Restore it again…</summary>") {
		t.Error("a finished restore should lead with its result")
	}

	h.post("/backups/restore/vaultwarden/kept/remove", url.Values{"path": {fr.kept[0].Path}}, h.srv.URL)
	if len(fr.removed) != 1 {
		t.Error("kept copy not removed")
	}
	// Every kept copy can be put back (SPEC.md §8.4).
	if !strings.Contains(body, "Put back…") {
		t.Error("no Put back on a kept copy")
	}
	h.post("/backups/restore/vaultwarden/kept/put-back", url.Values{"path": {fr.kept[0].Path}}, h.srv.URL)
	if len(fr.putBack) != 1 || fr.putBack[0] != fr.kept[0].Path {
		t.Errorf("put back %v", fr.putBack)
	}

	// Admins can't restore.
	ha, _ := loggedIn(t, auth.RoleAdmin)
	ha.node().restores = fr
	if code, _ := ha.get("/backups/restore/vaultwarden"); code != http.StatusForbidden {
		t.Errorf("admin restore page: %d", code)
	}
	if _, body := ha.get("/backups"); !strings.Contains(body, "Only the Owner can restore") {
		t.Error("admins should be told who can restore")
	}
}

// otherServer is another server in the lab, with no activities.
type otherServer struct{ *fakeManager }

func (otherServer) Media() node.Media     { return nil }
func (otherServer) Imports() node.Imports { return nil }

func (otherServer) Identity() nodeid.Identity {
	return nodeid.Identity{ID: "n-9876543210", Name: "vps"}
}
func (otherServer) LatestActivity(string) (activity.Snapshot, bool) {
	return activity.Snapshot{}, false
}
