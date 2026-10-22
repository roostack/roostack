// Package osupdate keeps the host's Debian packages up to date (SPEC.md
// §3.2): unattended-upgrades installs updates through roostack-agent as a
// maintenance job after the nightly backup, and a reboot is requested from
// the coordinator when an update needs one.
//
// Debian's own apt timers stay enabled as a fallback, so security updates
// still arrive if Roostack isn't running.
package osupdate

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/store"
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

const (
	lastKey    = "os.updates.last"
	runningKey = "os.updates.running" // a runMark while a run lasts
)

// Agent is what's needed from roostack-agent.
type Agent interface {
	// RunUpdates installs updates; a series first moves Roostack to that
	// release series (series.go).
	RunUpdates(ctx context.Context, series string, w io.Writer) error
	// UpdatesStatus reads the latest run, which may have outlived the
	// agent and this process.
	UpdatesStatus(ctx context.Context) (agent.UpdatesRun, error)
	Reboot(ctx context.Context) error
	HostChanges(ctx context.Context) (agent.HostChanges, error)
}

// HostCheck is what changed on the host without Roostack (SPEC.md §3.2), and
// whether the Owner said they made those changes on purpose.
type HostCheck struct {
	agent.HostChanges
	Known bool `json:"known"`
}

// hostKnownKey stores the fingerprint of changes the Owner knows about.
const hostKnownKey = "host.changes.known"

// HostChanges checks the host for changes Roostack didn't make.
func (s *Service) HostChanges(ctx context.Context) (HostCheck, error) {
	c, err := s.Agent.HostChanges(ctx)
	if err != nil {
		return HostCheck{}, err
	}
	known, _ := s.Store.Setting(ctx, hostKnownKey)
	return HostCheck{HostChanges: c, Known: c.Any() && known == c.Fingerprint()}, nil
}

// AcknowledgeHostChanges records that the Owner made the current changes
// on purpose: they're not flagged again until something else changes.
func (s *Service) AcknowledgeHostChanges(ctx context.Context) error {
	c, err := s.Agent.HostChanges(ctx)
	if err != nil {
		return err
	}
	return s.Store.SetSetting(ctx, hostKnownKey, c.Fingerprint())
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
	// HTTP fetches the list of Roostack's release series (series.go);
	// nil uses the default client.
	HTTP *http.Client
	// BackupCheck says why Roostack can't move to a new release series
	// now ("" when it can): it waits for a backup that worked (SPEC.md
	// §7). Nil means no check.
	BackupCheck func(ctx context.Context) string

	// PollEvery is how often a run is checked while it carries on without
	// the agent following it (0: every 5 seconds).
	PollEvery time.Duration

	mu      sync.Mutex
	pending bool          // queued or running
	act     *activity.Log // the queued run's log
}

// runMark is kept while a run lasts, so a Roostack that restarted
// halfway through (upgrading Roostack restarts it) can still record how
// it ended (Resume).
type runMark struct {
	Started time.Time `json:"started"`
}

// runLimit is how long after its start a run is followed: the agent gives
// up on one after an hour (agent.updatesTimeout).
const runLimit = 2 * time.Hour

// ErrAlreadyQueued means updates are already queued or running.
var ErrAlreadyQueued = errors.New("OS updates are already queued")

// Submit queues an update run.
func (s *Service) Submit(_ context.Context) error {
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
func (s *Service) Pending(_ context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending, nil
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
			series := s.seriesToInstall(ctx)
			if series != "" {
				act.Printf("Moving Roostack to its %s releases, as you accepted.", series)
			}
			mark := runMark{Started: s.Now()}
			s.setMark(ctx, &mark)
			var buf bytes.Buffer
			err := s.Agent.RunUpdates(ctx, series, io.MultiWriter(act, &buf))
			var res Result
			if errors.Is(err, agent.ErrUpdatesCut) {
				// Upgrading Roostack restarts the agent, which stops sending
				// the run's output, but the run carries on (SPEC.md §7). It's
				// followed to its end here, or, if this process is restarted
				// too, by the next one (Resume).
				act.Printf("Roostack's agent restarted (upgrading Roostack restarts it), and the run carries on: " +
					"following it until it ends.")
				if res, err = s.follow(ctx, mark); err != nil {
					act.Finish(err) // Roostack is stopping: the next start records it
					return err
				}
			} else {
				out := headTail(buf.String(), 3<<10, 5<<10)
				res = Result{At: s.Now(), OK: err == nil, Summary: Summarize(out), Output: out}
				if err != nil {
					res.Summary = "Updating didn't finish: " + err.Error()
				}
			}
			return s.record(ctx, act, res, err)
		},
	}
}

// record saves a run's result and says it in its log, forgets the run's
// mark, and asks for a restart if updates need one.
func (s *Service) record(ctx context.Context, act *activity.Log, res Result, err error) error {
	if err == nil {
		act.Step("%s", res.Summary)
	}
	ctx = context.WithoutCancel(ctx)
	if serr := store.PutJSON(ctx, s.Store, lastKey, res); serr != nil {
		s.Log.Error("recording OS update result", "err", serr)
	}
	s.setMark(ctx, nil)
	if s.CheckReboot() {
		act.Printf("A restart is needed to finish installing updates.")
	}
	act.Finish(err)
	return err
}

