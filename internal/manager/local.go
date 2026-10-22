package manager

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// The Owner's own packages ("Your own", SPEC.md §8.3.4): package files
// they add themselves, never fetched or reviewed. They're checked like any
// package (valid, renders, a valid logo), may break the package policy
// once the Owner has confirmed what breaks it, and are installed and kept
// in the lab like any other (§8.3.1).

// LocalPackageMaxBytes is the largest package file the Owner can add, the
// same as a lab takes from the registry (internal/registry's maxFile).
const LocalPackageMaxBytes = 1 << 20

// LocalPackagesDir is where the Owner's own packages are kept, for the
// page that adds them to say.
func (m *Manager) LocalPackagesDir() string { return m.Lab.Paths.LocalPackagesDir() }

// IsLocal reports whether a package is one of the Owner's own.
func (m *Manager) IsLocal(name string) bool {
	if !pkgspec.ValidName(name) {
		return false
	}
	_, err := os.Stat(m.Lab.Paths.LocalPackageDir(name))
	return err == nil
}

// PolicyCheck is how a package stands against the package policy (SPEC.md
// §8.3.4), for the install page.
type PolicyCheck struct {
	// Local is one of the Owner's own packages.
	Local bool
	// Problems is what breaks the policy: for the Owner's own, only what
	// could reach beyond its service (Package.Risks), which the Owner may
	// accept; for a registry package, everything (Package.Policy), which
	// stops it being installed.
	Problems []string
}

// CheckPackage checks a package Package returned against the policy.
func (m *Manager) CheckPackage(p *pkgspec.Package) PolicyCheck {
	if m.IsLocal(p.Name) {
		return PolicyCheck{Local: true, Problems: p.Risks()}
	}
	return PolicyCheck{Problems: p.Policy()}
}

// CheckPolicy refuses to install a registry package that breaks the
// package policy (a registry bug: its CI won't publish one), and one of
// the Owner's own that asks for something risky unless they've accepted
// it (SPEC.md §8.3.4).
func (m *Manager) CheckPolicy(p *pkgspec.Package, acceptRisks bool) error {
	c := m.CheckPackage(p)
	switch {
	case len(c.Problems) == 0:
		return nil
	case c.Local && acceptRisks:
		m.Log.Warn("installing a package of your own that breaks the package policy, as the Owner confirmed",
			"package", p.Name, "problems", c.Problems)
		return nil
	case c.Local:
		return usererr.New("%s asks for things packages from the service registry can't have. Read them above, and tick the box if you want to install it anyway.", p.Title)
	}
	for _, problem := range c.Problems {
		m.Log.Error("refusing to install a package that breaks the package policy", "package", p.Name, "problem", problem)
	}
	return usererr.New("%s %s can't be installed: it asks for something packages from the service registry aren't allowed, which the registry should have caught. Nothing has changed on your lab.", p.Title, p.Version)
}

