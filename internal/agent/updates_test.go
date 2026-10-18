package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The updates' log is followed from before the unit makes it to after
// the unit ends (RunUpdates).
func TestFollowFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-updates.log")
	done := make(chan error, 1)
	var out strings.Builder
	result := make(chan error, 1)
	go func() { result <- followFile(path, &out, done, 5*time.Millisecond) }()
	time.Sleep(20 * time.Millisecond) // no file yet
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("==> Downloading the list of available updates\n")
	time.Sleep(20 * time.Millisecond)
	f.WriteString("==> Installing updates\nAll upgrades installed\n")
	f.Close()
	done <- errors.New("exit status 1")
	if err := <-result; err == nil || err.Error() != "exit status 1" {
		t.Errorf("err = %v", err)
	}
	if want := "==> Downloading the list of available updates\n==> Installing updates\nAll upgrades installed\n"; out.String() != want {
		t.Errorf("out = %q", out.String())
	}
}

func TestUpdatesScriptSeries(t *testing.T) {
	// The series reaches the script only as an environment variable, and
	// only after ValidSeries; the script changes only the Suites line.
	for _, want := range []string{`"${ROOSTACK_SERIES:-}"`, `s/^Suites:.*/Suites: $ROOSTACK_SERIES/`, "--force-confold", "unattended-upgrade -v"} {
		if !strings.Contains(updatesScript, want) {
			t.Errorf("updatesScript lacks %q", want)
		}
	}
}
