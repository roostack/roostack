package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
)

// A package's shown files (SPEC.md §8.1, "Shown on its page"): read from
// its storage, trimmed and at most MaxShown; "" for one not made yet, or
// one whose path leads out of its storage through a link.
func TestShownFiles(t *testing.T) {
	lab := config.Default()
	lab.Paths.ServicesDir = t.TempDir()
	p := &pkgspec.Package{Name: "rustdesk", Storage: map[string]*pkgspec.Storage{"data": {Class: pkgspec.ClassCritical}},
		Shown: []pkgspec.Shown{{Title: "Key", Storage: "data", Path: "id_ed25519.pub"}, {Title: "Not yet", Storage: "data", Path: "later"},
			{Title: "Out", Storage: "data", Path: "out"}}}
	n := &Local{Lab: lab}
	n.assignment = Assignment{Services: []state.Service{{Package: p, Instance: &instance.Instance{Package: "rustdesk"}}}, Lab: LabContext{}}
	dir := filepath.Join(lab.Paths.ServicesDir, "rustdesk", "data")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "id_ed25519.pub"), []byte("PUBKEY=\n"), 0o644)
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("not yours"), 0o600)
	os.Symlink(secret, filepath.Join(dir, "out"))
	got, err := n.ShownFiles(context.Background(), "rustdesk")
	if err != nil || len(got) != 3 || got[0] != "PUBKEY=" || got[1] != "" || got[2] != "" {
		t.Errorf("shown: %q %v", got, err)
	}
	if _, err := n.ShownFiles(context.Background(), "nope"); err != ErrNotFound {
		t.Errorf("an unknown service: %v", err)
	}
}
