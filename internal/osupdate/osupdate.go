// Package osupdate keeps the host's Debian packages up to date (SPEC.md
// §3.2): unattended-upgrades installs updates through mylab-agent as a
// maintenance job after the nightly backup, and a reboot is requested from
// the coordinator when an update needs one.
//
// Debian's own apt timers stay enabled as a fallback, so security updates
// still arrive if MyLab isn't running.
package osupdate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/maintenance"
)

// JobName is the coordinator job name, shown in the UI ("waiting for OS
// updates to finish").
const JobName = "OS updates"

// Files written by Debian packages (e.g. a new kernel) when a reboot is
// needed. Overridable in tests.
var (
	RebootRequiredFile = "/run/reboot-required"
	RebootPkgsFile     = "/run/reboot-required.pkgs"
	ProcStat           = "/proc/stat"
)

const lastKey = "os.updates.last"

// Agent is what's needed from mylab-agent.
type Agent interface {
	RunUpdates(ctx context.Context, w io.Writer) error
	Reboot(ctx context.Context) error
}

// KV stores the last result (the store's settings table).
type KV interface {
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Coordinator is the maintenance coordinator's API used here.
type Coordinator interface {
	Submit(chain ...maintenance.Job)
	RequestReboot()
	Status() maintenance.Status
}

// Result is the outcome of the last update run.
type Result struct {
	At      time.Time `json:"at"`
	OK      bool      `json:"ok"`
	Summary string    `json:"summary"` // for people
	Output  string    `json:"output,omitempty"`
}

// Service runs OS updates and watches for needed reboots.
type Service struct {
	Agent Agent
	Store KV
	Coord Coordinator
	Now   func() time.Time
	Log   *slog.Logger
	// Activity shows each run live in the UI (optional).
	Activity *activity.Registry

	mu      sync.Mutex
	pending bool          // queued or running
	act     *activity.Log // the queued run's log
}

// ErrAlreadyQueued means updates are already queued or running.
var ErrAlreadyQueued = errors.New("OS updates are already queued")

// Submit queues an update run.
func (s *Service) Submit() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending {
		return ErrAlreadyQueued
	}
	s.pending = true
	// Start the log now, so a run queued behind a backup shows as waiting.
	s.act = s.Activity.Start(activity.OSUpdates, "OS updates")
	s.act.Step("Waiting for other maintenance to finish")
	s.Coord.Submit(s.Job())
	return nil
}

// Pending reports whether an update run is queued or running.
func (s *Service) Pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// Job installs updates, records the result, and requests a reboot if one
// is needed. It isn't resumable: apt must not be interrupted, and the
// coordinator never interrupts a job that doesn't say it can be.
func (s *Service) Job() maintenance.Job {
	return maintenance.Job{
		Name: JobName,
		Run: func(ctx context.Context, h *maintenance.Handle) error {
			defer func() {
				s.mu.Lock()
				s.pending = false
				s.mu.Unlock()
			}()
			s.mu.Lock()
			act := s.act
			s.act = nil
			s.mu.Unlock()
			if act == nil { // run without Submit
				act = s.Activity.Start(activity.OSUpdates, "OS updates")
			}
			act.Step("Starting")
			var buf bytes.Buffer
			err := s.Agent.RunUpdates(ctx, io.MultiWriter(act, &buf))
			out := headTail(buf.String(), 3<<10, 5<<10)
			res := Result{At: s.Now(), OK: err == nil, Summary: Summarize(out), Output: out}
			if err != nil {
				res.Summary = "Updating didn't finish: " + err.Error()
			} else {
				act.Step("%s", res.Summary)
			}
			if data, jerr := json.Marshal(res); jerr == nil {
				if serr := s.Store.SetSetting(context.WithoutCancel(ctx), lastKey, string(data)); serr != nil {
					s.Log.Error("recording OS update result", "err", serr)
				}
			}
			if s.CheckReboot() {
				act.Printf("A restart is needed to finish installing updates.")
			}
			act.Finish(err)
			return err
		},
	}
}

