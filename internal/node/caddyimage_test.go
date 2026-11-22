package node

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/systemd"
)

// imageUnits reports the image unit's states in turn (the last one repeats),
// and runs step before each report, e.g. to write more of the log.
type imageUnits struct {
	mu     sync.Mutex
	states []string
	step   func(i int)
	i      int
}

func (u *imageUnits) Status(_ context.Context, units ...string) ([]systemd.UnitStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.step != nil {
		u.step(u.i)
	}
	st := u.states[min(u.i, len(u.states)-1)]
	u.i++
	state, result, _ := strings.Cut(st, "/")
	return []systemd.UnitStatus{{Unit: units[0], ActiveState: state, Result: result}}, nil
}

// The download's output is copied whole lines at a time, including what
// it wrote just before finishing; a failed download is an error that says
// why, with Podman's reason.
func TestCopyImageLog(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "caddy-image.log")
	appendLog := func(s string) {
		f, _ := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		f.WriteString(s)
		f.Close()
	}
	lastLine := map[string]string{
		"active":   "Writing manifest to image destination\n",
		"inactive": "Writing manifest to image destination\n",
		"failed":   "Error: initializing source docker://ghcr.io/roostack/roostack-caddy: manifest unknown\n",
	}
	for _, c := range []struct {
		final   string
		wantErr bool
	}{{"active", false}, {"inactive", false}, {"failed", true}} {
		os.Remove(logFile)
		units := &imageUnits{states: []string{"inactive", "activating", "activating", "activating", c.final},
			step: func(i int) {
				switch i {
				case 1:
					appendLog("Trying to pull ghcr.io/roostack/roostack-caddy@sha256:…\nCopying blob 1f")
				case 2:
					appendLog("3e done\n")
				case 4:
					appendLog(lastLine[c.final])
				}
			}}
		n := &Local{Units: units, imagePoll: time.Millisecond}
		var out strings.Builder
		err := n.copyImageLog(context.Background(), logFile, &out)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err %v", c.final, err)
		}
		if c.wantErr && !strings.Contains(fmt.Sprint(err), "Podman said: initializing source docker://ghcr.io/roostack/roostack-caddy: manifest unknown. Roostack will try again") {
			t.Errorf("%s: err %v", c.final, err)
		}
		if want := "Trying to pull ghcr.io/roostack/roostack-caddy@sha256:…\nCopying blob 1f3e done\n" + lastLine[c.final]; out.String() != want {
			t.Errorf("%s: copied %q", c.final, out.String())
		}
	}
}

// An image already here skips the download (ExecCondition=), which isn't
// waited for as if it hadn't started.
func TestCopyImageLogSkipped(t *testing.T) {
	n := &Local{Units: &imageUnits{states: []string{"inactive/exec-condition"}}, imagePoll: time.Hour}
	if err := n.copyImageLog(context.Background(), filepath.Join(t.TempDir(), "none.log"), io.Discard); err != nil {
		t.Error(err)
	}
}

// Caddy says it's downloading its image or waiting for the domain instead of looking
// stopped.
func TestCaddyState(t *testing.T) {
	unit := func(load, active, sub string) systemd.UnitStatus {
		return systemd.UnitStatus{LoadState: load, ActiveState: active, SubState: sub}
	}
	for _, c := range []struct {
		caddy, image systemd.UnitStatus
		want         string
	}{
		{unit("not-found", "inactive", "dead"), unit("loaded", "activating", "start"), "downloading its image"},
		{unit("loaded", "inactive", "dead"), unit("loaded", "activating", "start"), "downloading its image"},
		{unit("not-found", "inactive", "dead"), unit("loaded", "active", "exited"), "waiting for your domain"},
		{unit("loaded", "inactive", "dead"), unit("loaded", "failed", "failed"), "its image didn't download"},
		{unit("loaded", "active", "running"), unit("loaded", "active", "exited"), "active (running)"},
	} {
		if got := caddyState(c.caddy, c.image); got != c.want {
			t.Errorf("%+v %+v: %q, want %q", c.caddy, c.image, got, c.want)
		}
	}
}

// An apply that starts a download empties the log an earlier one left,
// so the follower doesn't take it for the new one's (and stop at once, as
// the unit isn't running yet); a download already running keeps its log.
func TestClearImageLog(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "caddy-image.log")
	for _, c := range []struct {
		state string
		start bool
		want  string
	}{{"inactive", true, ""}, {"failed", true, ""}, {"activating", true, "old\n"}, {"inactive", false, "old\n"}} {
		os.WriteFile(logFile, []byte("old\n"), 0o644)
		plan := &reconcile.Plan{}
		if c.start {
			plan.ImageUnits = []string{"roostack-x-image.service", CaddyImageUnit}
		}
		n := &Local{Units: &imageUnits{states: []string{c.state}}}
		n.clearImageLog(context.Background(), plan, logFile)
		if b, _ := os.ReadFile(logFile); string(b) != c.want {
			t.Errorf("%s, start %v: log %q, want %q", c.state, c.start, b, c.want)
		}
	}
	// No log yet: nothing to empty, and no error.
	n := &Local{Units: &imageUnits{states: []string{"inactive"}}}
	n.clearImageLog(context.Background(), &reconcile.Plan{ImageUnits: []string{CaddyImageUnit}}, filepath.Join(t.TempDir(), "none.log"))
}
