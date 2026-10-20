package manager

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/state"
)

// forgejoIcon is the test fixture's logo, a valid one.
func forgejoIcon(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(filepath.Dir(pkgtest.File("forgejo")), "icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// unpinned is a package file with its first image's digest taken off,
// which breaks the package policy (SPEC.md §8.3.4).
func unpinned(data []byte) []byte {
	re := regexp.MustCompile(`@sha256:[0-9a-f]{64}`)
	done := false
	return re.ReplaceAllFunc(data, func(m []byte) []byte {
		if done {
			return m
		}
		done = true
		return nil
	})
}

func wantFormError(t *testing.T, err error, want string) {
	t.Helper()
	var fe *FormError
	if !errors.As(err, &fe) || !strings.Contains(fe.Msg, want) {
		t.Errorf("got %v, want a form error containing %q", err, want)
	}
}

// The Owner adds a package of their own, which is checked, listed as
// theirs, installed like any other and removed without touching the
// service installed from it (SPEC.md §8.3.4).
func TestLocalPackages(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	data, icon := pkgtest.Read(t, "forgejo"), forgejoIcon(t)

	_, err := m.AddLocalPackage(data, nil)
	wantFormError(t, err, "names a logo")
	_, err = m.AddLocalPackage(data, []byte("not a picture"))
	wantFormError(t, err, "The logo")
	_, err = m.AddLocalPackage([]byte("name: [not a package"), nil)
	wantFormError(t, err, "can't read that package")
	_, err = m.AddLocalPackage(nil, nil)
	wantFormError(t, err, "Choose a package file")
	// Every name is one package: the registry's (the catalog) has this one.
	_, err = m.AddLocalPackage(pkgtest.Read(t, "vaultwarden"), icon)
	wantFormError(t, err, "has a package called vaultwarden already")
	// Too new for this Roostack.
	m.Version = "v0.0.5"
	_, err = m.AddLocalPackage(data, icon)
	wantFormError(t, err, "needs Roostack 0.1 or newer")
	m.Version = "v0.1.0-3-gabc1234"

	p, err := m.AddLocalPackage(data, icon)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(m.Lab.Paths.LocalPackageDir("forgejo"), p.Version+".yaml")
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("the package file: %v %v", fi, err)
	}
	if !m.IsLocal("forgejo") || m.IsLocal("vaultwarden") {
		t.Error("IsLocal")
	}
	items, err := m.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, it := range items {
		if it.Name == "forgejo" {
			found = it.Local && it.Icon && it.Title == "Forgejo"
		}
		if it.Name != "forgejo" && it.Local {
			t.Errorf("%s isn't the Owner's own", it.Name)
		}
	}
	if !found {
		t.Errorf("the catalog doesn't list Forgejo as the Owner's own, with its logo: %+v", items)
	}
	if got, err := m.PackageFile("forgejo"); err != nil || !bytes.Equal(got, data) {
		t.Errorf("PackageFile: %v", err)
	}
	if got, err := m.PackageIcon("forgejo"); err != nil || !bytes.Equal(got, icon) {
		t.Errorf("PackageIcon: %v", err)
	}
	if c := m.CheckPackage(p); !c.Local || len(c.Problems) > 0 {
		t.Errorf("CheckPackage = %+v, want local with no problems", c)
	}

	if err := m.Install(ctx, "forgejo", forgejoRequest()); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	// The service records that it's the Owner's own, for its pages.
	if in, err := instance.Load(state.InstanceFile(m.Lab, "forgejo")); err != nil || !in.Local {
		t.Errorf("the instance should say it's from the Owner's own package: %+v %v", in, err)
	}
	kept, err := os.ReadFile(m.Lab.Paths.LabPackageFile("forgejo", p.Version))
	if err != nil || !bytes.Equal(kept, data) {
		t.Errorf("the lab's copy of the package: %v", err)
	}

	// A service uses this version: a changed file needs a new version.
	_, err = m.AddLocalPackage(append(bytes.Clone(data), "\n# changed\n"...), icon)
	wantFormError(t, err, "give the changed package a new version")
	// The same file again is fine (and a logo is needed again).
	if _, err := m.AddLocalPackage(data, icon); err != nil {
		t.Errorf("adding the same file again: %v", err)
	}

	if err := m.RemoveLocalPackage("forgejo"); err != nil {
		t.Fatal(err)
	}
	if m.IsLocal("forgejo") {
		t.Error("still there after removing it")
	}
	if err := m.RemoveLocalPackage("forgejo"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing it twice: %v", err)
	}
	if _, err := m.ServicePackage("forgejo"); err != nil {
		t.Errorf("the service installed from it: %v", err)
	}
	// Added back, say with a newer version for the service to update to.
	if _, err = m.AddLocalPackage(data, icon); err != nil {
		t.Errorf("adding it back (the installed service came from it): %v", err)
	}
}

// A registry package with the same name as one of the Owner's own isn't
// listed: theirs keeps the name.
func TestLocalPackageKeepsItsName(t *testing.T) {
	m, _ := newManager(t)
	if _, err := m.AddLocalPackage(pkgtest.Read(t, "forgejo"), forgejoIcon(t)); err != nil {
		t.Fatal(err)
	}
	addForgejo(t, m) // the registry publishes one later
	items, _ := m.Catalog()
	n := 0
	for _, it := range items {
		if it.Name == "forgejo" {
			n++
			if !it.Local {
				t.Error("the registry's Forgejo is listed in place of the Owner's own")
			}
		}
	}
	if n != 1 {
		t.Errorf("Forgejo listed %d times", n)
	}
}

// Installing checks the package policy (SPEC.md §8.3.4): a registry
// package that breaks it is refused; one of the Owner's own needs the
// Owner to accept what it asks for.
func TestInstallChecksPolicy(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()

	// A registry package that breaks it (a registry bug).
	vw := m.Lab.Paths.CatalogPackageDir("vaultwarden")
	version, err := state.CatalogVersion(m.Lab.Paths, "vaultwarden")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vw, version+".yaml"), unpinned(pkgtest.Read(t, "vaultwarden")), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := m.Package("vaultwarden")
	if err != nil {
		t.Fatal(err)
	}
	if c := m.CheckPackage(p); c.Local || len(c.Problems) != 1 {
		t.Errorf("CheckPackage = %+v, want one problem", c)
	}
	err = m.Install(ctx, "vaultwarden", InstallRequest{AcceptRisks: true})
	wantFormError(t, err, "can't be installed")
	if _, err := os.Stat(state.InstanceFile(m.Lab, "vaultwarden")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused install must not save anything")
	}

	// One of the Owner's own that breaks it.
	p, err = m.AddLocalPackage(unpinned(pkgtest.Read(t, "forgejo")), forgejoIcon(t))
	if err != nil {
		t.Fatal(err)
	}
	if c := m.CheckPackage(p); !c.Local || len(c.Problems) != 1 || !strings.Contains(c.Problems[0], "image: pin") {
		t.Errorf("CheckPackage = %+v, want the unpinned image", c)
	}
	err = m.Install(ctx, "forgejo", forgejoRequest())
	wantFormError(t, err, "tick the box")
	req := forgejoRequest()
	req.AcceptRisks = true
	if err := m.Install(ctx, "forgejo", req); err != nil {
		t.Fatal(err)
	}
	m.Wait()
}

