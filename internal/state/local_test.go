package state

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// The Owner's own packages: one version each, replaced whole (logo
// included), the lab's copy taken from them, and removed (SPEC.md §8.3.4).
func TestLocalPackages(t *testing.T) {
	lab := newLab(t)
	paths := lab.Paths
	os.RemoveAll(paths.CatalogPackageDir("forgejo")) // only the Owner's own
	data := pkgtest.Read(t, "forgejo")
	p, err := pkgspec.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if names, err := LocalPackageNames(paths); err != nil || len(names) != 0 {
		t.Fatalf("none yet: %v %v", names, err)
	}
	if _, err := LoadLocalPackage(paths, "forgejo"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("none yet: %v", err)
	}
	if err := SaveLocalPackage(paths, p, data, []byte("logo")); err != nil {
		t.Fatal(err)
	}
	if names, _ := LocalPackageNames(paths); !slices.Equal(names, []string{"forgejo"}) {
		t.Errorf("names = %v", names)
	}
	got, err := LoadLocalPackage(paths, "forgejo")
	if err != nil || got.Version != p.Version {
		t.Fatalf("loaded %v, %v", got, err)
	}
	if logo, err := os.ReadFile(LocalIconFile(paths, p)); err != nil || string(logo) != "logo" {
		t.Errorf("logo: %q %v", logo, err)
	}

	// The lab's copy comes from it, as installing takes it.
	if err := KeepPackage(paths, "forgejo", p.Version); err != nil {
		t.Fatal(err)
	}
	if kept, _ := os.ReadFile(paths.LabPackageFile("forgejo", p.Version)); string(kept) != string(data) {
		t.Error("the lab's copy isn't the Owner's file")
	}

	// A newer version replaces it, and the old logo goes with it.
	newer := *p
	newer.Version = "15.0.10-roostack.1"
	if err := SaveLocalPackage(paths, &newer, data, nil); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(paths.LocalPackageDir("forgejo"))
	if len(files) != 1 || files[0].Name() != "15.0.10-roostack.1.yaml" {
		t.Errorf("after replacing: %v", files)
	}
	if entries, _ := os.ReadDir(paths.LocalPackagesDir()); len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}

	if err := RemoveLocalPackage(paths, "forgejo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.LocalPackageDir("forgejo")); !errors.Is(err, os.ErrNotExist) {
		t.Error("still there")
	}
	if err := RemoveLocalPackage(paths, "forgejo"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("removing it twice: %v", err)
	}
	if _, err := os.Stat(filepath.Join(paths.LabPackageFile("forgejo", p.Version))); err != nil {
		t.Error("the lab's copy goes only when no service uses it (PrunePackages)")
	}
}
