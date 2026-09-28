package systemd

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"time"
)

// UserCommand builds a command that runs as a transient unit of the roostack
// user's systemd manager (systemd-run --user), with stdin/stdout/stderr
// connected to the returned Cmd.
//
// This is how roostack runs podman and anything touching container-owned
// files: roostack itself is sandboxed with NoNewPrivileges, which breaks
// rootless podman's setuid helpers (SPEC.md §6.1, ASSUMPTIONS A8).
//
// unit names the transient service (e.g. "roostack-backup"); env is passed
// with --setenv. Cancelling ctx sends SIGINT to the unit, so programs like
// restic can stop cleanly.
func UserCommand(ctx context.Context, unit string, env []string, name string, args ...string) *exec.Cmd {
	full := []string{"--user", "--wait", "--pipe", "--quiet", "--collect", "--unit=" + unit}
	sorted := append([]string(nil), env...)
	sort.Strings(sorted) // stable command lines for tests and logs
	for _, e := range sorted {
		full = append(full, "--setenv="+e)
	}
	full = append(full, "--", name)
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "systemd-run", full...)
	cmd.Cancel = func() error {
		// Stop the unit's process, not just systemd-run.
		stop := exec.Command("systemctl", "--user", "kill", "--signal=SIGINT", unit+".service")
		stop.Run()
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 30 * time.Second
	return cmd
}
