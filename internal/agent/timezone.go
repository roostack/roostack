package agent

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// tzRe matches IANA time zone names such as UTC, Europe/Berlin or
// America/Argentina/Buenos_Aires.
var tzRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]*(/[A-Za-z0-9_+-]+){0,2}$`)

// ValidTimezone reports whether name looks like an IANA time zone name.
// timedatectl then checks the zone exists; this keeps anything else (paths,
// options) from reaching it.
func ValidTimezone(name string) bool {
	return len(name) <= 64 && tzRe.MatchString(name) && !strings.Contains(name, "..")
}

// SetTimezone runs timedatectl, which also checks the zone exists.
func (h LinuxHost) SetTimezone(name string) error {
	if out, err := exec.Command("timedatectl", "set-timezone", name).CombinedOutput(); err != nil {
		return fmt.Errorf("timedatectl: %w: %s", err, out)
	}
	return nil
}
