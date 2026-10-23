package node

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/sleep"
)

// CaddyBuildUnit builds Roostack's Caddy image (generate.System).
const CaddyBuildUnit = generate.CaddyUnit + "-build.service"

// How often the build log and its unit's state are read, unless the
// node's buildPoll says otherwise.
const buildPoll = 2 * time.Second

// caddyBuilding reports whether Caddy's image is being built.
func (n *Local) caddyBuilding(ctx context.Context) bool {
	if n.Units == nil {
		return false
	}
	st, err := n.Units.Status(ctx, CaddyBuildUnit)
	return err == nil && len(st) == 1 && st[0].ActiveState == "activating"
}

// followCaddyBuild shows the Caddy image build, started in the background
// by an apply (reconcile.Plan.BuildUnits), as an activity.Build log: its
// output, copied from the log file the build unit writes
// (config.Paths.CaddyBuildLog), until the unit stops building. The first
// build compiles Caddy and takes minutes; without this, Caddy just looks
// stopped. Only one follower runs at a time.
func (n *Local) followCaddyBuild(logFile string) {
	if n.Units == nil || n.Activity == nil {
		return
	}
	n.mu.Lock()
	if n.followingBuild {
		n.mu.Unlock()
		return
	}
	n.followingBuild = true
	n.mu.Unlock()

	act := n.Activity.Start(activity.Build, "Building Roostack's web proxy")
	act.Step("compiling Caddy; the first time takes several minutes")
	go func() {
		defer func() {
			n.mu.Lock()
			n.followingBuild = false
			n.mu.Unlock()
		}()
		act.Finish(n.copyBuildLog(context.Background(), logFile, act))
	}()
}

// clearBuildLog empties the build log before plan starts a new build, so
// what the follower reads afterwards is that build's: the unit truncates
// the log only once it runs, and until then an earlier build's log would
// look like the new one's, finished. A build already running keeps its
// log: it writes on where it was, and restarted by plan it truncates the
// log itself (copyBuildLog notices).
func (n *Local) clearBuildLog(ctx context.Context, plan *reconcile.Plan, logFile string) {
	if !slices.Contains(plan.BuildUnits, CaddyBuildUnit) || n.caddyBuilding(ctx) {
		return
	}
	if err := os.Truncate(logFile, 0); err != nil && !errors.Is(err, os.ErrNotExist) {
		n.Log.Error("emptying the Caddy build's log", "err", err)
	}
}

// copyBuildLog copies new lines of the build log to w until the build unit
// has finished, and reports whether it failed. The unit is a oneshot: it
// goes back to inactive when the build succeeds, and to failed when it
// doesn't. Inactive before it has started (a background start is queued)
// means waiting, for a while.
func (n *Local) copyBuildLog(ctx context.Context, logFile string, w io.Writer) error {
	var offset int64
	var partial []byte
	copyNew := func() {
		f, err := os.Open(logFile)
		if err != nil {
			return // not written yet
		}
		defer f.Close()
		if st, err := f.Stat(); err == nil && st.Size() < offset {
			offset, partial = 0, nil // a new build truncated it
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return
		}
		data, _ := io.ReadAll(f)
		offset += int64(len(data))
		data = append(partial, data...)
		// Only whole lines; the rest waits for the next read.
		i := bytes.LastIndexByte(data, '\n')
		if i < 0 {
			partial = data
			return
		}
		w.Write(data[:i+1])
		partial = append([]byte(nil), data[i+1:]...)
	}
	poll := cmp.Or(n.buildPoll, buildPoll)
	started := false
	for waited := time.Duration(0); ; waited += poll {
		copyNew()
		st, err := n.Units.Status(ctx, CaddyBuildUnit)
		if err == nil && len(st) == 1 {
			switch st[0].ActiveState {
			case "activating":
				started = true
			case "failed":
				copyNew()
				return errors.New("the build failed; its log above says why")
			case "active":
				copyNew()
				return nil
			default: // inactive: done, or not started yet
				// A build too quick to see running has still written its log.
				if started || offset > 0 || len(partial) > 0 {
					copyNew()
					return nil
				}
				if waited > time.Minute {
					return errors.New("the build didn't start")
				}
			}
		}
		if !sleep.For(ctx, poll) {
			return ctx.Err()
		}
	}
}
