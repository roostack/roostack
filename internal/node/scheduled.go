package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// Scheduled hooks (SPEC.md §8.1, "Scheduled tasks"): upkeep a package asks
// for every day, week or month, such as Seafile's garbage collection. Each
// server runs its own services' tasks, in the night's maintenance: the
// nightly schedule queues the ones due after the backup and OS updates, on
// the server's maintenance coordinator, so a task never overlaps a backup,
// a restore, or an update, and tasks run one at a time. A task runs only
// while its service is running; one that didn't run is due again the next
// night.

// scheduledRecord is what a server remembers of one scheduled hook, as a
// runtime setting (scheduledKey).
type scheduledRecord struct {
	// Since is when Roostack first saw the hook, which a first weekly or
	// monthly run counts from when it missed its night.
	Since time.Time `json:"since"`
	// Started and Finished are the last run's; OK and Error its result.
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	OK       bool      `json:"ok,omitempty"`
	Error    string    `json:"error,omitempty"`
	// Failures is how many runs in a row have failed.
	Failures int `json:"failures,omitempty"`
	// Skipped is why the last night it was due it didn't run ("" if it
	// ran since), and SkippedAt when.
	Skipped   string    `json:"skipped,omitempty"`
	SkippedAt time.Time `json:"skipped_at,omitzero"`
}

// ScheduledStatus is one scheduled hook of a service, for its page and
// "Needs attention" (HookStatus.Scheduled).
type ScheduledStatus struct {
	Name, Title, Description string
	// Every is day, week or month; Day is its night: a weekday (0 is
	// Sunday) for a weekly task, a day of the month for a monthly one.
	Every string
	Day   int
	// The last run: when it started and finished, whether it worked, and
	// how many runs in a row have failed.
	Started, Finished time.Time
	OK                bool
	Error             string
	Failures          int
	// Skipped is why it didn't run the last night it was due.
	Skipped   string
	SkippedAt time.Time
	// Queued: waiting for its turn on the server's coordinator; Running:
	// running now.
	Queued, Running bool
}

// ScheduledFailuresShown is how many runs in a row must fail before a
// task goes under "Needs attention": one failure may be a bad night (the
// app busy, a disk briefly full), and it's tried again the next.
const ScheduledFailuresShown = 2

func scheduledKey(service, hook string) string {
	return "hooks." + service + ".scheduled." + hook
}

// scheduledDay is the night a weekly or monthly task runs: a weekday, or
// a day of the month from 1 to 28 (so every month has it), from a hash of
// its service and name, so a server's tasks are spread over the week and
// the month rather than all on one night.
func scheduledDay(every, service, hook string) int {
	h := fnv.New32a()
	h.Write([]byte(service + "/" + hook))
	switch every {
	case pkgspec.EveryWeek:
		return int(h.Sum32() % 7)
	case pkgspec.EveryMonth:
		return int(h.Sum32()%28) + 1
	}
	return 0
}

// scheduledDue says whether a task is due in the night's maintenance that
// starts at now: on its own night, unless it ran in the last 12 hours
// (Run now); on any night once it has missed one, since its server was off
// or its service stopped then.
func scheduledDue(now time.Time, every string, day int, rec scheduledRecord) bool {
	if !rec.Started.IsZero() && now.Sub(rec.Started) < 12*time.Hour {
		return false
	}
	from := rec.Started
	if from.IsZero() {
		from = rec.Since
	}
	switch every {
	case pkgspec.EveryDay:
		return true
	case pkgspec.EveryWeek:
		return int(now.Weekday()) == day || (!from.IsZero() && now.After(from.AddDate(0, 0, 7).Add(12*time.Hour)))
	case pkgspec.EveryMonth:
		return now.Day() == day || (!from.IsZero() && now.After(from.AddDate(0, 1, 0).Add(12*time.Hour)))
	}
	return false
}

func (n *Local) scheduledRecord(ctx context.Context, service, hook string) scheduledRecord {
	var rec scheduledRecord
	if n.State == nil {
		return rec
	}
	if v, err := n.State.Setting(ctx, scheduledKey(service, hook)); err == nil && v != "" {
		json.Unmarshal([]byte(v), &rec)
	}
	return rec
}

