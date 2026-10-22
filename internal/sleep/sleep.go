// Package sleep has the two waits loops share: For, which gives up when
// ctx ends, and Every, which calls a function every so often until ctx
// ends. Waits by the wall clock, such as a nightly job, which must fire
// on time after the machine sleeps, use maintenance.Every.
package sleep

import (
	"context"
	"time"
)

// For waits d, or until ctx ends: it reports whether the wait ran its
// full time (false: ctx ended, so stop).
func For(ctx context.Context, d time.Duration) bool {
	if ctx.Err() != nil {
		return false
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Every calls fn every d until ctx ends, the first time after d. The wait
// starts again once fn returns, so a slow fn never runs twice at once nor
// back to back.
func Every(ctx context.Context, d time.Duration, fn func()) {
	for For(ctx, d) {
		fn()
	}
}
