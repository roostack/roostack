package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// updatesTimeout bounds how long the agent follows one run of OS updates.
const updatesTimeout = time.Hour

// updatesUnit runs the updates. UpdatesLog is where its output goes, and
// UpdatesExit gets its exit status when it ends: in the agent's runtime
// directory, which it can clear before a run, and which outlives a
// restart of the agent (RuntimeDirectoryPreserve), so a run that upgraded
// Roostack can still be read there (UpdatesStatus). A restart of the
// server clears them.
const (
	updatesUnit = "roostack-os-updates"
	UpdatesLog  = "/run/roostack-agent/os-updates.log"
	UpdatesExit = "/run/roostack-agent/os-updates.exit"
)

// seriesPattern is a release series, as Roostack's apt suites are named
// (major.minor: scripts/build-apt-repo.sh).
var seriesPattern = regexp.MustCompile(`^[0-9]{1,4}\.[0-9]{1,4}$`)

// ValidSeries reports whether s names a release series.
func ValidSeries(s string) bool { return seriesPattern.MatchString(s) }

// RoostackSource is Roostack's own apt source, a conffile of the roostack
// package (SPEC.md §7).
const RoostackSource = "/etc/apt/sources.list.d/roostack.sources"

// updatesScript announces its steps with "==> " lines (see
// internal/activity), and writes its exit status to UpdatesExit however it
// ends (but for being killed). With ROOSTACK_SERIES set, it first moves Roostack's
// source to that series (SPEC.md §7, "A new release series"): only its
// Suites line changes, so a mirror in URIs stays. If the new series can't
// be fetched (apt checks its signature), the source goes back as it was.
// Then roostack is installed from it with apt-get, keeping the source as
// it is on disk (--force-confold): the new package's own source names the
// same series, and differs only if someone changed it, such as for a
// mirror. unattended-upgrade would leave a package out instead of asking.
const updatesScript = `trap 'echo $? >` + UpdatesExit + `' EXIT
src=` + RoostackSource + `
if [ -n "${ROOSTACK_SERIES:-}" ]; then
    echo "==> Moving Roostack to its $ROOSTACK_SERIES releases"
    [ -f "$src" ] || { echo "$src is missing"; exit 1; }
    cp -p "$src" /run/roostack-sources.old || exit
    sed -i "s/^Suites:.*/Suites: $ROOSTACK_SERIES/" "$src" || exit
    if ! apt-get update -q; then
        echo "==> Roostack $ROOSTACK_SERIES couldn't be fetched: putting its source back"
        cp -p /run/roostack-sources.old "$src"
        apt-get update -q
        exit 1
    fi
    apt-get install -y -q -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold roostack || exit
else
    echo "==> Downloading the list of available updates"
    apt-get update -q || exit
fi
echo "==> Installing updates"
unattended-upgrade -v`

// RunUpdates runs the updates in a transient systemd unit, outside the
// agent's own sandbox (dpkg scripts may touch anything), and streams their
// output to w from the unit's log file.
//
// The unit doesn't depend on the agent: upgrading the roostack package
// restarts the agent (and the roostack service) halfway through. Its
// output used to come through a pipe to the agent (systemd-run --pipe),
// which broke then, and whatever wrote next failed (unattended-upgrade
// exited 120 after Roostack 0.4.1 installed itself on a VM, 2026-10-17).
// Now the unit runs to the end on its own; a restarted agent just stops
// following it.
func (h LinuxHost) RunUpdates(ctx context.Context, series string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, updatesTimeout)
	defer cancel()
	for _, f := range []string{UpdatesLog, UpdatesExit} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	args := []string{"--unit=" + updatesUnit, "--wait", "--quiet", "--collect",
		"--property=StandardOutput=truncate:" + UpdatesLog, "--property=StandardError=inherit",
		"--setenv=DEBIAN_FRONTEND=noninteractive"}
	if series != "" {
		args = append(args, "--setenv=ROOSTACK_SERIES="+series)
	}
	args = append(args, "--", "sh", "-c", updatesScript)
	cmd := exec.CommandContext(ctx, "systemd-run", args...)
	var runOut bytes.Buffer // systemd-run's own complaints
	cmd.Stdout, cmd.Stderr = &runOut, &runOut
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	out := &lineFilter{w: w, drop: dpkgLogMarker}
	err := followFile(UpdatesLog, out, done, 500*time.Millisecond)
	out.flush()
	if err != nil {
		if msg := strings.TrimSpace(runOut.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
	}
	return err
}

// UpdatesRun is the latest run of OS updates as the host has it.
type UpdatesRun struct {
	// Running: its unit is still going.
	Running bool `json:"running"`
	// Ended is when it ended, and Status its exit status; zero if it hasn't
	// (still running, or cut off: the server restarted, or it was killed).
	Ended  time.Time `json:"ended,omitzero"`
	Status int       `json:"status"`
	// Output is its log so far, without dpkg's log markers, shortened in
	// the middle when long (updatesOutputMax).
	Output string `json:"output,omitempty"`
}

