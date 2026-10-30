package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/systemd"
	"roostack.cloud/roostack/internal/usererr"
)

// When a scheduled task is due (SPEC.md §8.1): on its own night, or any
// night once it has missed one, but not twice within 12 hours.
func TestScheduledDue(t *testing.T) {
	tue := time.Date(2026, 10, 27, 2, 30, 0, 0, time.Local) // a Tuesday
	wed := tue.AddDate(0, 0, 1)
	for _, c := range []struct {
		name  string
		now   time.Time
		every string
		day   int
		rec   scheduledRecord
		want  bool
	}{
		{"daily, never run", tue, pkgspec.EveryDay, 0, scheduledRecord{Since: tue}, true},
		{"daily, ran last night", tue, pkgspec.EveryDay, 0, scheduledRecord{Started: tue.AddDate(0, 0, -1)}, true},
		{"daily, run now an hour ago", tue, pkgspec.EveryDay, 0, scheduledRecord{Started: tue.Add(-time.Hour)}, false},
		{"weekly, its night", tue, pkgspec.EveryWeek, int(time.Tuesday), scheduledRecord{Started: tue.AddDate(0, 0, -7)}, true},
		{"weekly, not its night", wed, pkgspec.EveryWeek, int(time.Tuesday), scheduledRecord{Started: tue}, false},
		{"weekly, first night is its night", tue, pkgspec.EveryWeek, int(time.Tuesday), scheduledRecord{Since: tue}, true},
		{"weekly, waiting for its first night", wed, pkgspec.EveryWeek, int(time.Tuesday), scheduledRecord{Since: tue.AddDate(0, 0, -3)}, false},
		{"weekly, missed its night", wed, pkgspec.EveryWeek, int(time.Tuesday), scheduledRecord{Started: tue.AddDate(0, 0, -7)}, true},
		{"weekly, never ran in over a week", wed, pkgspec.EveryWeek, int(time.Tuesday), scheduledRecord{Since: tue.AddDate(0, 0, -8)}, true},
		{"monthly, its day", tue, pkgspec.EveryMonth, 27, scheduledRecord{Started: tue.AddDate(0, -1, 0)}, true},
		{"monthly, not its day", wed, pkgspec.EveryMonth, 27, scheduledRecord{Started: tue}, false},
		{"monthly, missed its day", wed, pkgspec.EveryMonth, 27, scheduledRecord{Started: tue.AddDate(0, -1, 0)}, true},
	} {
		if got := scheduledDue(c.now, c.every, c.day, c.rec); got != c.want {
			t.Errorf("%s: due %v, want %v", c.name, got, c.want)
		}
	}
}

// A server's tasks are spread over the week and the month, the same for
// a task every time.
func TestScheduledDay(t *testing.T) {
	weekdays, days := map[int]bool{}, map[int]bool{}
	for _, svc := range []string{"seafile", "seafile-2", "immich", "forgejo", "paperless", "photos", "files", "wiki"} {
		w, d := scheduledDay(pkgspec.EveryWeek, svc, "gc"), scheduledDay(pkgspec.EveryMonth, svc, "gc")
		if w < 0 || w > 6 || d < 1 || d > 28 {
			t.Fatalf("%s: weekday %d, day %d", svc, w, d)
		}
		if w != scheduledDay(pkgspec.EveryWeek, svc, "gc") {
			t.Fatal("a task's weekday changed")
		}
		weekdays[w], days[d] = true, true
	}
	if len(weekdays) < 3 || len(days) < 3 {
		t.Errorf("not spread: weekdays %v, days %v", weekdays, days)
	}
}

const tasksPackage = `
schema: roostack/v1alpha1
name: files
title: Files
version: 1.0.0-roostack.1
settings:
  - { key: key, title: Key, type: secret, generate: true }
containers:
  app:
    image: example.org/files:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
routes:
  web: { container: app, port: 80, subdomain: files, exposure: private }
hooks:
  - on: schedule
    name: free-space
    title: Free up space
    container: app
    every: day
    timeout: 2h
    env: { WHERE: "{{ .Routes.web.Host }}" }
    secrets: { KEY: key }
    command: [gc]
`

