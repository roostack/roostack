package backup

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

// Failed destinations are tried again after an hour, then three, then
// left for the nightly backup; failures that need someone aren't; a
// missing drive is tried as soon as it's back.
func TestRetries(t *testing.T) {
	drive := errors.New("BACKUP isn't connected")
	s := &Service{Deps: &JobDeps{
		Precheck: func() error { return drive },
		Copies:   []Copy{{Name: "provider:1"}, {Name: "server:dev2"}},
	}}
	now := time.Date(2026, 10, 3, 2, 0, 0, 0, time.UTC)
	unreachable := errors.New("couldn't reach dev2's disk in 2m0s")
	s.updateRetries(now, map[string]error{
		"":            &DriveError{drive},
		"provider:1":  errors.New("restic backup: exit status 1: Fatal: wrong password or no key found"),
		"server:dev2": unreachable,
	}, true)
	if r := s.retryFor("server:dev2"); r == nil || !r.At.Equal(now.Add(time.Hour)) {
		t.Errorf("first retry: %+v", r)
	}
	if r := s.retryFor("provider:1"); r != nil {
		t.Errorf("a wrong key isn't retried: %+v", r)
	}
	if r := s.retryFor(""); r == nil || !r.Drive {
		t.Errorf("a missing drive waits for it: %+v", r)
	}
	if due := s.dueRetries(now.Add(30 * time.Minute)); len(due) != 0 {
		t.Errorf("nothing due yet: %v", due)
	}
	drive = nil // plugged in
	if due := s.dueRetries(now.Add(time.Hour)); !slices.Equal(due, []string{"", "server:dev2"}) {
		t.Errorf("due: %v", due)
	}
	// The retry fails again: three hours; then no more.
	later := now.Add(time.Hour)
	s.updateRetries(later, map[string]error{"": nil, "server:dev2": unreachable}, false)
	if r := s.retryFor("server:dev2"); r == nil || !r.At.Equal(later.Add(3*time.Hour)) {
		t.Errorf("second retry: %+v", r)
	}
	if s.retryFor("") != nil {
		t.Error("worked: no retry")
	}
	s.updateRetries(later.Add(3*time.Hour), map[string]error{"server:dev2": unreachable}, false)
	if r := s.retryFor("server:dev2"); r != nil {
		t.Errorf("after two retries it waits for the nightly backup: %+v", r)
	}
	// The nightly backup starts afresh; a paused run plans nothing.
	s.updateRetries(now.Add(24*time.Hour), map[string]error{"server:dev2": unreachable}, true)
	if r := s.retryFor("server:dev2"); r == nil || !r.At.Equal(now.Add(25*time.Hour)) {
		t.Errorf("nightly: %+v", r)
	}
	s.updateRetries(now, map[string]error{"provider:1": context.Canceled}, false)
	if s.retryFor("provider:1") != nil {
		t.Error("a run stopped for a restart isn't a failure")
	}
	// A backup that's been removed is forgotten.
	s.Deps.Copies = s.Deps.Copies[:1]
	if due := s.dueRetries(now.Add(48 * time.Hour)); len(due) != 0 || s.retryFor("server:dev2") != nil {
		t.Errorf("removed destination: %v", due)
	}
}
