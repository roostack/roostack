package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/maintenance"
	"github.com/mylab/mylab/internal/store"
)

func TestIsLocal(t *testing.T) {
	for repo, want := range map[string]bool{
		"/mnt/backup/mylab":            true,
		"relative/dir":                 true,
		"s3:https://s3.example.com/b":  false,
		"sftp:user@host:/srv/restic":   false,
		"rest:https://backup.example/": false,
		"":                             false,
	} {
		if got := isLocal(repo); got != want {
			t.Errorf("isLocal(%q) = %v", repo, got)
		}
	}
}

func TestServiceRecordsRuns(t *testing.T) {
	repo := testRepo(t)
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "mylab.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "state", "home")}
	writeFile(t, filepath.Join(paths.StateDir, "lab.yaml"), "x")

	coord := &maintenance.Coordinator{Policy: maintenance.DefaultPolicy(), Now: time.Now,
		Reboot: func() error { return nil }, Journal: nopJournal{}, Ticks: make(chan time.Time),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go coord.Run(ctx)

	svc := &Service{Coord: coord, Store: st, Repository: repo.URL, Deps: &JobDeps{
		Repo: repo, Preparer: &Preparer{}, Paths: paths, Host: "t",
		DumpState: st.Backup,
		Plan:      func() (Plan, error) { return LabPlan(paths, nil), nil },
	}, Activity: activity.New()}
	if err := svc.Submit(); err != nil {
		t.Fatal(err)
	}
	if err := svc.Submit(); !errors.Is(err, ErrAlreadyQueued) {
		t.Errorf("second submit: %v, want ErrAlreadyQueued", err)
	}
	wait := func() *Status {
		for i := 0; i < 600; i++ {
			s, _ := svc.Status(ctx)
			if !s.Running && !s.Queued && s.Last != nil {
				return &s
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("backup did not finish")
		return nil
	}
	s := wait()
	if !s.Last.OK || s.LastOK == nil || s.LastOK.SnapshotID == "" || !s.LocalOnly {
		t.Fatalf("status after success: %+v %+v", s, s.Last)
	}
	act, _ := svc.Activity.Latest(activity.Backup)
	for _, want := range []string{"==> Saving a copy of MyLab's own database", "==> Opening the backup storage",
		"==> Uploading changes", "==> Done"} {
		if !slices.Contains(act.Lines, want) {
			t.Errorf("activity missing %q:\n%s", want, strings.Join(act.Lines, "\n"))
		}
	}
	if !strings.HasPrefix(act.Status, "Backed up: ") || act.Running() {
		t.Errorf("activity status %q", act.Status)
	}

	// A failure is recorded too, and the last success is kept.
	svc.Deps.Plan = func() (Plan, error) { return Plan{}, errors.New("plan broke") }
	if err := svc.Submit(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	s = wait()
	if s.Last.OK || s.Last.Error != "plan broke" || s.LastOK == nil {
		t.Errorf("status after failure: last=%+v lastOK=%+v", s.Last, s.LastOK)
	}
}