// lockedKV is settings in memory, safe to read while the coordinator's
// goroutine writes them.
type lockedKV struct {
	mu sync.Mutex
	m  map[string]string
}

func (k *lockedKV) Setting(_ context.Context, key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[key], nil
}
func (k *lockedKV) SetSetting(_ context.Context, key, v string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = v
	return nil
}

type nullJournal struct{}

func (nullJournal) Load() ([]string, error) { return nil, nil }
func (nullJournal) Save([]string) error     { return nil }

// gcExec plays the app's garbage collection: it fails while fail is set,
// and waits for release while hold is.
type gcExec struct {
	mu       sync.Mutex
	runs     int
	fail     bool
	hold     chan struct{}
	env      map[string]string
	deadline time.Duration
}

func (f *gcExec) exec(ctx context.Context, _ string, env map[string]string, cmd []string) ([]byte, []byte, error) {
	f.mu.Lock()
	f.runs++
	f.env = env
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	fail, hold := f.fail, f.hold
	f.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if fail {
		return nil, []byte("Removing unused blocks\nthe database is locked, using key " + env["KEY"] + "\n"), errors.New("exit status 1")
	}
	return []byte("Removed 12 blocks\n"), nil, nil
}

func (f *gcExec) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

type unitsState string

func (u *unitsState) Status(_ context.Context, units ...string) ([]systemd.UnitStatus, error) {
	var out []systemd.UnitStatus
	for _, name := range units {
		out = append(out, systemd.UnitStatus{Unit: name, ActiveState: string(*u)})
	}
	return out, nil
}

