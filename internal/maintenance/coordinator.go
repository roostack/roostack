package maintenance

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrInterrupted is returned for a job paused for a reboot.
var ErrInterrupted = errors.New("interrupted for reboot")

// Job is one disruptive operation.
type Job struct {
	Name string
	// Run does the work. It must return promptly once ctx is cancelled.
	Run func(ctx context.Context, h *Handle) error
	// Resumable jobs are recorded when interrupted and queued again after
	// the reboot (e.g. a backup: restic continues where it stopped).
	Resumable bool
}

// Handle lets a running job report its state.
type Handle struct {
	c *Coordinator
}

// SetInterruptible marks whether the job can be safely paused right now.
// Jobs start as not interruptible.
func (h *Handle) SetInterruptible(v bool) {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	if h.c.running != nil {
		h.c.running.Interruptible = v
	}
}

// SetProgress reports progress from 0 to 100.
func (h *Handle) SetProgress(p int) {
	h.c.mu.Lock()
	defer h.c.mu.Unlock()
	if h.c.running != nil {
		h.c.running.Progress = p
	}
}

// Journal persists interrupted resumable jobs across a reboot.
type Journal interface {
	Load() ([]string, error)
	Save(names []string) error
}

// Coordinator runs job chains one at a time and handles reboots.
type Coordinator struct {
	Policy Policy
	Now    func() time.Time
	// Reboot asks the host to reboot (via mylab-agent).
	Reboot  func() error
	Journal Journal
	// Resume rebuilds a job by name after a reboot.
	Resume func(name string) (Job, bool)
	// Ticks drive periodic reboot checks and carry the current time
	// (time.Ticker.C in production; tests send them by hand).
	Ticks <-chan time.Time
	Log   *slog.Logger

	mu           sync.Mutex
	queue        [][]Job
	running      *Running
	pendingSince time.Time
	wake         chan struct{}
}

// Submit queues a chain of jobs. They run in order; if one fails, the rest
// are skipped (e.g. no automatic updates without a fresh backup).
func (c *Coordinator) Submit(chain ...Job) {
	c.mu.Lock()
	c.queue = append(c.queue, chain)
	c.mu.Unlock()
	c.poke()
}

// RequestReboot records that the host needs a reboot (e.g. after a kernel
// update). The first request's time counts towards InterruptAfter.
func (c *Coordinator) RequestReboot() {
	c.mu.Lock()
	if c.pendingSince.IsZero() {
		c.pendingSince = c.Now()
	}
	c.mu.Unlock()
	c.poke()
}

// Status is shown in the UI.
type Status struct {
	Running *Running
	Queued  int
	Reboot  Decision
}

// Status reports what the coordinator is doing and why.
func (c *Coordinator) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Status{Queued: len(c.queue), Reboot: Decide(c.Policy, c.Now(), c.pendingSince, c.running)}
	if c.running != nil {
		r := *c.running
		st.Running = &r
	}
	return st
}

func (c *Coordinator) poke() {
	c.mu.Lock()
	if c.wake == nil {
		c.wake = make(chan struct{}, 1)
	}
	w := c.wake
	c.mu.Unlock()
	select {
	case w <- struct{}{}:
	default:
	}
}

// Run processes jobs until ctx ends or the host reboots. Interrupted jobs
// from before the last reboot are queued first.
func (c *Coordinator) Run(ctx context.Context) error {
	c.poke() // make sure wake exists
	if err := c.resumeInterrupted(); err != nil {
		c.Log.Error("resuming interrupted jobs", "err", err)
	}
	for {
		if rebooted, err := c.maybeReboot(); rebooted || err != nil {
			return err
		}
		if chain, ok := c.next(); ok {
			c.runChain(ctx, chain)
			continue
		}
		c.mu.Lock()
		wake := c.wake
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil
		case <-c.Ticks:
		case <-wake:
		}
	}
}

func (c *Coordinator) next() ([]Job, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.queue) == 0 {
		return nil, false
	}
	chain := c.queue[0]
	c.queue = c.queue[1:]
	return chain, true
}

// maybeReboot reboots if the policy says so and nothing is running or
// queued. Queued work runs first: the queue lives in memory, and the nightly
// order is backup, then updates, then reboot (SPEC.md §3.2).
func (c *Coordinator) maybeReboot() (bool, error) {
	c.mu.Lock()
	d := Decide(c.Policy, c.Now(), c.pendingSince, nil)
	if d.Action != Reboot || len(c.queue) > 0 {
		c.mu.Unlock()
		return false, nil
	}
	c.mu.Unlock()
	c.Log.Info(d.Reason)
	return true, c.Reboot()
}

func (c *Coordinator) runChain(ctx context.Context, chain []Job) {
	for i, job := range chain {
		err := c.runJob(ctx, job)
		switch {
		case err == nil:
			continue
		case errors.Is(err, ErrInterrupted):
			c.Log.Info("job paused for reboot", "job", job.Name)
		default:
			c.Log.Error("job failed", "job", job.Name, "err", err)
		}
		for _, skipped := range chain[i+1:] {
			c.Log.Warn("skipping job because an earlier step did not finish", "job", skipped.Name, "after", job.Name)
		}
		return
	}
}

func (c *Coordinator) runJob(ctx context.Context, job Job) error {
	jctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	c.running = &Running{Name: job.Name, Progress: -1}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.running = nil
		c.mu.Unlock()
	}()

	c.Log.Info("job started", "job", job.Name)
	done := make(chan error, 1)
	go func() { done <- job.Run(jctx, &Handle{c: c}) }()
	for {
		select {
		case err := <-done:
			if err == nil {
				c.Log.Info("job finished", "job", job.Name)
			}
			return err
		case now := <-c.Ticks:
			// Decide at the tick's time (a time.Ticker sends the current
			// time), so the decision matches the moment it was triggered.
			c.mu.Lock()
			d := Decide(c.Policy, now, c.pendingSince, c.running)
			c.mu.Unlock()
			if d.Action != InterruptAndReboot {
				continue
			}
			c.Log.Info(d.Reason)
			if job.Resumable {
				if err := c.Journal.Save([]string{job.Name}); err != nil {
					// Without a record the job wouldn't resume; keep waiting.
					c.Log.Error("saving interrupted job", "err", err)
					continue
				}
			}
			cancel()
			<-done
			return ErrInterrupted
		}
	}
}

func (c *Coordinator) resumeInterrupted() error {
	names, err := c.Journal.Load()
	if err != nil || len(names) == 0 {
		return err
	}
	for _, name := range names {
		if job, ok := c.Resume(name); ok {
			c.Log.Info("resuming job interrupted by reboot", "job", name)
			c.mu.Lock()
			c.queue = append([][]Job{{job}}, c.queue...)
			c.mu.Unlock()
		}
	}
	return c.Journal.Save(nil)
}
