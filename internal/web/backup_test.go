package web

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/store"
)

type fakeBackups struct {
	status backup.Status
	runs   int
}

func (f *fakeBackups) Status(context.Context) (backup.Status, error) { return f.status, nil }
func (f *fakeBackups) RunNow() error                                 { f.runs++; return nil }

func TestBackupCard(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleAdmin)
	fb := &fakeBackups{}
	h.node().backups = fb
	card := func() string {
		_, body := h.get("/partials/backups")
		return body
	}

	fb.status = backup.Status{}
	if !strings.Contains(card(), "not set up") {
		t.Error("unconfigured backups must be flagged")
	}

	ok := &store.BackupRun{OK: true, Finished: time.Now().Add(-3 * time.Hour)}
	fb.status = backup.Status{Configured: true, LastOK: ok, Last: ok, LocalOnly: true, Next: time.Now().Add(time.Hour), Progress: -1}
	body := card()
	for _, want := range []string{"Last backup:", "Back up now", "only stored on this machine", "Next backup:"} {
		if !strings.Contains(body, want) {
			t.Errorf("card missing %q", want)
		}
	}
	if strings.Contains(body, "over 2 days ago") {
		t.Error("a recent backup must not be flagged as stale")
	}

	failed := &store.BackupRun{OK: false, Finished: time.Now(), Error: "repository locked"}
	old := &store.BackupRun{OK: true, Finished: time.Now().Add(-72 * time.Hour)}
	fb.status = backup.Status{Configured: true, Last: failed, LastOK: old, Progress: -1}
	body = card()
	if !strings.Contains(body, "repository locked") || !strings.Contains(body, "over 2 days ago") {
		t.Error("failure and staleness must be shown")
	}

	fb.status = backup.Status{Configured: true, Running: true, Progress: 42}
	if body := card(); !strings.Contains(body, "42%") || strings.Contains(body, "Back up now") {
		t.Error("running backup: show progress, hide the button")
	}

	if code, _ := h.post("/backup/run", nil, h.srv.URL); code != http.StatusOK || fb.runs != 1 {
		t.Errorf("back up now: %d, runs %d", code, fb.runs)
	}
}

func TestMembersCannotStartBackups(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	fb := &fakeBackups{status: backup.Status{Configured: true, Progress: -1}}
	h.node().backups = fb
	if _, body := h.get("/"); strings.Contains(body, "Back up now") || strings.Contains(body, `href="/backups"`) {
		t.Error("members must not see backups")
	}
	if code, _ := h.get("/backups"); code != http.StatusForbidden {
		t.Errorf("members can't open the backups page: %d", code)
	}
	if code, _ := h.post("/backup/run", nil, h.srv.URL); code != http.StatusForbidden || fb.runs != 0 {
		t.Errorf("member backup: %d", code)
	}
}