// AddLocalPackage checks a package file the Owner gives (data) and its
// logo (icon, nil for none) and keeps them as one of their own packages,
// replacing whatever version of it they added before. What's wrong with
// either is a usererr.Error saying what to change.
func (m *Manager) AddLocalPackage(data, icon []byte) (*pkgspec.Package, error) {
	if len(data) == 0 {
		return nil, usererr.New("Choose a package file.")
	}
	if len(data) > LocalPackageMaxBytes {
		return nil, usererr.New("That file is too large for a package (at most %d KB).", LocalPackageMaxBytes/1000)
	}
	p, err := pkgspec.Parse(data)
	if err != nil {
		return nil, usererr.New("Roostack can't read that package: %s", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	problems, err := m.localProblems(p, data)
	if err != nil {
		return nil, err
	}
	if len(problems) > 0 {
		return nil, usererr.New("%s", problems[0])
	}
	switch {
	case p.About.Icon != "" && icon == nil:
		return nil, usererr.New("The package names a logo (%s): add it too, or take about.icon out.", p.About.Icon)
	case icon != nil:
		if err := pkgspec.CheckIcon(icon); err != nil {
			return nil, usererr.New("The logo: %s. It must be a square PNG, at least %d×%d pixels.", err, pkgspec.IconMinSize, pkgspec.IconMinSize)
		}
	}
	if err := state.SaveLocalPackage(m.Lab.Paths, p, data, icon); err != nil {
		return nil, err
	}
	m.Log.Info("added a package of your own", "package", p.Name, "version", p.Version, "risks", len(p.Risks()))
	return p, nil
}

// localProblems is what stops a valid package being added as one of the
// Owner's own, each a sentence saying what to change: it doesn't render,
// it needs a newer Roostack, its name is the registry's, or its version is
// in use with a different file. m.mu must be held.
func (m *Manager) localProblems(p *pkgspec.Package, data []byte) ([]string, error) {
	var problems []string
	if err := generate.Check(p); err != nil {
		problems = append(problems, fmt.Sprintf("Roostack can't set that package up: %s", err))
	}
	if !p.RunsOn(m.Version) {
		problems = append(problems, fmt.Sprintf("%s needs Roostack %s or newer. Update Roostack first, or change requires.roostack if it doesn't need it.", p.Title, p.Requires.Roostack))
	}
	// Every name is one package (decided by the Owner, 2026-10-14). A
	// service installed from a package of that name doesn't stop it: it
	// may have come from this one, removed from the list since.
	if !m.IsLocal(p.Name) {
		if _, err := state.CatalogVersion(m.Lab.Paths, p.Name); err == nil {
			problems = append(problems, fmt.Sprintf("The service registry has a package called %s already: give yours another name.", p.Name))
		}
	}
	// A service may use this version already, from the lab's copy, which
	// never changes (SPEC.md §8.3.1): a changed file needs a new version.
	kept, err := os.ReadFile(m.Lab.Paths.LabPackageFile(p.Name, p.Version))
	switch {
	case err == nil && !bytes.Equal(kept, data):
		problems = append(problems, fmt.Sprintf("A service uses %s %s already, and this file is different: give the changed package a new version.", p.Name, p.Version))
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	return problems, nil
}

// LocalPackageCheck is what Roostack says about a package of the Owner's
// own before it's added: the package editor's Check with this Roostack
// (SPEC.md §8.3.7, "The package editor"). Errors stop it being added
// (as AddLocalPackage would say); Policy is everything the registry's
// package policy refuses, and Risks the part a package of your own is
// confirmed with when it's installed.
type LocalPackageCheck struct {
	Errors []string `json:"errors"`
	Policy []string `json:"policy"`
	Risks  []string `json:"risks"`
}

// CheckLocalPackage checks a package as AddLocalPackage does, without
// adding it, and says everything it finds rather than the first.
func (m *Manager) CheckLocalPackage(data []byte) (LocalPackageCheck, error) {
	var c LocalPackageCheck
	if len(data) > LocalPackageMaxBytes {
		c.Errors = []string{fmt.Sprintf("That's too large for a package (at most %d KB).", LocalPackageMaxBytes/1000)}
		return c, nil
	}
	p, err := pkgspec.Parse(data)
	if err != nil {
		c.Errors = strings.Split(err.Error(), "\n")
		return c, nil
	}
	m.mu.Lock()
	c.Errors, err = m.localProblems(p, data)
	m.mu.Unlock()
	if err != nil {
		return c, err
	}
	c.Policy, c.Risks = p.Policy(), p.Risks()
	return c, nil
}

// RemoveLocalPackage deletes one of the Owner's own packages. Services
// installed from it keep running from the lab's copy (SPEC.md §8.3.1).
func (m *Manager) RemoveLocalPackage(name string) error {
	if !m.IsLocal(name) {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := state.RemoveLocalPackage(m.Lab.Paths, name); err != nil {
		return err
	}
	m.Log.Info("removed a package of your own", "package", name)
	return nil
}
