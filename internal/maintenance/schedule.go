package maintenance

import (
	"context"
	"hash/fnv"
	"time"
)

// Default schedule (SPEC.md §3.2, §6.9): back up nightly before the 04:00
// reboot window; prune and check the backup repository weekly.
const (
	DefaultBackupAt      = 2 * time.Hour
	DefaultMaintenanceAt = 3 * time.Hour
	DefaultMaintenanceOn = time.Sunday
)

// Spread is how far Offset spreads servers' nightly work past the
// schedule's times.
const Spread = 30 * time.Minute

// Offset is how long after the schedule's times (the backup, the weekly
// maintenance, and the restart window) the server with node ID id starts
// them: a whole number of minutes under Spread, the same for that server
// every night. Many labs share a time zone, and without it every one of
// them would reach shared services (backup storage, Roostack Cloud, the
// service registry, and Debian's mirrors) in the same minute, and restart
// together.
func Offset(id string) time.Duration {
	h := fnv.New32a()
	h.Write([]byte(id))
	return time.Duration(h.Sum32()%uint32(Spread/time.Minute)) * time.Minute
}

// NextDaily returns the next time of day `at` (local time) after now.
// Using time.Date keeps it at the same wall-clock time across DST changes.
func NextDaily(now time.Time, at time.Duration) time.Time {
	h, m := int(at.Hours()), int(at.Minutes())%60
	next := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, now.Location())
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month(), now.Day()+1, h, m, 0, 0, now.Location())
	}
	return next
}

// NextWeekly returns the next `weekday` at time of day `at` after now.
func NextWeekly(now time.Time, weekday time.Weekday, at time.Duration) time.Time {
	next := NextDaily(now, at)
	for next.Weekday() != weekday {
		next = time.Date(next.Year(), next.Month(), next.Day()+1, next.Hour(), next.Minute(), 0, 0, next.Location())
	}
	return next
}

// Every calls fire at each time returned by next, until ctx ends.
//
// It looks at the clock at least every checkEvery rather than waiting once
// for the whole time: Go's timers count time the machine is running, so
// after a sleep or suspend (or a paused VM) one long wait would fire hours
// late. The time to fire at is the wall clock's (next's), so a sleep
// through it fires as soon as the machine wakes.
func Every(ctx context.Context, next func(now time.Time) time.Time, fire func()) {
	at := next(time.Now())
	for {
		if !time.Now().Before(at) {
			fire()
			at = next(time.Now())
			continue
		}
		t := time.NewTimer(min(time.Until(at), checkEvery))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// checkEvery is how often Every looks at the clock.
var checkEvery = time.Minute
