package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/pkgspec"
)

// The lab keeps the exact package file of every version its services use
// (SPEC.md §8.3.1): generation reads that copy, never the catalog, so a
// restore needs nothing but the backup and a newer catalog never changes a
// running service. The catalog is only what can be installed.

// KeepPackage copies the catalog's package file into the lab's state as
// the copy its services use, unless the lab already has that version. It
// fails if the catalog's package isn't that version.
func KeepPackage(paths config.Paths, name, version string) error {
	if !pkgspec.ValidName(name) || !pkgspec.ValidVersion(version) {
		return fmt.Errorf("package %q version %q: not a valid package name and version", name, version)
	}
	dst := paths.LabPackageFile(name, version)
	if _, err := os.Stat(dst); err == nil {
		return nil
	}
	src := paths.PackageFile(name)
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	p, err := pkgspec.Parse(data)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}
	if p.Name != name || p.Version != version {
		return fmt.Errorf("%s is %s %s, not %s %s", src, p.Name, p.Version, name, version)
	}
	// Not secret: 0644, so people can read what runs (SPEC.md §8.3.1).
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// LoadPackage reads the lab's copy of a package version. A service
// installed before the lab kept copies gets one from the catalog, if the
// catalog still has its version.
func LoadPackage(paths config.Paths, name, version string) (*pkgspec.Package, error) {
	if !pkgspec.ValidName(name) || !pkgspec.ValidVersion(version) {
		return nil, fmt.Errorf("package %q version %q: not a valid package name and version", name, version)
	}
	path := paths.LabPackageFile(name, version)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := KeepPackage(paths, name, version); err != nil {
			return nil, fmt.Errorf("the lab has no copy of %s %s (%s), and it can't be taken from the catalog: %w",
				name, version, path, err)
		}
	}
	p, err := pkgspec.Load(path)
	if err != nil {
		return nil, err
	}
	if p.Name != name || p.Version != version {
		return nil, fmt.Errorf("%s is %s %s, not %s %s", path, p.Name, p.Version, name, version)
	}
	return p, nil
}

// PrunePackages deletes the lab's copies of package versions no service
// uses any more. Call it with every service in the lab, after they all
// loaded.
func PrunePackages(paths config.Paths, services []Service) error {
	used := map[string]bool{}
	for _, s := range services {
		used[paths.LabPackageFile(s.Instance.Package, s.Instance.Version)] = true
	}
	dirs, err := os.ReadDir(paths.PackagesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(paths.PackagesDir(), d.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		left := len(files)
		for _, f := range files {
			path := filepath.Join(dir, f.Name())
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".yaml") || used[path] {
				continue
			}
			if err := os.Remove(path); err != nil {
				errs = append(errs, err)
				continue
			}
			left--
		}
		if left == 0 {
			if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}
