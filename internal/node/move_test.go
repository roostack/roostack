package node

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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

// member is one entry of a test tar stream.
type member struct {
	name, link string
	typ        byte
}

func tarStream(t *testing.T, members ...member) *bytes.Reader {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, m := range members {
		h := &tar.Header{Name: m.name, Linkname: m.link, Typeflag: m.typ, Mode: 0o644}
		data := []byte("pwned\n")
		switch m.typ {
		case tar.TypeReg:
			h.Size = int64(len(data))
		case tar.TypeDir:
			h.Mode = 0o755
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if m.typ == tar.TypeReg {
			tw.Write(data)
		}
	}
	tw.Close()
	return bytes.NewReader(b.Bytes())
}

// A hostile stream can't put anything outside the folder it's unpacked
// in, even through a link left there by an earlier pass of the copy
// (docs/ASSUMPTIONS.md L19). Runs the real tar.
func TestImportDataStaysInside(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("no tar")
	}
	root := t.TempDir()
	lab := config.Default()
	lab.Paths.ServicesDir, lab.Paths.MediaDir = filepath.Join(root, "services"), filepath.Join(root, "media")
	n := &Local{Lab: lab, Launch: direct}
	outside := filepath.Join(root, "outside")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "victim"), []byte("original\n"), 0o644)
	ctx := context.Background()
	in := filepath.Join(lab.Paths.ServicesDir, "forgejo"+staging)

	// A first pass with links (allowed: data may have links) and a file.
	err := n.ImportData(ctx, "forgejo", 0, tarStream(t,
		member{name: "./data/", typ: tar.TypeDir},
		member{name: "./data/a", typ: tar.TypeReg},
		member{name: "./out", link: outside, typ: tar.TypeSymlink},
		member{name: "./up", link: "../../outside", typ: tar.TypeSymlink},
		member{name: "./data/b", link: "./data/a", typ: tar.TypeLink}), 0)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(in, "out")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link: %v %v", fi, err)
	}
	// Later passes that would write through those links are refused.
	for _, bad := range [][]member{
		{{name: "./out/esc", typ: tar.TypeReg}},
		{{name: "./up/esc", typ: tar.TypeReg}},
		{{name: "./out/", typ: tar.TypeDir}, {name: "./out/esc", typ: tar.TypeReg}},
		{{name: "./h", link: "./out/victim", typ: tar.TypeLink}},
		{{name: "./l2", link: "/", typ: tar.TypeSymlink}, {name: "./l2/esc", typ: tar.TypeReg}},
		{{name: "../esc", typ: tar.TypeReg}},
		{{name: "./data/../../esc", typ: tar.TypeReg}},
		{{name: "./h2", link: "../outside/victim", typ: tar.TypeLink}},
	} {
		if err := n.ImportData(ctx, "forgejo", 0, tarStream(t, bad...), 0); err == nil {
			t.Errorf("%+v was taken", bad)
		}
	}
	// With a backup's full paths stripped, the same.
	err = n.ImportData(ctx, "forgejo", 0, tarStream(t, member{name: "srv/roostack/services/forgejo/out/esc", typ: tar.TypeReg}), 4)
	if err == nil {
		t.Error("stripped: taken")
	}
	err = n.ImportData(ctx, "forgejo", 0, tarStream(t, member{name: "srv/roostack/services/forgejo/data/c", typ: tar.TypeReg},
		member{name: "srv/roostack/services/forgejo/data/d", link: "srv/roostack/services/forgejo/data/c", typ: tar.TypeLink}), 4)
	if err != nil {
		t.Errorf("stripped: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(in, "data", "d")); string(b) != "pwned\n" {
		t.Errorf("stripped hard link: %q", b)
	}
	// A file in place of a link is fine (tar removes the link first).
	if err := n.ImportData(ctx, "forgejo", 0, tarStream(t, member{name: "./out", typ: tar.TypeReg}), 0); err != nil {
		t.Errorf("a file over a link: %v", err)
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 1 {
		t.Errorf("outside has %d entries", len(entries))
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "victim")); string(b) != "original\n" {
		t.Errorf("victim = %q", b)
	}
	if fi, err := os.Stat(filepath.Join(outside, "victim")); err != nil || fi.Sys().(*syscall.Stat_t).Nlink != 1 {
		t.Error("something links to the victim")
	}
}