func (s *Service) setMark(ctx context.Context, m *runMark) {
	v := ""
	if m != nil {
		data, _ := json.Marshal(m)
		v = string(data)
	}
	if err := s.Store.SetSetting(context.WithoutCancel(ctx), runningKey, v); err != nil {
		s.Log.Error("recording that OS updates are running", "err", err)
	}
}

// follow waits for a run the agent no longer follows to end, and reads
// how it went (agent.UpdatesRun).
func (s *Service) follow(ctx context.Context, mark runMark) (Result, error) {
	every := cmp.Or(s.PollEvery, 5*time.Second)
	for {
		run, err := s.Agent.UpdatesStatus(ctx)
		switch {
		case err == nil && !run.Running:
			return runResult(run, mark, s.Now()), nil
		case s.Now().Sub(mark.Started) > runLimit:
			why := "it's still running"
			if err != nil {
				why = "Roostack can't ask the agent: " + err.Error()
			}
			return Result{At: s.Now(), Summary: "Roostack stopped waiting for the OS updates started " +
				mark.Started.Local().Format("2 January 15:04") + ": " + why + ". Its log is " + agent.UpdatesLog + "."}, nil
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(every):
		}
	}
}

// runResult is a run's result as the agent last saw it. A run that never
// said how it ended, or whose ending is older than the mark (an earlier
// run's), was cut off: the server restarted, which stops it.
func runResult(run agent.UpdatesRun, mark runMark, now time.Time) Result {
	out := headTail(run.Output, 3<<10, 5<<10)
	res := Result{At: now, Output: out}
	switch {
	case run.Ended.IsZero() || run.Ended.Before(mark.Started.Add(-time.Second)):
		res.Summary = "The OS updates started " + mark.Started.Local().Format("2 January 15:04") +
			" didn't finish: they were stopped, probably by the server restarting. The next run carries on."
		res.Output = ""
	case run.Status != 0:
		res.Summary = "Updating didn't finish: exit status " + strconv.Itoa(run.Status)
	default:
		res.OK, res.Summary = true, Summarize(out)
		if v := roostackUpgrade(out); v != "" {
			res.Summary = "Roostack updated itself to " + v + " and restarted. " + res.Summary
		}
	}
	return res
}

// Resume records a run of OS updates that was still going when Roostack
// stopped: upgrading Roostack restarts it halfway through one (SPEC.md
// §7). As a maintenance job, so nothing else starts meanwhile, it follows
// the run to its end and records it. Called once at start.
func (s *Service) Resume(ctx context.Context) {
	v, err := s.Store.Setting(ctx, runningKey)
	if err != nil || v == "" {
		return
	}
	var mark runMark
	if err := json.Unmarshal([]byte(v), &mark); err != nil {
		s.setMark(ctx, nil)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending {
		return
	}
	s.pending = true
	s.Coord.Submit(maintenance.Job{
		Name: JobName,
		Run: func(ctx context.Context, h *maintenance.Handle) error {
			defer func() {
				s.mu.Lock()
				s.pending = false
				s.mu.Unlock()
			}()
			act := s.Activity.Start(activity.OSUpdates, "OS updates")
			act.Step("Finishing the run started %s", mark.Started.Local().Format("2 January 15:04"))
			act.Printf("Roostack restarted while OS updates were running (upgrading Roostack restarts it): " +
				"following the run until it ends, to record how it went.")
			res, err := s.follow(ctx, mark)
			if err != nil {
				act.Finish(err) // Roostack is stopping again
				return err
			}
			if res.Output != "" {
				act.Printf("The run's output:")
				io.WriteString(act, res.Output)
			}
			return s.record(ctx, act, res, nil)
		},
	})
}

// Last returns the last update run, or nil if there hasn't been one.
func (s *Service) Last(ctx context.Context) (*Result, error) {
	r, err := store.GetJSON[*Result](ctx, s.Store, lastKey)
	if err != nil {
		return nil, err
	}
	return r, nil
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
	// LastBoot is when the server last started.
	LastBoot time.Time
}

// RestartStatus reports whether a restart is needed and why it's waiting.
func (s *Service) RestartStatus(_ context.Context) (RestartStatus, error) {
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
	return rs, nil
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

// LastBoot is when the server last started (btime in /proc/stat), or
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

// roostackUpgrade is the version of Roostack a run's output says dpkg
// unpacked, or "".
func roostackUpgrade(out string) string {
	_, rest, ok := strings.Cut(out, "Unpacking roostack (")
	if !ok {
		return ""
	}
	v, _, ok := strings.Cut(rest, ")")
	if !ok || strings.ContainsAny(v, " \n") {
		return ""
	}
	return v
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