// Scheduled tasks run on the server's coordinator, record each run, count
// failures in a row for "Needs attention", and never run twice at once or
// while their service isn't running (SPEC.md §8.1).
func TestScheduledTasks(t *testing.T) {
	p, err := pkgspec.Parse([]byte(tasksPackage))
	if err != nil {
		t.Fatal(err)
	}
	lab := config.Default()
	lab.Domain = "lab.example.com"
	in := &instance.Instance{Package: "files", Version: p.Version, Updates: instance.UpdatesNotify,
		Routes: map[string]instance.RouteOverride{"web": {Port: 20040}}}
	kv := &lockedKV{m: map[string]string{}}
	ex := &gcExec{}
	units := unitsState("active")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	coord := &maintenance.Coordinator{Policy: maintenance.DefaultPolicy(), Now: time.Now, Journal: nullJournal{}, Log: log}
	go coord.Run(ctx)
	n := &Local{Lab: lab, State: kv, Exec: ex.exec, Activity: activity.New(), Log: log, Units: &units,
		Secrets: &memSecrets{m: map[string][]byte{"roostack-files-key": []byte("s3cret-key")}},
		Jobs:    Maintenance{Coord: coord}}
	n.assignment.Services = []state.Service{{Package: p, Instance: in}}
	task := func() ScheduledStatus {
		st := n.InstallStatus(ctx, "files").Scheduled
		if len(st) != 1 {
			t.Fatalf("scheduled tasks %+v", st)
		}
		return st[0]
	}
	waitRuns := func(want int) {
		t.Helper()
		var st ScheduledStatus
		for deadline := time.Now().Add(10 * time.Second); ; {
			st = task()
			if ex.count() == want && !st.Queued && !st.Running {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%d runs, want %d; status %+v", ex.count(), want, st)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Tonight's: it runs with its settings and secret, within its timeout.
	if st := task(); !st.Started.IsZero() || st.Every != pkgspec.EveryDay || st.Title != "Free up space" {
		t.Fatalf("before it ran: %+v", st)
	}
	n.QueueScheduled()
	waitRuns(1)
	st := task()
	if !st.OK || st.Failures != 0 || st.Started.IsZero() || st.Finished.Before(st.Started) {
		t.Fatalf("after a run: %+v", st)
	}
	if ex.env["KEY"] != "s3cret-key" || ex.env["WHERE"] != "files.lab.example.com" {
		t.Errorf("env %v", ex.env)
	}
	if ex.deadline < time.Hour+50*time.Minute || ex.deadline > 2*time.Hour {
		t.Errorf("ran with %s to go, want its timeout of 2h", ex.deadline)
	}
	snap, _ := n.Activity.Latest(activity.Scheduled)
	if snap.Title != "Files: Free up space" || !strings.Contains(strings.Join(snap.Lines, "\n"), "Removed 12 blocks") {
		t.Errorf("log %q: %q", snap.Title, snap.Lines)
	}

	// Again tonight: it ran less than 12 hours ago, so it isn't due.
	n.QueueScheduled()
	time.Sleep(50 * time.Millisecond)
	if ex.count() != 1 {
		t.Fatal("it ran twice in one night")
	}

	// Two failures in a row: counted, with what it said last and the
	// secret hidden; then a run that works starts the count again.
	ex.mu.Lock()
	ex.fail = true
	ex.mu.Unlock()
	for i := 1; i <= 2; i++ {
		if err := n.RunScheduled(ctx, "files", "free-space"); err != nil {
			t.Fatal(err)
		}
		waitRuns(1 + i)
		if st := task(); st.OK || st.Failures != i {
			t.Fatalf("after failure %d: %+v", i, st)
		}
	}
	st = task()
	if st.Failures < ScheduledFailuresShown || !strings.HasPrefix(st.Error, "the database is locked, using key [secret]") ||
		strings.Contains(st.Error, "s3cret") {
		t.Errorf("after two failures: %+v", st)
	}
	ex.mu.Lock()
	ex.fail = false
	ex.mu.Unlock()
	n.RunScheduled(ctx, "files", "free-space")
	waitRuns(4)
	if st := task(); !st.OK || st.Failures != 0 || st.Error != "" {
		t.Errorf("after it worked again: %+v", st)
	}

	// Never two at once: while one run is held up, Run now and tonight's
	// queue add nothing, and the status says it's running.
	hold := make(chan struct{})
	ex.mu.Lock()
	ex.hold = hold
	ex.mu.Unlock()
	n.RunScheduled(ctx, "files", "free-space")
	for deadline := time.Now().Add(10 * time.Second); !task().Running; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("it didn't start")
		}
	}
	n.RunScheduled(ctx, "files", "free-space")
	kv.SetSetting(ctx, scheduledKey("files", "free-space"), `{"started":"2026-01-01T00:00:00Z"}`)
	n.QueueScheduled()
	if st := coord.Status(); st.Queued != 0 {
		t.Errorf("%d more queued while it runs", st.Queued)
	}
	ex.mu.Lock()
	ex.hold = nil
	ex.mu.Unlock()
	close(hold)
	waitRuns(5)

	// Only while the service is running and set up.
	runs := ex.count()
	kv.SetSetting(ctx, hookKey("files"), hookPending)
	var ue *usererr.Error
	if err := n.RunScheduled(ctx, "files", "free-space"); !errors.As(err, &ue) || !strings.Contains(ue.Msg, "Files hasn't finished setting up") {
		t.Errorf("run while setting up: %v", err)
	}
	kv.SetSetting(ctx, hookKey("files"), hookDone)
	units = "inactive"
	if err := n.RunScheduled(ctx, "files", "free-space"); !errors.As(err, &ue) || !strings.Contains(ue.Msg, "Files isn't running") {
		t.Errorf("run while stopped: %v", err)
	}
	// Stopped after it was queued: skipped, and why is kept.
	kv.SetSetting(ctx, scheduledKey("files", "free-space"), "")
	units = "active"
	hold = make(chan struct{})
	coord.Submit(maintenance.Job{Name: "a backup", Run: func(context.Context, *maintenance.Handle) error { <-hold; return nil }})
	n.QueueScheduled()
	if st := task(); !st.Queued {
		t.Fatalf("not queued behind the backup: %+v", st)
	}
	n.mu.Lock()
	n.assignment.Services[0].Stop = &state.Stop{}
	n.mu.Unlock()
	close(hold)
	for deadline := time.Now().Add(10 * time.Second); task().Skipped == ""; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("not skipped")
		}
	}
	if st := task(); ex.count() != runs || !strings.Contains(st.Skipped, "the service registry withdrew its version") || st.SkippedAt.IsZero() {
		t.Errorf("skipped: %+v, %d runs", st, ex.count())
	}

	// Unknown tasks and services.
	if err := n.RunScheduled(ctx, "files", "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown task: %v", err)
	}
	if err := n.RunScheduled(ctx, "nope", "free-space"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown service: %v", err)
	}
}

