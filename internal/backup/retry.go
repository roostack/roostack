package backup

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// Trying failed backups again (SPEC.md §6.9): only the destinations that
// failed, and only for failures that may fix themselves (storage that
// couldn't be reached, a timeout): after retryAfter[0], then
// retryAfter[1], then they wait for the next nightly backup. A
// destination whose drive isn't plugged in is backed up as soon as it is
// (checked every minute). Failures that need someone (a full
// destination, a wrong key) aren't retried. The state is in memory: after
// a restart, the nightly backup tries everything again anyway.

// retryAfter is how long after each failure the next try is.
var retryAfter = []time.Duration{time.Hour, 3 * time.Hour}

// Retry is a failed destination's next try, for the UI.
type Retry struct {
	// At is when it's tried again; zero while waiting for its drive.
	At time.Time
	// Drive: it's tried as soon as its drive is plugged in.
	Drive bool
}

type retryState struct {
	Retry
	next int // index into retryAfter for the failure after this try
}

// permanent reports failures that trying again won't fix.
func permanent(err error) bool {
	if errors.Is(err, ErrWrongKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"wrong password", "no key found", "no space left", "insufficient storage", "quota",
		"permission denied", "access denied", "forbidden", "401 unauthorized", "403"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// updateRetries records a run's results (nil for a destination that
// worked) and plans the next tries. full: the nightly backup or "Back up
// now", which starts every destination's retries afresh.
func (s *Service) updateRetries(now time.Time, results map[string]error, full bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retries == nil || full {
		s.retries = map[string]*retryState{}
	}
	for name, err := range results {
		prev := s.retries[name]
		delete(s.retries, name)
		var drive *DriveError
		switch {
		case err == nil, errors.Is(err, context.Canceled), permanent(err):
		case errors.As(err, &drive):
			s.retries[name] = &retryState{Retry: Retry{Drive: true}}
		default:
			n := 0
			if prev != nil && !prev.Drive {
				n = prev.next
			}
			if n < len(retryAfter) {
				s.retries[name] = &retryState{Retry: Retry{At: now.Add(retryAfter[n])}, next: n + 1}
			}
		}
	}
}

// retryFor is destination name's next try, if any.
func (s *Service) retryFor(name string) *Retry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.retries[name]; st != nil {
		r := st.Retry
		return &r
	}
	return nil
}

// dueRetries are the destinations to try again now: their time has come,
// or their drive is back.
func (s *Service) dueRetries(now time.Time) []string {
	s.mu.Lock()
	var due []string
	var drives []string
	for name, st := range s.retries {
		if s.Deps == nil || name != "" && !slices.ContainsFunc(s.Deps.Copies, func(c Copy) bool { return c.Name == name }) {
			delete(s.retries, name) // no longer a destination
			continue
		}
		switch {
		case st.Drive:
			drives = append(drives, name)
		case !now.Before(st.At):
			due = append(due, name)
		}
	}
	s.mu.Unlock()
	for _, name := range drives {
		if s.Deps != nil && precheck(s.precheckOf(name)) == nil {
			due = append(due, name)
		}
	}
	slices.Sort(due)
	return due
}

// precheckOf is destination name's check (its drive).
func (s *Service) precheckOf(name string) func() error {
	if name == "" {
		return s.Deps.Precheck
	}
	for _, c := range s.Deps.Copies {
		if c.Name == name {
			return c.Precheck
		}
	}
	return nil
}

// RetryDue starts a backup of the destinations due another try, unless a
// backup is already queued or running. Call it every minute.
func (s *Service) RetryDue(now time.Time) {
	if !s.Configured() {
		return
	}
	if due := s.dueRetries(now); len(due) > 0 {
		if s.loadFence(context.Background()) == nil { // stood down: nothing to retry
			s.submit(due)
		}
	}
}

// WatchRetries calls RetryDue every minute until ctx ends.
func (s *Service) WatchRetries(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.RetryDue(now)
		}
	}
}
