package backup

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/store/storetest"
	"roostack.cloud/roostack/internal/testwait"
)

// The lab was restored on another machine (a newer generation backs up
// into the same repository): this copy stands down, uploads nothing, and
// stays down until the Owner keeps it; then the other copy would stand
// down instead.
func TestFenceRestored(t *testing.T) {
	t.Parallel()
	repo := testRepo(t)
	ctx := context.Background()
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "state", "home")}
	writeFile(t, filepath.Join(paths.StateDir, "lab.yaml"), "domain: x")
	deps := &JobDeps{Repo: repo, Preparer: &Preparer{}, Paths: paths, Host: "old",
		DumpState: func(_ context.Context, path string) error { writeFile(t, path, "db"); return nil },
		Plan:      func() (Plan, error) { return LabPlan(paths, nil), nil }}

	// Both copies at generation 0 back up happily.
	if _, err := Run(ctx, *deps, nopReporter{}); err != nil {
		t.Fatal(err)
	}
	// The restored copy (generation 1, another machine) backs up there.
	restored := *deps
	restored.Host, restored.Generation = "new", 1
	if _, err := Run(ctx, restored, nopReporter{}); err != nil {
		t.Fatal(err)
	}
	before, _ := repo.Snapshots(ctx, TagRoostack)

	st := storetest.Open(t)
	coord := &maintenance.Coordinator{Policy: maintenance.DefaultPolicy(), Now: time.Now, Journal: nopJournal{},
		Ticks: make(chan time.Time), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go coord.Run(cctx)
	svc := &Service{Coord: coord, Store: st, Deps: deps, Activity: activity.New()}
	if err := svc.Submit(); err != nil {
		t.Fatal(err)
	}
	testwait.For(t, 60*time.Second, func() bool { return svc.Fenced(ctx) != nil })
	testwait.For(t, 10*time.Second, func() bool { st, _ := svc.Status(ctx); return !st.Running && !st.Queued })
	if after, _ := repo.Snapshots(ctx, TagRoostack); len(after) != len(before) {
		t.Errorf("the old copy uploaded %d snapshots", len(after)-len(before))
	}
	f := svc.Fenced(ctx)
	if f == nil || f.Reason != FenceRestored || f.Host != "new" || f.Gen != 1 {
		t.Fatalf("fence = %+v", f)
	}
	if err := svc.Submit(); !errors.Is(err, ErrFenced) {
		t.Errorf("no backups while fenced: %v", err)
	}
	if st, _ := svc.Status(ctx); st.Fenced == nil {
		t.Error("the status says so")
	}

	// The Owner keeps this copy: a newer generation still, backing up again.
	if err := svc.KeepThisCopy(ctx); err != nil {
		t.Fatal(err)
	}
	if svc.Fenced(ctx) != nil || deps.Generation != 2 || ReadGeneration(paths) != 2 {
		t.Fatalf("kept: fence %v, generation %d (file %d)", svc.Fenced(ctx), deps.Generation, ReadGeneration(paths))
	}
	if _, err := Run(ctx, *deps, nopReporter{}); err != nil {
		t.Fatal(err)
	}
	// And now the other copy is the out-of-date one.
	var fe *FenceError
	if _, err := Run(ctx, restored, nopReporter{}); !errors.As(err, &fe) || fe.Fence.Gen != 2 {
		t.Errorf("the other copy: %v", err)
	}
}

// A server the lab removed while it was off finds the mark and stands down.
func TestFenceRemoved(t *testing.T) {
	t.Parallel()
	repo := testRepo(t)
	ctx := context.Background()
	root := t.TempDir()
	paths := config.Paths{StateDir: filepath.Join(root, "state"), HomeDir: filepath.Join(root, "state", "home")}
	writeFile(t, filepath.Join(paths.StateDir, "lab.yaml"), "domain: x")
	deps := JobDeps{Repo: repo, Preparer: &Preparer{}, Paths: paths, Host: "nas",
		DumpState: func(_ context.Context, path string) error { writeFile(t, path, "db"); return nil },
		Plan:      func() (Plan, error) { return LabPlan(paths, nil), nil }}
	if _, err := Run(ctx, deps, nopReporter{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkRemoved(ctx, "nas"); err != nil {
		t.Fatal(err)
	}
	snaps, _ := repo.Snapshots(ctx, TagRoostack, TagRemoved)
	if len(snaps) != 1 || !slices.Contains(snaps[0].Tags, TagRemoved) {
		t.Fatalf("mark: %+v", snaps)
	}
	var fe *FenceError
	if _, err := Run(ctx, deps, nopReporter{}); !errors.As(err, &fe) || fe.Fence.Reason != FenceRemoved {
		t.Errorf("removed: %v", err)
	}
}