// Roostack stopping while a task runs isn't the task's failure: its record
// stays as it was, so it's due again.
func TestScheduledTaskInterruptedByRoostack(t *testing.T) {
	p, err := pkgspec.Parse([]byte(tasksPackage))
	if err != nil {
		t.Fatal(err)
	}
	in := &instance.Instance{Package: "files", Version: p.Version, Updates: instance.UpdatesNotify,
		Routes: map[string]instance.RouteOverride{"web": {Port: 20040}}}
	kv := &lockedKV{m: map[string]string{}}
	started := make(chan struct{})
	exec := func(ctx context.Context, _ string, _ map[string]string, _ []string) ([]byte, []byte, error) {
		close(started)
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	n := &Local{Lab: config.Default(), State: kv, Exec: exec, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Secrets: &memSecrets{m: map[string][]byte{"roostack-files-key": []byte("k")}}}
	n.assignment.Services = []state.Service{{Package: p, Instance: in}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- n.runScheduled(ctx, "files", "free-space") }()
	<-started
	cancel()
	<-done
	if rec := n.scheduledRecord(context.Background(), "files", "free-space"); rec.Failures != 0 || !rec.Started.IsZero() {
		t.Errorf("record after Roostack stopped: %+v", rec)
	}
}

// A task still running after its timeout is ended by restarting the
// container it ran in, since the interrupt doesn't reach processes it
// started (SPEC.md §8.1; docs/ASSUMPTIONS.md Y2).
func TestScheduledTaskTimeoutRestartsItsContainer(t *testing.T) {
	p, err := pkgspec.Parse([]byte(tasksPackage))
	if err != nil {
		t.Fatal(err)
	}
	in := &instance.Instance{Package: "files", Version: p.Version, Updates: instance.UpdatesNotify,
		Routes: map[string]instance.RouteOverride{"web": {Port: 20040}}}
	exec := func(ctx context.Context, _ string, _ map[string]string, _ []string) ([]byte, []byte, error) {
		<-ctx.Done()
		return []byte("Removing unused blocks\n"), nil, errors.New("signal: interrupt")
	}
	units := &fakeUnits{active: true}
	n := &Local{Lab: config.Default(), State: &lockedKV{m: map[string]string{}}, Exec: exec, Units: units,
		Reconciler: &reconcile.Reconciler{Systemd: units}, Activity: activity.New(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), scheduledTimeout: 20 * time.Millisecond,
		Secrets: &memSecrets{m: map[string][]byte{"roostack-files-key": []byte("k")}}}
	n.assignment.Services = []state.Service{{Package: p, Instance: in}}
	err = n.runScheduled(context.Background(), "files", "free-space")
	if err == nil || !strings.Contains(err.Error(), "so Roostack restarted its app container to stop it") {
		t.Errorf("error %v", err)
	}
	if len(units.restarted) != 1 || units.restarted[0] != "roostack-files-app.service" {
		t.Errorf("restarted %v", units.restarted)
	}
	rec := n.scheduledRecord(context.Background(), "files", "free-space")
	if rec.Failures != 1 || rec.OK {
		t.Errorf("record %+v", rec)
	}
	snap, _ := n.Activity.Latest(activity.Scheduled)
	if !strings.Contains(strings.Join(snap.Lines, "\n"), "Roostack is restarting its app container, so that nothing it started keeps running.") {
		t.Errorf("log %q", snap.Lines)
	}
}
