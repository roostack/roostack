package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// updatesTimeout bounds how long the agent follows one run of OS updates.
const updatesTimeout = time.Hour

// updatesUnit runs the updates. UpdatesLog is where its output goes: in
// the agent's runtime directory, which it can clear before a run, and
// which outlives a restart of the agent (RuntimeDirectoryPreserve), so
// the end of a run that upgraded Roostack can still be read there.
const (
	updatesUnit = "roostack-os-updates"
	UpdatesLog  = "/run/roostack-agent/os-updates.log"
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
// internal/activity). With ROOSTACK_SERIES set, it first moves Roostack's
// source to that series (SPEC.md §7, "A new release series"): only its
// Suites line changes, so a mirror in URIs stays. If the new series can't
// be fetched (apt checks its signature), the source goes back as it was.
// Then roostack is installed from it with apt-get, keeping the source as
// it is on disk (--force-confold): the new package's own source names the
// same series, and differs only if someone changed it, such as for a
// mirror. unattended-upgrade would leave a package out instead of asking.
const updatesScript = `src=` + RoostackSource + `
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
	if err := os.Remove(UpdatesLog); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
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
