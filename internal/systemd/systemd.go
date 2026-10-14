// Package systemd drives the roostack user's systemd manager via systemctl.
//
// It shells out to `systemctl --user` rather than using D-Bus directly: no
// dependency, and every action matches a command a person could run
// themselves (docs/ASSUMPTIONS.md A3, H).
package systemd

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Runner runs a command and returns its stdout. Tests replace it.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner runs real commands.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Manager controls user units.
type Manager struct {
	Run Runner
}

// New returns a Manager that runs systemctl for real.
func New() *Manager { return &Manager{Run: ExecRunner} }

func (m *Manager) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	return m.Run(ctx, "systemctl", append([]string{"--user"}, args...)...)
}

// DaemonReload makes systemd re-run the Quadlet generator.
func (m *Manager) DaemonReload(ctx context.Context) error {
	_, err := m.systemctl(ctx, "daemon-reload")
	return err
}

// Start starts units (no-op for units already running).
func (m *Manager) Start(ctx context.Context, units ...string) error {
	return m.each(ctx, "start", units)
}

// StartNoBlock starts units in the background (systemctl --no-block): for
// jobs that take minutes, such as building an image, which nothing should
// wait for.
func (m *Manager) StartNoBlock(ctx context.Context, units ...string) error {
	if len(units) == 0 {
		return nil
	}
	_, err := m.systemctl(ctx, append([]string{"start", "--no-block"}, units...)...)
	return err
}

// Restart restarts units.
func (m *Manager) Restart(ctx context.Context, units ...string) error {
	return m.each(ctx, "restart", units)
}

// Stop stops units.
func (m *Manager) Stop(ctx context.Context, units ...string) error {
	return m.each(ctx, "stop", units)
}

// Reload asks units to reload their configuration (ExecReload=).
func (m *Manager) Reload(ctx context.Context, units ...string) error {
	return m.each(ctx, "reload", units)
}

// ReloadOrRestart reloads running units and starts stopped ones.
func (m *Manager) ReloadOrRestart(ctx context.Context, units ...string) error {
	return m.each(ctx, "reload-or-restart", units)
}

// Starting units is done without --no-block, so errors (e.g. a failed image
// pull) are reported to the caller. That makes a first start slow.
func (m *Manager) each(ctx context.Context, verb string, units []string) error {
	if len(units) == 0 {
		return nil
	}
	_, err := m.systemctl(ctx, append([]string{verb}, units...)...)
	return err
}

// UnitStatus is the state of one unit.
type UnitStatus struct {
	Unit        string
	LoadState   string // loaded, not-found, ...
	ActiveState string // active, inactive, failed, activating, ...
	SubState    string // running, dead, exited, ...
	// NRestarts is how often systemd restarted it (Restart=) since it
	// was last started by hand: a climbing count means it keeps failing.
	NRestarts int
}

// Status returns the state of each unit, in the order given.
func (m *Manager) Status(ctx context.Context, units ...string) ([]UnitStatus, error) {
	if len(units) == 0 {
		return nil, nil
	}
	args := append([]string{"show", "--property=Id,LoadState,ActiveState,SubState,NRestarts"}, units...)
	out, err := m.systemctl(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseShow(out), nil
}

// parseShow parses `systemctl show` output: blocks of key=value lines
// separated by blank lines, one block per unit.
func parseShow(out []byte) []UnitStatus {
	var res []UnitStatus
	var cur UnitStatus
	flush := func() {
		if cur != (UnitStatus{}) {
			res = append(res, cur)
		}
		cur = UnitStatus{}
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "Id":
			cur.Unit = v
		case "LoadState":
			cur.LoadState = v
		case "ActiveState":
			cur.ActiveState = v
		case "SubState":
			cur.SubState = v
		case "NRestarts":
			cur.NRestarts, _ = strconv.Atoi(v)
		}
	}
	flush()
	return res
}

// SystemStatus returns the state of system units (e.g. roostack-agent), which
// any user may read.
func (m *Manager) SystemStatus(ctx context.Context, units ...string) ([]UnitStatus, error) {
	if len(units) == 0 {
		return nil, nil
	}
	args := append([]string{"show", "--property=Id,LoadState,ActiveState,SubState,NRestarts"}, units...)
	out, err := m.Run(ctx, "systemctl", args...)
	if err != nil {
		return nil, err
	}
	return parseShow(out), nil
}
