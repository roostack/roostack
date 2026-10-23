package systemd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// unitSeq numbers transient units, so two never share a name.
var unitSeq atomic.Int64

// TransientUnit names a transient unit after the command it runs, so
// `systemctl --user list-units` shows a "podman-pull" during an install
// rather than something else: roostack-<what>-<pid>-<n>. What is the
// command, with podman's subcommand ("podman-pull"), or what podman
// unshare runs ("restic"), looking past nice and ionice ("du").
func TransientUnit(name string, args ...string) string {
	return fmt.Sprintf("roostack-%s-%d-%d", unitWhat(name, args), os.Getpid(), unitSeq.Add(1))
}

func unitWhat(name string, args []string) string {
	words := append([]string{name}, args...)
	if filepath.Base(words[0]) == "podman" && len(words) > 2 && words[1] == "unshare" {
		words = words[2:]
	}
	// nice and ionice, with their options, run the command after them.
	for len(words) > 1 && (filepath.Base(words[0]) == "nice" || filepath.Base(words[0]) == "ionice") {
		words = words[1:]
		for len(words) > 1 && (strings.HasPrefix(words[0], "-") || strings.Trim(words[0], "0123456789") == "") {
			words = words[1:]
		}
	}
	what := filepath.Base(words[0])
	if what == "podman" && len(words) > 1 && !strings.HasPrefix(words[1], "-") {
		what += "-" + words[1]
	}
	// Anything else becomes a dash: a unit's name can't hold it, or it
	// would read badly.
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, what)
}

// UserCommand builds a command that runs as a transient unit of the roostack
// user's systemd manager (systemd-run --user), with stdin/stdout/stderr
// connected to the returned Cmd.
//
// This is how roostack runs podman and anything touching container-owned
// files: roostack itself is sandboxed with NoNewPrivileges, which breaks
// rootless podman's setuid helpers (SPEC.md §6.1, ASSUMPTIONS A8).
//
// unit names the transient service (e.g. "roostack-restic-1234-1"). env
// ("NAME=value") reaches the unit by name only (--setenv=NAME takes the
// value from systemd-run's own environment), so values such as backup
// store passwords never appear in a command line anyone on the server can
// see with ps. Cancelling ctx sends SIGINT to the unit, so programs like
// restic can stop cleanly.
func UserCommand(ctx context.Context, unit string, env []string, name string, args ...string) *exec.Cmd {
	full := []string{"--user", "--wait", "--pipe", "--quiet", "--collect", "--unit=" + unit}
	sorted := append([]string(nil), env...)
	sort.Strings(sorted) // stable command lines for tests and logs
	for _, e := range sorted {
		name, _, _ := strings.Cut(e, "=")
		full = append(full, "--setenv="+name)
	}
	full = append(full, "--", name)
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "systemd-run", full...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Cancel = func() error {
		// Stop the unit's process, not just systemd-run.
		stop := exec.Command("systemctl", "--user", "kill", "--signal=SIGINT", unit+".service")
		stop.Run()
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 30 * time.Second
	return cmd
}
