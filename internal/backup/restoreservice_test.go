package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/generate"
)

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
