package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/store"
)

func TestIsLocal(t *testing.T) {
	for repo, want := range map[string]bool{
		"/mnt/backup/roostack":         true,
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
	repo, copyRepo := testRepo(t), testRepo(t)
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "roostack.db"))
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
		Copies:    []Copy{{Name: "provider:1", Title: "Backblaze", Repo: copyRepo}},
	}, Activity: activity.New()}
	var after atomic.Int32
	svc.AfterBackup = func() { after.Add(1) }
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
	if after.Load() != 1 {
		t.Errorf("AfterBackup ran %d times after a backup that worked", after.Load())
	}
	// Each run is recorded by its repository, a copy's with its services
	// too, so it keeps them if it becomes the main backup.
	if s.Last.Repository != repo.URL || len(s.Copies) != 1 || s.Copies[0].LastOK == nil ||
		s.Copies[0].LastOK.Repository != copyRepo.URL || s.Copies[0].LastOK.Services == nil {
		t.Errorf("recorded by repository: main %+v, copies %+v", s.Last, s.Copies)
	}
	act, _ := svc.Activity.Latest(activity.Backup)
	for _, want := range []string{"==> Saving a copy of Roostack's own database", "==> Opening the backup storage",
		"==> Backing up Roostack's own settings", "==> Done"} {
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
	if after.Load() != 1 {
		t.Error("AfterBackup runs only after a backup that worked")
	}
}

// A night the server was off is caught up: a backup is queued when none
// was tried for over a day (or ever: a new lab's first), and not
// otherwise; the next backup's time says when the catch-up runs one.
func TestCatchUp(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A coordinator that never runs anything: queued is all we look at.
	coord := &maintenance.Coordinator{Policy: maintenance.DefaultPolicy(), Now: time.Now, Journal: nopJournal{},
		Ticks: make(chan time.Time), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	night := time.Now().Add(15 * time.Hour)
	svc := &Service{Coord: coord, Store: st, NextRun: func() time.Time { return night }}
	if why, err := svc.CatchUp(ctx); why != "" || err != nil {
		t.Errorf("not set up: %q %v", why, err)
	}
	svc.Deps = &JobDeps{Repo: &Repo{URL: "/mnt/usb/roostack"}}
	soon := time.Now().Add(10 * time.Minute)
	svc.CatchUpAt(soon)
	if s, _ := svc.Status(ctx); !s.Next.Equal(soon) {
		t.Errorf("never backed up: the next backup %v, want the catch-up's %v", s.Next, soon)
	}
	if why, err := svc.CatchUp(ctx); why != "no backup yet" || err != nil {
		t.Errorf("never backed up: %q %v", why, err)
	}
	if why, _ := svc.CatchUp(ctx); why != "" {
		t.Error("one is queued already")
	}
	svc.setPending(false)
	st.RecordBackup(ctx, store.BackupRun{Started: time.Now().Add(-40 * time.Hour), Finished: time.Now().Add(-40 * time.Hour), OK: true, Repository: "/mnt/usb/roostack"})
	st.RecordBackup(ctx, store.BackupRun{Started: time.Now().Add(-3 * time.Hour), Finished: time.Now().Add(-3 * time.Hour), OK: false, Repository: "/mnt/usb/roostack"})
	if why, _ := svc.CatchUp(ctx); why != "" {
		t.Error("tried 3 hours ago (failed attempts have their own retries)")
	}
	if s, _ := svc.Status(ctx); !s.Next.Equal(night) {
		t.Errorf("tried 3 hours ago: the next backup %v, want the night's %v", s.Next, night)
	}
	// A day and a half ago: the server was off last night.
	st.RecordBackup(ctx, store.BackupRun{Started: time.Now().Add(-36 * time.Hour), Finished: time.Now().Add(-36 * time.Hour), OK: true, Repository: "/mnt/usb/roostack"})
	if why, _ := svc.CatchUp(ctx); why != "none for over a day" {
		t.Errorf("over a day since the last attempt: %q", why)
	}
}

// TestStatusAfterSwap: "Make this backup 1" swaps the main backup and a
// copy; each keeps its own results, read by repository, and a page that
// matches its entries by repository gets them right even before the
// server has restarted with the swap.
func TestStatusAfterSwap(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	coord := &maintenance.Coordinator{Policy: maintenance.DefaultPolicy(), Now: time.Now, Journal: nopJournal{},
		Ticks: make(chan time.Time), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	usb, cloud := &Repo{URL: "/mnt/usb/roostack"}, &Repo{URL: "s3:https://s3.example/b/lab"}
	day := time.Now().Add(-20 * time.Hour)
	// The USB drive's last backup failed; the online copy's worked.
	st.RecordBackup(ctx, store.BackupRun{Started: day, Finished: day, OK: true, SnapshotID: "usb-ok", Repository: usb.URL})
	st.RecordBackup(ctx, store.BackupRun{Started: day.Add(time.Hour), Finished: day.Add(time.Hour), OK: false,
		Error: "the backup drive isn't connected", Repository: usb.URL})
	st.RecordBackup(ctx, store.BackupRun{Started: day.Add(time.Hour), Finished: day.Add(time.Hour), OK: true,
		SnapshotID: "cloud-ok", Repository: cloud.URL})

	before := &Service{Coord: coord, Store: st, Deps: &JobDeps{Repo: usb,
		Copies: []Copy{{Name: "provider:1", Title: "Backblaze", Repo: cloud}}}}
	after := &Service{Coord: coord, Store: st, Deps: &JobDeps{Repo: cloud,
		Copies: []Copy{{Name: "drive:uuid:64B1", Title: "USB drive", Repo: usb}}}}
	for name, svc := range map[string]*Service{"before the swap": before, "after the swap": after} {
		s, err := svc.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if last, ok, _ := s.For(cloud.URL); last == nil || !last.OK || ok.SnapshotID != "cloud-ok" {
			t.Errorf("%s: the online storage shows %+v", name, last)
		}
		if last, ok, _ := s.For(usb.URL); last == nil || last.OK || ok.SnapshotID != "usb-ok" {
			t.Errorf("%s: the USB drive shows %+v", name, last)
		}
		if last, _, _ := s.For("/mnt/elsewhere"); last != nil {
			t.Errorf("%s: a repository it doesn't use has results", name)
		}
	}
	s, _ := after.Status(ctx)
	if s.Last == nil || !s.Last.OK || s.LastOK.SnapshotID != "cloud-ok" {
		t.Errorf("after the swap, the main backup shows %+v, want the online storage's", s.Last)
	}
	if len(s.Copies) != 1 || s.Copies[0].Last == nil || s.Copies[0].Last.OK {
		t.Errorf("after the swap, the copy shows %+v, want the USB drive's failure", s.Copies)
	}
}