// The package editor's Check with this Roostack says all that adding the
// package would, without adding it, and how it stands with the package
// policy (SPEC.md §8.3.7).
func TestCheckLocalPackage(t *testing.T) {
	m, _ := newManager(t)
	data := pkgtest.Read(t, "forgejo")

	c, err := m.CheckLocalPackage(data)
	if err != nil || len(c.Errors) != 0 || len(c.Policy) != 0 || len(c.Risks) != 0 {
		t.Fatalf("a good package: %+v %v", c, err)
	}
	if m.IsLocal("forgejo") {
		t.Error("checking added it")
	}
	c, _ = m.CheckLocalPackage(unpinned(data))
	if len(c.Errors) != 0 || len(c.Risks) != 1 || len(c.Policy) != 1 || !strings.Contains(c.Risks[0], "pin the image by digest") {
		t.Errorf("an unpinned image: %+v", c)
	}
	c, _ = m.CheckLocalPackage([]byte("schema: roostack/v1alpha1\nname: Bad\n"))
	if len(c.Errors) < 2 || !strings.Contains(strings.Join(c.Errors, "\n"), `name: "Bad" must be lowercase`) {
		t.Errorf("an invalid package: every problem, one a line: %+v", c)
	}
	// What adding refuses, all at once: the registry's name, and too new.
	m.Version = "v0.0.5"
	c, _ = m.CheckLocalPackage(pkgtest.Read(t, "vaultwarden"))
	if all := strings.Join(c.Errors, "\n"); !strings.Contains(all, "needs Roostack 0.1 or newer") || !strings.Contains(all, "has a package called vaultwarden already") {
		t.Errorf("adding would refuse it twice over: %+v", c)
	}
}
