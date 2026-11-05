package systemd

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// Values reach the unit through systemd-run's environment, never its
// command line (anyone on the server can read that with ps).
func TestUserCommandKeepsValuesOffTheCommandLine(t *testing.T) {
	cmd := UserCommand(context.Background(), "roostack-backup-1", []string{"RESTIC_REST_PASSWORD=s3cret", "GOGC=20"}, "restic", "snapshots")
	line := strings.Join(cmd.Args, " ")
	if strings.Contains(line, "s3cret") || !strings.Contains(line, "--setenv=GOGC --setenv=RESTIC_REST_PASSWORD -- restic snapshots") {
		t.Errorf("command line: %s", line)
	}
	if !slices.Contains(cmd.Env, "RESTIC_REST_PASSWORD=s3cret") {
		t.Error("the value should be in systemd-run's environment")
	}
}

// A transient unit is named after what it runs (the quality review of
// 2026-10-21, D8).
func TestUnitWhat(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"restic", []string{"backup"}, "restic"},
		{"/usr/bin/restic", nil, "restic"},
		{"podman", []string{"pull", "docker.io/library/nginx:1"}, "podman-pull"},
		{"podman", []string{"secret", "ls"}, "podman-secret"},
		{"podman", []string{"--log-level=error", "ps"}, "podman"},
		{"podman", []string{"unshare", "restic", "snapshots"}, "restic"},
		{"podman", []string{"unshare", "/usr/bin/roostack", "backup-helper"}, "roostack"},
		{"podman", []string{"unshare", "nice", "-n", "19", "ionice", "-c3", "du", "-s"}, "du"},
		{"nice", []string{"-n", "19", "ionice", "-c3", "du", "-s", "/srv"}, "du"},
		{"podman", nil, "podman"},
		{"odd name:x", nil, "odd-name-x"},
	} {
		if got := unitWhat(c.name, c.args); got != c.want {
			t.Errorf("unitWhat(%q, %q) = %q, want %q", c.name, c.args, got, c.want)
		}
	}
}

// A command reaches the unit as it is: systemd-run doesn't expand ${x}
// in it (a package's hook scripts use the shell's own; the package try
// of Gokapi, 2026-10-31).
func TestUserCommandDoesNotExpand(t *testing.T) {
	cmd := UserCommand(context.Background(), "roostack-podman-exec-1", nil, "podman", "exec", "app", "sh", "-c", `echo "${#x}"`)
	dash := slices.Index(cmd.Args, "--")
	if i := slices.Index(cmd.Args, "--expand-environment=no"); i < 0 || i > dash {
		t.Errorf("command line: %s", strings.Join(cmd.Args, " "))
	}
	if cmd.Args[len(cmd.Args)-1] != `echo "${#x}"` {
		t.Errorf("the script changed: %q", cmd.Args[len(cmd.Args)-1])
	}
}