// updatesOutputMax bounds the output UpdatesStatus returns: the start and
// the end of a long log are what say what happened.
const updatesOutputMax = 256 << 10

// UpdatesStatus reads the latest run of OS updates: whether its unit is
// still running, its exit status once it has ended, and its log. It's how
// Roostack learns how a run ended after it restarted halfway through
// (upgrading Roostack restarts it).
func (h LinuxHost) UpdatesStatus() (UpdatesRun, error) {
	var run UpdatesRun
	state, err := exec.Command("systemctl", "show", "--property=ActiveState", "--value", updatesUnit+".service").Output()
	if err != nil {
		return run, fmt.Errorf("systemctl show: %w", err)
	}
	switch strings.TrimSpace(string(state)) {
	case "active", "activating", "deactivating", "reloading":
		run.Running = true
	}
	if data, err := os.ReadFile(UpdatesExit); err == nil {
		if st, serr := os.Stat(UpdatesExit); serr == nil {
			if n, perr := strconv.Atoi(strings.TrimSpace(string(data))); perr == nil {
				run.Ended, run.Status = st.ModTime(), n
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return run, err
	}
	data, err := os.ReadFile(UpdatesLog)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return run, err
	}
	var out strings.Builder
	f := &lineFilter{w: &out, drop: dpkgLogMarker}
	f.Write(data)
	f.flush()
	run.Output = out.String()
	if n := len(run.Output); n > updatesOutputMax {
		head := updatesOutputMax / 4
		run.Output = run.Output[:head] + "\n…\n" + run.Output[n-(updatesOutputMax-head):]
	}
	return run, nil
}

// followFile copies what's written to path to w until done says the
// writer has finished, then copies the rest and returns done's error. The
// file may not exist yet when it starts.
func followFile(path string, w io.Writer, done <-chan error, every time.Duration) error {
	var f *os.File
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		var finished bool
		var err error
		select {
		case err = <-done:
			finished = true
		case <-tick.C:
		}
		if f == nil {
			f, _ = os.Open(path)
		}
		if f != nil {
			io.Copy(w, f)
		}
		if finished {
			return err
		}
	}
}

// Reboot asks systemd to reboot after a moment, so the API call returns.
func (h LinuxHost) Reboot() error {
	return exec.Command("systemd-run", "--on-active=5", "--unit=roostack-reboot", "--collect",
		"systemctl", "reboot").Run()
}

// dpkgLogMarker: unattended-upgrade -v prints "Log started: <time>" and
// "Log ended: <time>" around every dpkg run (dozens in one update), which
// tell nobody anything.
func dpkgLogMarker(line string) bool {
	return strings.HasPrefix(line, "Log started: ") || strings.HasPrefix(line, "Log ended: ")
}

// lines splits output into lines, keeping an unfinished last line for
// the next write.
type lines struct{ partial string }

// add returns the lines p finishes.
func (l *lines) add(p []byte) []string {
	all := strings.Split(l.partial+string(p), "\n")
	l.partial = all[len(all)-1]
	return all[:len(all)-1]
}

// rest returns the unfinished last line, and forgets it.
func (l *lines) rest() string {
	s := l.partial
	l.partial = ""
	return s
}

// lineFilter passes output on a line at a time, leaving out the lines drop
// says to.
type lineFilter struct {
	mu    sync.Mutex
	w     io.Writer
	drop  func(line string) bool
	lines lines
}

func (f *lineFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, line := range f.lines.add(p) {
		if f.drop(strings.TrimRight(line, "\r")) {
			continue
		}
		if _, err := io.WriteString(f.w, line+"\n"); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// flush passes on an unfinished last line.
func (f *lineFilter) flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if rest := f.lines.rest(); rest != "" && !f.drop(rest) {
		io.WriteString(f.w, rest)
	}
}

// lineStream sends written output as UpdatesMessage lines. If the caller
// has gone, output is dropped rather than failing the write, which would
// close the command's output and kill it (SIGPIPE).
type lineStream struct {
	mu    sync.Mutex
	enc   *json.Encoder
	flush func()
	lines lines
	gone  bool
}

func (s *lineStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return len(p), nil
	}
	for _, line := range s.lines.add(p) {
		if err := s.enc.Encode(UpdatesMessage{Line: &line}); err != nil {
			s.gone = true
			return len(p), nil
		}
	}
	s.flush()
	return len(p), nil
}

// close sends an unfinished last line.
func (s *lineStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rest := s.lines.rest(); rest != "" {
		s.enc.Encode(UpdatesMessage{Line: &rest})
	}
}
