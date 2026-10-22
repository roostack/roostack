//go:build !linux

package agent

import (
	"errors"
	"os"
)

// ownedFolder refuses on systems other than Linux, where the agent doesn't
// run (peercred_other.go).
func ownedFolder(*os.File, string, int, int) error {
	return errors.New("backup folders can only be prepared on Linux")
}
