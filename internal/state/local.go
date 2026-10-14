package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/pkgspec"
)

// The Owner's own packages ("Your own", SPEC.md §8.3.4) are kept in
// config.Paths.LocalPackagesDir, a folder each, laid out like the
// catalog's: <version>.yaml and the logo. Each has one version: adding a
// newer one replaces it (services installed from it keep the lab's copy,
// §8.3.1).

// LocalIcon is the file name the logo of one of the Owner's own packages
// is kept under when its package doesn't name one.
const LocalIcon = "icon.png"

// LocalPackageNames lists the Owner's own packages, by folder name.
func LocalPackageNames(paths config.Paths) ([]string, error) {
	entries, err := os.ReadDir(paths.LocalPackagesDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && pkgspec.ValidName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// LoadLocalPackage reads one of the Owner's own packages, returning an
// error wrapping os.ErrNotExist when there's no such package.
func LoadLocalPackage(paths config.Paths, name string) (*pkgspec.Package, error) {
	if !pkgspec.ValidName(name) {
		return nil, fmt.Errorf("package %q: %w", name, os.ErrNotExist)
	}
	dir := paths.LocalPackageDir(name)
	version, err := newestVersion(dir)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, version+".yaml")
	p, err := pkgspec.Load(path)
	if err != nil {
		return nil, err
	}
	if p.Name != name || p.Version != version {
		return nil, fmt.Errorf("%s is %s %s, not %s %s", path, p.Name, p.Version, name, version)
	}
	return p, nil
}

// LocalIconFile is where the logo of one of the Owner's own packages is
// kept: the file its package names, or LocalIcon.
func LocalIconFile(paths config.Paths, p *pkgspec.Package) string {
	icon := p.About.Icon
	if icon == "" {
		icon = LocalIcon
	}
	return filepath.Join(paths.LocalPackageDir(p.Name), icon)
}

// SaveLocalPackage keeps a package file the Owner added (data, already
// checked as p) and its logo (nil for none) as one of their own packages,
// replacing whatever version of it they added before, logo included.
func SaveLocalPackage(paths config.Paths, p *pkgspec.Package, data, icon []byte) error {
	if !pkgspec.ValidName(p.Name) || !pkgspec.ValidVersion(p.Version) {
		return fmt.Errorf("package %q version %q: not a valid package name and version", p.Name, p.Version)
	}
	dir := paths.LocalPackageDir(p.Name)
	// Written beside it, then swapped in, so a failure leaves the old one.
	tmp := dir + ".new"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	// Not secret: 0644, like the lab's copies (SPEC.md §8.3.1).
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tmp, p.Version+".yaml"), data, 0o644); err != nil {
		return err
	}
	if icon != nil {
		if err := os.WriteFile(filepath.Join(tmp, filepath.Base(LocalIconFile(paths, p))), icon, 0o644); err != nil {
			return err
		}
	}
	old := dir + ".old"
	if err := os.RemoveAll(old); err != nil {
		return err
	}
	if err := os.Rename(dir, old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	return os.RemoveAll(old)
}

// RemoveLocalPackage deletes one of the Owner's own packages. Services
// installed from it keep the lab's copy (SPEC.md §8.3.1).
func RemoveLocalPackage(paths config.Paths, name string) error {
	if !pkgspec.ValidName(name) {
		return fmt.Errorf("package %q: %w", name, os.ErrNotExist)
	}
	dir := paths.LocalPackageDir(name)
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	return os.RemoveAll(dir)
}
