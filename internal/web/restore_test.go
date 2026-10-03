package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/setup"
)

type fakeRestores struct {
	snaps    []backup.Snapshot
	kept     []backup.KeptCopy
	restored []string
	removed  []string
	pending  bool
}

func (f *fakeRestores) Snapshots(context.Context, string) ([]backup.Snapshot, error) {
	return f.snaps, nil
}
func (f *fakeRestores) Restore(name, id string) error {
	if f.pending {
		return backup.ErrRestoreQueued
	}
	f.restored = append(f.restored, name+" "+id)
	return nil
}
func (f *fakeRestores) Pending() bool                          { return f.pending }
func (f *fakeRestores) Kept(string) ([]backup.KeptCopy, error) { return f.kept, nil }
func (f *fakeRestores) RemoveKept(_ context.Context, _, p string) error {
	f.removed = append(f.removed, p)
	return nil
}

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
	fm.otherBackups = []manager.ServiceBackup{{Ref: "copy:n-0123456789:provider:1", ServerID: "n-0123456789", ServerName: "nuc",
		Where: "Backblaze", Snapshot: backup.Snapshot{ID: "77aa00bbcccc", Short: "77aa00bb", Time: time.Now().Add(-50 * time.Minute)}}}
	fm.backupNotes = []string{"Couldn't read vps's backups at Backblaze: timeout"}

	if _, body := h.get("/backups"); !strings.Contains(body, `href="/backups/restore/vaultwarden">Restore Vaultwarden…`) {
		t.Error("backups page should link to restoring each service")
	}
	_, body := h.get("/backups/restore/vaultwarden")
	for _, want := range []string{`value="backup:n-0123456789@9a6817f7aaaa"`, "(suggested)", "8550fc0a", "Kept copies",
		"data.before-restore-20260926-130602", "from nuc&#39;s copy at Backblaze", "Couldn&#39;t read vps&#39;s backups"} {
		if !strings.Contains(body, want) {
			t.Errorf("restore page missing %q", want)
		}
	}
	if code, _ := h.get("/backups/restore/nope"); code != http.StatusNotFound {
		t.Errorf("unknown service: %d", code)
	}

	// The suggested backup is chosen; the rest are a click away.
	if i, j := strings.Index(body, "9a6817f7aaaa"), strings.Index(body, "Choose a different backup"); i < 0 || j < 0 ||
		!strings.Contains(body[i:], " checked") || strings.Index(body[i:], " checked") > strings.Index(body[i:], "</label>") {
		t.Error("the newest main backup should be chosen")
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

	h.post("/backups/restore/vaultwarden/kept/remove", url.Values{"path": {fr.kept[0].Path}}, h.srv.URL)
	if len(fr.removed) != 1 {
		t.Error("kept copy not removed")
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