func (n *Local) setScheduledRecord(ctx context.Context, service, hook string, rec scheduledRecord) {
	b, _ := json.Marshal(rec)
	if err := n.State.SetSetting(ctx, scheduledKey(service, hook), string(b)); err != nil {
		n.Log.Error("keeping a scheduled task's run", "service", service, "task", hook, "err", err)
	}
}

// forgetScheduled forgets a service's scheduled tasks' runs, as it's gone
// from this server (n.mu held, as Apply has it).
func (n *Local) forgetScheduled(s state.Service) {
	for _, h := range s.Package.HooksOn(pkgspec.HookSchedule) {
		n.State.SetSetting(context.Background(), scheduledKey(s.Name(), h.Name), "")
	}
}

// scheduledStatus is each of a service's scheduled tasks.
func (n *Local) scheduledStatus(ctx context.Context, s state.Service) []ScheduledStatus {
	var out []ScheduledStatus
	for _, h := range s.Package.HooksOn(pkgspec.HookSchedule) {
		rec := n.scheduledRecord(ctx, s.Name(), h.Name)
		st := ScheduledStatus{Name: h.Name, Title: h.Title, Description: h.Description, Every: h.Every,
			Day: scheduledDay(h.Every, s.Name(), h.Name), Started: rec.Started, Finished: rec.Finished, OK: rec.OK,
			Error: rec.Error, Failures: rec.Failures, Skipped: rec.Skipped, SkippedAt: rec.SkippedAt}
		n.mu.Lock()
		switch n.scheduled[scheduledKey(s.Name(), h.Name)] {
		case scheduledQueued:
			st.Queued = true
		case scheduledRunning:
			st.Running = true
		}
		n.mu.Unlock()
		out = append(out, st)
	}
	return out
}

// What a task is doing on the server's coordinator (Local.scheduled).
const (
	scheduledQueued  = 1
	scheduledRunning = 2
)

// QueueScheduled queues the scheduled tasks due tonight on the server's
// coordinator, after the nightly backup and OS updates the schedule has
// queued already. It's called once a night, at the nightly backup's time.
func (n *Local) QueueScheduled() {
	if n.State == nil || n.Exec == nil || n.Jobs.Coord == nil {
		return
	}
	ctx := context.Background()
	now := time.Now()
	for _, s := range n.services() {
		for _, h := range s.Package.HooksOn(pkgspec.HookSchedule) {
			rec := n.scheduledRecord(ctx, s.Name(), h.Name)
			if rec.Since.IsZero() {
				rec.Since = now
				n.setScheduledRecord(ctx, s.Name(), h.Name, rec)
			}
			if !scheduledDue(now, h.Every, scheduledDay(h.Every, s.Name(), h.Name), rec) {
				continue
			}
			n.queueScheduled(s, h)
		}
	}
}

// queueScheduled queues one task, unless it's queued or running already
// (never two runs of one task at once). It says whether it queued it.
func (n *Local) queueScheduled(s state.Service, h *pkgspec.Hook) bool {
	key := scheduledKey(s.Name(), h.Name)
	n.mu.Lock()
	if n.scheduled[key] != 0 {
		n.mu.Unlock()
		return false
	}
	if n.scheduled == nil {
		n.scheduled = map[string]int{}
	}
	n.scheduled[key] = scheduledQueued
	n.mu.Unlock()
	name, hook := s.Name(), h.Name
	n.Jobs.Coord.Submit(maintenance.Job{
		Name: s.Title() + "'s scheduled task (" + h.Title + ")",
		Run: func(ctx context.Context, _ *maintenance.Handle) error {
			n.setScheduled(key, scheduledRunning)
			defer n.setScheduled(key, 0)
			return n.runScheduled(ctx, name, hook)
		},
	})
	return true
}

// lastLine is the last line a command printed, "" if none.
func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func (n *Local) setScheduled(key string, v int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if v == 0 {
		delete(n.scheduled, key)
	} else {
		n.scheduled[key] = v
	}
}

