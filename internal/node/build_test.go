package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/systemd"
)

// units reports the build unit's states in turn (the last one repeats),
// and runs step before each report, e.g. to write more of the log.
type buildUnits struct {
	mu     sync.Mutex
	states []string
	step   func(i int)
	i      int
}

func (u *buildUnits) Status(_ context.Context, units ...string) ([]systemd.UnitStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.step != nil {
		u.step(u.i)
	}
	st := u.states[min(u.i, len(u.states)-1)]
	u.i++
	return []systemd.UnitStatus{{Unit: units[0], ActiveState: st}}, nil
}

// The build's output is copied whole lines at a time, including what it
// wrote just before finishing; a failed build is an error.
func TestCopyBuildLog(t *testing.T) {
	buildPoll = time.Millisecond
	t.Cleanup(func() { buildPoll = 2 * time.Second })
	logFile := filepath.Join(t.TempDir(), "caddy-build.log")
	appendLog := func(s string) {
		f, _ := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		f.WriteString(s)
		f.Close()
	}
	for _, c := range []struct {
		final   string
		wantErr bool
	}{{"inactive", false}, {"failed", true}} {
		os.Remove(logFile)
		units := &buildUnits{states: []string{"inactive", "activating", "activating", "activating", c.final},
			step: func(i int) {
				switch i {
				case 1:
					appendLog("STEP 1/2: FROM caddy-builder\nxcaddy bu")
				case 2:
					appendLog("ild --with cloudflare\n")
				case 4:
					appendLog("COMMIT localhost/roostack-caddy\n")
				}
			}}
		n := &Local{Units: units}
		var out strings.Builder
		err := n.copyBuildLog(context.Background(), logFile, &out)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err %v", c.final, err)
		}
		if want := "STEP 1/2: FROM caddy-builder\nxcaddy build --with cloudflare\nCOMMIT localhost/roostack-caddy\n"; out.String() != want {
			t.Errorf("%s: copied %q", c.final, out.String())
		}
	}
}

// Caddy says it's building or waiting for the domain instead of looking
// stopped.
func TestCaddyState(t *testing.T) {
	unit := func(load, active, sub string) systemd.UnitStatus {
		return systemd.UnitStatus{LoadState: load, ActiveState: active, SubState: sub}
	}
	for _, c := range []struct {
		caddy, build systemd.UnitStatus
		want         string
	}{
		{unit("not-found", "inactive", "dead"), unit("loaded", "activating", "start"), "building its image (the first time takes several minutes)"},
		{unit("loaded", "inactive", "dead"), unit("loaded", "activating", "start"), "building its image (the first time takes several minutes)"},
		{unit("not-found", "inactive", "dead"), unit("loaded", "active", "exited"), "waiting for your domain"},
		{unit("loaded", "inactive", "dead"), unit("loaded", "failed", "failed"), "its image didn't build"},
		{unit("loaded", "active", "running"), unit("loaded", "active", "exited"), "active (running)"},
	} {
		if got := caddyState(c.caddy, c.build); got != c.want {
			t.Errorf("%+v %+v: %q, want %q", c.caddy, c.build, got, c.want)
		}
	}
}

// An apply that starts a build empties the log an earlier build left, so
// the follower doesn't take it for the new build's (and stop at once, as
// the unit isn't running yet); a build already running keeps its log.
func TestClearBuildLog(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "caddy-build.log")
	for _, c := range []struct {
		state string
		build bool
		want  string
	}{{"inactive", true, ""}, {"failed", true, ""}, {"activating", true, "old\n"}, {"inactive", false, "old\n"}} {
		os.WriteFile(logFile, []byte("old\n"), 0o644)
		plan := &reconcile.Plan{}
		if c.build {
			plan.BuildUnits = []string{"roostack-x-build.service", CaddyBuildUnit}
		}
		n := &Local{Units: &buildUnits{states: []string{c.state}}}
		n.clearBuildLog(context.Background(), plan, logFile)
		if b, _ := os.ReadFile(logFile); string(b) != c.want {
			t.Errorf("%s, build %v: log %q, want %q", c.state, c.build, b, c.want)
		}
	}
	// No log yet: nothing to empty, and no error.
	n := &Local{Units: &buildUnits{states: []string{"inactive"}}}
	n.clearBuildLog(context.Background(), &reconcile.Plan{BuildUnits: []string{CaddyBuildUnit}}, filepath.Join(t.TempDir(), "none.log"))
}
