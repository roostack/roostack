package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"roostack.cloud/roostack/internal/config"
)

// direct runs "podman unshare <cmd>" as plain <cmd> (tests have no podman).
func direct(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	if name == "podman" && len(args) > 1 && args[0] == "unshare" {
		return exec.CommandContext(ctx, args[1], args[2:]...)
	}
	return exec.CommandContext(ctx, name, args...)
}

func TestKeptData(t *testing.T) {
	root := t.TempDir()
	lab := config.Default()
	lab.Paths.ServicesDir, lab.Paths.MediaDir = filepath.Join(root, "services"), filepath.Join(root, "media")
	n := &Local{Lab: lab, Launch: direct}
	mk := func(p string) {
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, "f"), []byte("12345"), 0o644)
	}
	moved := filepath.Join(lab.Paths.ServicesDir, "forgejo.moved-to-vps-20260928-002806")
	before := filepath.Join(lab.Paths.MediaDir, "immich.before-move-20260101-000000")
	live := filepath.Join(lab.Paths.ServicesDir, "forgejo")
	restoreKept := filepath.Join(lab.Paths.ServicesDir, "forgejo", "data.before-restore-20260101-000000")
	for _, p := range []string{moved, before, live, restoreKept} {
		mk(p)
	}
	ctx := context.Background()
	kept, err := n.KeptData(ctx)
	if err != nil || len(kept) != 2 {
		t.Fatalf("kept = %+v, %v", kept, err)
	}
	if kept[0].Path != moved || !kept[0].MovedAway || kept[0].Service != "forgejo" || kept[0].Size <= 0 {
		t.Errorf("newest = %+v", kept[0])
	}
	if kept[1].Path != before || kept[1].MovedAway {
		t.Errorf("older = %+v", kept[1])
	}
	for _, bad := range []string{live, restoreKept, "/etc", filepath.Join(root, "other.moved-x-20260101-000000"), moved + "/.."} {
		if err := n.RemoveKeptData(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: want ErrNotFound, got %v", bad, err)
		}
	}
	if err := n.RemoveKeptData(ctx, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(moved); !os.IsNotExist(err) {
		t.Error("not deleted")
	}
	if _, err := os.Stat(live); err != nil {
		t.Error("the live folder must stay")
	}
}
