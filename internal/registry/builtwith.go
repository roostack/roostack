package registry

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/pkgspec"
)

// BuiltWithFile is the registry's list of the names packages may give in
// built_with: (SPEC.md §6.16; docs/RED-BUTTON-PLAN.md §4.1), at the top of
// the registry repository. It's kept by the registry, not Roostack, so a
// new name (a library with a zero-day) needs no Roostack release, and it's
// published in the index so labs can show each name's title. Optional:
// without it, packages' names aren't checked against a list.
const BuiltWithFile = "built-with.yaml"

// BuiltWith is one name in the registry's list: its title and a line
// about it for people, and the package identifiers (package URLs, CPE
// names) that the registry's watcher will match vulnerability feeds
// against.
type BuiltWith struct {
	Name  string   `yaml:"name" json:"name"`
	Title string   `yaml:"title" json:"title"`
	About string   `yaml:"about" json:"about,omitempty"`
	Purls []string `yaml:"purls" json:"purls,omitempty"`
	CPEs  []string `yaml:"cpes" json:"cpes,omitempty"`
}

// ReadBuiltWith reads a registry repository's list of built-with names, or
// nil without one (the watcher matches on its package URLs and CPEs).
func ReadBuiltWith(repo string) ([]BuiltWith, error) { return readBuiltWith(repo) }

// readBuiltWith reads the registry's list, or nil without one.
func readBuiltWith(repo string) ([]BuiltWith, error) {
	data, err := os.ReadFile(filepath.Join(repo, BuiltWithFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []BuiltWith
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("%s: %w", BuiltWithFile, err)
	}
	if err := checkBuiltWith(list); err != nil {
		return nil, fmt.Errorf("%s: %w", BuiltWithFile, err)
	}
	return list, nil
}

// checkBuiltWith checks a list of names: each a name built_with: takes,
// once, with a short title; identifiers in their own formats.
func checkBuiltWith(list []BuiltWith) error {
	seen := map[string]bool{}
	var errs []error
	for i, b := range list {
		at := fmt.Sprintf("[%d] %s", i, b.Name)
		switch {
		case !pkgspec.ValidBuiltWith(b.Name):
			errs = append(errs, fmt.Errorf("%s: not a name built_with: takes (lowercase letters, digits and dashes)", at))
		case seen[b.Name]:
			errs = append(errs, fmt.Errorf("%s: listed twice", at))
		}
		seen[b.Name] = true
		if b.Title == "" || len(b.Title) > 40 || strings.ContainsAny(b.Title, "\n<>") {
			errs = append(errs, fmt.Errorf("%s: give it a title of up to 40 characters, as people would write it (PHP, Log4j)", at))
		}
		if len(b.About) > 200 || strings.ContainsAny(b.About, "\n<>") {
			errs = append(errs, fmt.Errorf("%s: about is one line of up to 200 characters", at))
		}
		for _, p := range b.Purls {
			if !strings.HasPrefix(p, "pkg:") || strings.ContainsAny(p, " \n") {
				errs = append(errs, fmt.Errorf("%s: %q isn't a package URL (pkg:type/namespace/name)", at, p))
			}
		}
		for _, c := range b.CPEs {
			if !strings.HasPrefix(c, "cpe:2.3:") || strings.ContainsAny(c, " \n") {
				errs = append(errs, fmt.Errorf("%s: %q isn't a CPE 2.3 name (cpe:2.3:a:vendor:product:…)", at, c))
			}
		}
	}
	return errors.Join(errs...)
}

// BuiltWithTitle is a name's title from the registry's list, or "" when
// the list doesn't have it (a package of the Owner's own may use any name).
func (ix *Index) BuiltWithTitle(name string) string {
	if ix == nil {
		return ""
	}
	for _, b := range ix.BuiltWith {
		if b.Name == name {
			return b.Title
		}
	}
	return ""
}
