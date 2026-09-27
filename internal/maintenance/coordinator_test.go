package maintenance

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"sync"
	"testing"
	"time"
)

type memJournal struct {
	mu    sync.Mutex
	names []string
}

func (j *memJournal) Load() ([]string, error) { j.mu.Lock(); defer j.mu.Unlock(); return j.names, nil }
func (j *memJournal) Save(n []string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.names = n
	return nil
}

type env struct {
	c       *Coordinator
	ticks   chan time.Time
	mu      sync.Mutex
	now     time.Time
	reboots int
	log     []string
	journal *memJournal
	done    chan error
	cancel  context.CancelFunc
}

func newEnv(t *testing.T, now time.Time) *env {
	e := &env{now: now, ticks: make(chan time.Time), journal: &memJournal{}}
	e.c = &Coordinator{
		Policy:  DefaultPolicy(),
		Now:     func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		Reboot:  func() error { e.mu.Lock(); e.reboots++; e.mu.Unlock(); return nil },
		Journal: e.journal,
		Resume: func(name string) (Job, bool) {
			return e.job(name+" (resumed)", nil), true
		},
		Ticks: e.ticks,
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return e
}

func (e *env) start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan error, 1)
	go func() { e.done <- e.c.Run(ctx) }()
}

func (e *env) rebootCount() int { e.mu.Lock(); defer e.mu.Unlock(); return e.reboots }

func (e *env) setNow(t time.Time) { e.mu.Lock(); e.now = t; e.mu.Unlock() }

func (e *env) record(s string) { e.mu.Lock(); e.log = append(e.log, s); e.mu.Unlock() }

func (e *env) events() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.log...)
}

// job returns a job that records its name and returns err.
func (e *env) job(name string, err error) Job {
	return Job{Name: name, Run: func(context.Context, *Handle) error { e.record(name); return err }}
}

// waitFor polls until cond is true (the coordinator runs in a goroutine).
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestChainStopsOnFailure(t *testing.T) {
	e := newEnv(t, at(2, 12, 0))
	e.start()
	defer e.cancel()
	e.c.Submit(e.job("backup", errors.New("disk full")), e.job("update vaultwarden", nil))
	e.c.Submit(e.job("backup", nil), e.job("update immich", nil))
	waitFor(t, "jobs", func() bool { return len(e.events()) == 3 })
	want := []string{"backup", "backup", "update immich"}
	if got := e.events(); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %v, want %v (updates must not run after a failed backup)", got, want)
	}
}

func TestRebootWhenIdleInWindow(t *testing.T) {
	e := newEnv(t, at(2, 12, 0))
	e.start()
	e.c.RequestReboot()
	e.ticks <- time.Time{} // outside the window: nothing happens
	if e.rebootCount() != 0 {
		t.Fatal("rebooted outside the window")
	}
	e.setNow(at(3, 4, 5))
	e.ticks <- time.Time{}
	if err := <-e.done; err != nil {
		t.Fatal(err)
	}
	if e.rebootCount() != 1 {
		t.Errorf("reboots = %d, want 1", e.rebootCount())
	}
}

func TestQueuedJobsRunBeforeReboot(t *testing.T) {
	e := newEnv(t, at(2, 4, 0))
	e.c.RequestReboot()
	e.c.Submit(e.job("backup", nil), e.job("update immich", nil))
	e.start()
	if err := <-e.done; err != nil {
		t.Fatal(err)
	}
	if got := e.events(); len(got) != 2 || e.rebootCount() != 1 {
		t.Errorf("want both jobs then a reboot; ran %v, reboots %d", got, e.rebootCount())
	}
}

func TestRebootWaitsForJob(t *testing.T) {
	e := newEnv(t, at(2, 4, 0))
	release := make(chan struct{})
	e.start()
	// A long job that is never interruptible (like a restore).
	e.c.Submit(Job{Name: "restore", Run: func(ctx context.Context, h *Handle) error {
		e.record("restore started")
		<-release
		return nil
	}})
	waitFor(t, "restore to start", func() bool { return len(e.events()) == 1 })
	e.c.RequestReboot()
	e.setNow(at(3, 4, 30)) // pending for over 24h, in the window
	e.ticks <- at(3, 4, 30)
	if st := e.c.Status(); st.Reboot.Action != Wait || e.rebootCount() != 0 {
		t.Fatalf("must not reboot during a restore: %+v", st.Reboot)
	}
	close(release)
	if err := <-e.done; err != nil {
		t.Fatal(err)
	}
	if e.rebootCount() != 1 {
		t.Errorf("should reboot once the restore finishes")
	}
}

func TestInterruptResumableJob(t *testing.T) {
	e := newEnv(t, at(2, 4, 0))
	e.start()
	e.c.Submit(Job{Name: "backup", Resumable: true, Run: func(ctx context.Context, h *Handle) error {
		h.SetInterruptible(false) // prepare step: dumps
		h.SetInterruptible(true)  // uploading: safe to pause
		h.SetProgress(40)
		e.record("uploading")
		<-ctx.Done()
		return ctx.Err()
	}}, e.job("update vaultwarden", nil))
	waitFor(t, "upload", func() bool { return len(e.events()) == 1 })
	e.c.RequestReboot()

	e.ticks <- at(2, 4, 0) // pending only a moment: keep waiting
	if st := e.c.Status(); st.Running == nil || st.Running.Progress != 40 {
		t.Fatalf("status: %+v", st)
	}
	e.setNow(at(3, 4, 1)) // 24h later, in the window
	e.ticks <- at(3, 4, 1)
	if err := <-e.done; err != nil {
		t.Fatal(err)
	}
	if e.rebootCount() != 1 {
		t.Errorf("reboots = %d, want 1", e.rebootCount())
	}
	if names, _ := e.journal.Load(); !reflect.DeepEqual(names, []string{"backup"}) {
		t.Errorf("journal = %v, want the interrupted backup", names)
	}
	if got := e.events(); len(got) != 1 {
		t.Errorf("the rest of the chain must not run after an interruption: %v", got)
	}

	// After the reboot, a new coordinator resumes the backup first.
	e2 := newEnv(t, at(3, 4, 10))
	e2.journal = e.journal
	e2.c.Journal = e.journal
	e2.start()
	defer e2.cancel()
	waitFor(t, "resume", func() bool { return len(e2.events()) == 1 })
	if got := e2.events()[0]; got != "backup (resumed)" {
		t.Errorf("first job after reboot = %q", got)
	}
	if names, _ := e.journal.Load(); len(names) != 0 {
		t.Errorf("journal should be cleared after resuming, got %v", names)
	}
}

func TestHold(t *testing.T) {
	e := newEnv(t, at(2, 4, 0))
	release := make(chan struct{})
	e.c.Submit(Job{Name: "backup", Run: func(context.Context, *Handle) error {
		e.record("backup")
		<-release
		return nil
	}})
	e.start()
	defer e.cancel()
	waitFor(t, "backup to start", func() bool { return len(e.events()) == 1 })
	e.c.Hold()
	e.c.Submit(e.job("updates", nil))
	e.c.RequestReboot()
	close(release)
	waitFor(t, "backup to finish", func() bool { return e.c.Status().Running == nil })
	e.ticks <- time.Time{}
	if got := e.events(); len(got) != 1 || e.rebootCount() != 0 {
		t.Errorf("held: ran %v, reboots %d; want the running job to finish and nothing more", got, e.rebootCount())
	}
}
