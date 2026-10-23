package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/generate"
)

// A put back takes only a copy a restore kept of one of the service's
// backed-up folders, and keeps what it replaces in turn.
func TestPutBack(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "forgejo", "data")
	svc := &generate.Output{Service: "forgejo", Units: []string{"roostack-forgejo-app.service"},
		Backup: generate.BackupPlan{Include: []string{data}}}
	before := data + ".before-restore-20260926-130602"
	writeFile(t, filepath.Join(data, "now.txt"), "now")
	writeFile(t, filepath.Join(before, "then.txt"), "then")
	for _, d := range []string{data + ".failed-restore-20260927-090000", data + ".before-restore-notatime",
		filepath.Join(root, "other.before-restore-20260926-130602")} {
		os.MkdirAll(d, 0o750)
	}
	units := &fakeUnits{}
	r := &Restorer{Units: units, Now: func() time.Time { return time.Date(2026, 9, 28, 10, 0, 0, 0, time.Local) }}
	for _, bad := range []string{data, data + ".failed-restore-20260927-090000", data + ".before-restore-notatime",
		filepath.Join(root, "other.before-restore-20260926-130602"), "/"} {
		if err := r.PutBack(context.Background(), svc, bad); err == nil {
			t.Errorf("PutBack(%s) accepted", bad)
		}
	}
	if len(units.calls) != 0 {
		t.Fatalf("units = %v", units.calls)
	}
	if err := r.PutBack(context.Background(), svc, before); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(data, "then.txt")); string(got) != "then" {
		t.Error("the copy isn't back")
	}
	if got, _ := os.ReadFile(filepath.Join(data+".before-put-back-20260928-100000", "now.txt")); string(got) != "now" {
		t.Error("what it replaced isn't kept")
	}
	if strings.Join(units.calls, ",") != "stop roostack-forgejo-app.service,start roostack-forgejo-app.service" {
		t.Errorf("units = %v", units.calls)
	}
}
