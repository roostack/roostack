package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mylab/mylab/internal/generate"
)

func TestSnapshotsFor(t *testing.T) {
	svc := &generate.Output{Service: "forgejo", Backup: generate.BackupPlan{
		Include: []string{"/srv/mylab/services/forgejo/data", "/srv/mylab/services/forgejo/db-dump"}}}
	day := func(d int) time.Time { return time.Date(2026, 9, d, 2, 0, 0, 0, time.UTC) }
	snaps := []Snapshot{
		{Short: "a", Time: day(25), Paths: []string{"/var/lib/mylab"}}, // before Forgejo was installed
		{Short: "b", Time: day(26), Paths: append([]string{"/var/lib/mylab"}, svc.Backup.Include...)},
		{Short: "c", Time: day(27), Paths: append([]string{"/var/lib/mylab"}, svc.Backup.Include...)},
	}
	got := SnapshotsFor(snaps, svc)
	if len(got) != 2 || got[0].Short != "c" || got[1].Short != "b" {
		t.Errorf("got %+v, want c then b", got)
	}
}

func TestKeptCopies(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "forgejo", "data")
	svc := &generate.Output{Service: "forgejo", Backup: generate.BackupPlan{Include: []string{data}}}
	for _, d := range []string{data, data + ".before-restore-20260926-130602", data + ".failed-restore-20260927-090000",
		data + ".before-restore-notatime", filepath.Join(root, "other.before-restore-20260926-130602")} {
		os.MkdirAll(d, 0o750)
	}
	kept, err := KeptCopies(svc)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 2 || !kept[0].Failed || kept[1].Failed || kept[1].Of != data {
		t.Fatalf("kept = %+v", kept)
	}

	var ran []string
	r := &Restorer{Launch: func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return exec.Command("true")
	}}
	for _, bad := range []string{data, filepath.Join(root, "other.before-restore-20260926-130602"), "/", data + ".before-restore-notatime"} {
		if err := r.RemoveKept(context.Background(), svc, bad); err == nil {
			t.Errorf("RemoveKept(%s) accepted", bad)
		}
	}
	if err := r.RemoveKept(context.Background(), svc, kept[1].Path); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "podman unshare rm -rf -- "+kept[1].Path {
		t.Errorf("ran %q", ran)
	}
}
