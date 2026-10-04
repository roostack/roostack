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