// RunScheduled runs one of a service's scheduled tasks as soon as the
// server's coordinator is free (Run now, or Try again after it failed).
func (n *Local) RunScheduled(ctx context.Context, service, hook string) error {
	s, ok := n.service(service)
	if !ok || n.Exec == nil || n.State == nil || n.Jobs.Coord == nil {
		return ErrNotFound
	}
	h := s.Package.Scheduled(hook)
	if h == nil {
		return ErrNotFound
	}
	if why := n.scheduledBlocked(ctx, s); why != "" {
		return usererr.New("%s can't run now: %s.", h.Title, why)
	}
	n.queueScheduled(s, h) // already queued or running: that run will do
	return nil
}

// scheduledBlocked says why a service's scheduled tasks can't run now, ""
// if they can: only while it's running, set up, and not stopped or held
// by Roostack.
func (n *Local) scheduledBlocked(ctx context.Context, s state.Service) string {
	title := s.Title()
	switch {
	case s.Stop != nil:
		return title + " is stopped: the service registry withdrew its version"
	case s.Instance != nil && s.Instance.AwaitingData:
		return title + " is waiting for its data to be brought in"
	case s.Instance != nil && s.Instance.Held != nil:
		return title + " is stopped after a restore that didn't finish"
	}
	if v, err := n.State.Setting(ctx, hookKey(s.Name())); err == nil && v != "" && v != hookDone {
		return title + " hasn't finished setting up"
	}
	if n.Units == nil {
		return ""
	}
	out, err := state.Output(n.currentLab(), s)
	if err != nil {
		return title + "'s files couldn't be worked out"
	}
	units, err := n.Units.Status(ctx, out.Units...)
	if err != nil || Summarize(units) != "running" {
		return title + " isn't running"
	}
	return ""
}

// runScheduled runs a task once, as a coordinator job: it records the run,
// or why it was skipped, and keeps its log as history (activity.Scheduled).
func (n *Local) runScheduled(ctx context.Context, service, hook string) (err error) {
	s, ok := n.service(service)
	if !ok {
		return nil // removed while it waited
	}
	h := s.Package.Scheduled(hook)
	if h == nil {
		return nil // updated to a version without it
	}
	rec := n.scheduledRecord(ctx, service, hook)
	if why := n.scheduledBlocked(ctx, s); why != "" {
		n.Log.Info("scheduled task skipped", "service", service, "task", hook, "why", why)
		rec.Skipped, rec.SkippedAt = why, time.Now()
		n.setScheduledRecord(ctx, service, hook, rec)
		return nil
	}
	act := n.Activity.Start(activity.Scheduled, s.Title()+": "+h.Title)
	start := time.Now()
	defer func() {
		if ctx.Err() != nil {
			// Roostack is stopping (a restart): not the task's failure,
			// and it's due again as before.
			act.Printf("Roostack stopped before it finished; it will run again the next night it's due.")
			act.Finish(err)
			return
		}
		rec.Started, rec.Finished, rec.OK, rec.Skipped, rec.SkippedAt = start, time.Now(), err == nil, "", time.Time{}
		rec.Error = ""
		if err != nil {
			rec.Error = err.Error()
			rec.Failures++
		} else {
			rec.Failures = 0
		}
		n.setScheduledRecord(context.WithoutCancel(ctx), service, hook, rec)
		act.Finish(err)
	}()
	timeout := h.RunTimeout()
	act.Step("Running in its %s container, for up to %s", h.Container, words.ExactDuration(timeout))
	run, err := generate.Hook(n.currentLab(), s.Package, s.Instance, h, nil)
	if err != nil {
		return err
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := n.hookSecrets(rctx, run, nil); err != nil {
		return err
	}
	out, err := n.execHook(rctx, run)
	act.Write(out)
	switch {
	case errors.Is(rctx.Err(), context.DeadlineExceeded):
		// The interrupt reaches the command's own process, not processes
		// it started (docs/ASSUMPTIONS.md Y2), so don't say it stopped.
		return fmt.Errorf("it was still running after %s, so Roostack interrupted it", words.ExactDuration(timeout))
	case err != nil:
		// The last thing it said is usually why; the log has the rest.
		if line := lastLine(out); line != "" {
			return fmt.Errorf("%s (%w)", line, err)
		}
		return err
	}
	return nil
}
