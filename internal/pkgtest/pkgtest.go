// Package pkgtest finds the package fixtures tests use
// (internal/testdata/packages), copies of packages from the registry in
// its layout: <name>/<version>.yaml beside icon.png and registry.yaml
// (SPEC.md §8.3.3). It has no dependencies, so any package's tests can use
// it, pkgspec's included.
package pkgtest

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Names are the packages there are fixtures for.
var Names = []string{"forgejo", "immich", "mealie", "stirling-pdf", "vaultwarden"}

// Dir is the fixtures' folder, found from the working directory (a test
// runs in its package's folder) by looking up for go.mod, so it works
// wherever the tests are built and run (scripts/dev-vm.sh test builds them
// on one machine and runs them on another).
func Dir() string {
	dir, err := os.Getwd()
	if err != nil {
		panic("pkgtest: " + err.Error())
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "internal", "testdata", "packages")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("pkgtest: no go.mod above the working directory")
		}
		dir = parent
	}
}

// File is the path of a fixture's package file. Each fixture has one
// version.
func File(name string) string {
	files, _ := filepath.Glob(filepath.Join(Dir(), name, "*.yaml"))
	for _, f := range files {
		if filepath.Base(f) != "registry.yaml" {
			return f
		}
	}
	panic("pkgtest: no package file for " + name)
}

// Read returns a fixture's package file.
func Read(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(File(name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Catalog copies every fixture into dir as a catalog, in the layout of
// config.Paths.CatalogDir.
func Catalog(t testing.TB, dir string) {
	t.Helper()
	err := filepath.WalkDir(Dir(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, ".md") {
			return err
		}
		rel, _ := filepath.Rel(Dir(), path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