// Last returns the last update run, or nil if there hasn't been one.
func (s *Service) Last(ctx context.Context) (*Result, error) {
	v, err := s.Store.Setting(ctx, lastKey)
	if err != nil || v == "" {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal([]byte(v), &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// CheckReboot asks the coordinator for a reboot if updates need one.
func (s *Service) CheckReboot() bool {
	if _, err := os.Stat(RebootRequiredFile); err != nil {
		return false
	}
	s.Coord.RequestReboot()
	return true
}

// RestartStatus is what the UI shows about restarting.
type RestartStatus struct {
	Needed   bool
	Since    time.Time // when an update asked for it
	Packages []string  // which updates asked for it
	Waiting  string    // the coordinator's reason, when needed
	Running  string    // a job that's running now, if any
	// LastBoot is when the machine last started.
	LastBoot time.Time
}

// RestartStatus reports whether a restart is needed and why it's waiting.
func (s *Service) RestartStatus() RestartStatus {
	need, pkgs := RebootNeeded()
	st := s.Coord.Status()
	rs := RestartStatus{Needed: need, Packages: pkgs, LastBoot: LastBoot()}
	if need {
		rs.Waiting = st.Reboot.Reason
		if fi, err := os.Stat(RebootRequiredFile); err == nil {
			rs.Since = fi.ModTime()
		}
	}
	if st.Running != nil {
		rs.Running = st.Running.Name
	}
	return rs
}

// ErrBusy means a restart now would interrupt a running job.
type ErrBusy struct{ Job string }

func (e ErrBusy) Error() string { return "wait until " + e.Job + " has finished" }

// RestartNow restarts the host, unless a maintenance job is running.
func (s *Service) RestartNow(ctx context.Context) error {
	if st := s.Coord.Status(); st.Running != nil {
		return ErrBusy{st.Running.Name}
	}
	s.Log.Info("restart requested from the UI")
	return s.Agent.Reboot(ctx)
}

// LastBoot is when the machine last started (btime in /proc/stat), or
// zero if unknown.
func LastBoot() time.Time {
	data, err := os.ReadFile(ProcStat)
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "btime "); ok {
			if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
				return time.Unix(n, 0)
			}
		}
	}
	return time.Time{}
}

// RebootNeeded reports whether a reboot is needed, and which packages
// asked for it.
func RebootNeeded() (bool, []string) {
	if _, err := os.Stat(RebootRequiredFile); err != nil {
		return false, nil
	}
	var pkgs []string
	if f, err := os.Open(RebootPkgsFile); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if p := strings.TrimSpace(sc.Text()); p != "" && !slices.Contains(pkgs, p) {
				pkgs = append(pkgs, p)
			}
		}
	}
	return true, pkgs
}

// headTail shortens long output, keeping its start (unattended-upgrade's
// list of what it will install) and its end (the outcome).
func headTail(s string, head, tail int) string {
	if len(s) <= head+tail {
		return s
	}
	return s[:head] + "\n…\n" + s[len(s)-tail:]
}

// Summarize turns unattended-upgrade's verbose output into a sentence.
func Summarize(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		// unattended-upgrade prefixes its lines with a log level.
		_, msg, found := strings.Cut(line, " ")
		if !found || !strings.HasPrefix(line, "INFO") {
			msg = line
		}
		switch {
		case strings.HasPrefix(msg, "No packages found that can be upgraded unattended"):
			return "Everything was already up to date."
		case strings.HasPrefix(msg, "Packages that will be upgraded:"):
			pkgs := strings.Fields(strings.TrimPrefix(msg, "Packages that will be upgraded:"))
			if len(pkgs) == 1 {
				return "Installed 1 update: " + pkgs[0] + "."
			}
			return "Installed " + strconv.Itoa(len(pkgs)) + " updates: " + strings.Join(pkgs, ", ") + "."
		}
	}
	return "Updates ran."
}
