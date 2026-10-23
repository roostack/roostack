package watch

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
)

// Package is one of the registry's packages, as the watcher needs it.
type Package struct {
	Name, Title string
	// Repo is its source's GitHub repository ("owner/name"), whose own
	// security advisories the watcher reads; "" when it isn't on GitHub.
	Repo string
	// Source is its about.source, for saying what isn't watched.
	Source string
	// Versions are the registry's versions of it, oldest first.
	Versions []string
	// BuiltWith are its built-with names, databases' included.
	BuiltWith []string
	// Images and Names are each version's images (its containers', and
	// its databases' where it names one) and built-with names.
	Images, Names map[string][]string
}

// Upstream is a package version's app version (1.37.4 for
// 1.37.4-roostack.3).
func Upstream(version string) string { return pkgspec.Upstream(version) }

// Repo is what the watcher reads of the registry repository.
type Repo struct {
	Dir       string
	Packages  []Package
	BuiltWith []registry.BuiltWith
	// Advisories are those published already: their IDs, and the CVE and
	// advisory IDs they're about (from cves and the link), so nothing is
	// drafted twice.
	Advisories map[string][]string
	// BOMs are the package URLs, with versions, in each package version's
	// bills of materials ("ntfy 2.31.1-roostack.1"), and Bills the bills
	// themselves, one for each image (bom.go).
	BOMs  map[string][]string
	Bills map[string][]BOM
}

var githubRepoRe = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(\.git)?/?$`)

// GitHubRepo is a source address's GitHub repository ("owner/name"), or
// "" for anything else.
func GitHubRepo(source string) string {
	m := githubRepoRe.FindStringSubmatch(source)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// ReadRepo reads a registry repository checkout.
func ReadRepo(dir string) (*Repo, error) {
	r := &Repo{Dir: dir, Advisories: map[string][]string{}, BOMs: map[string][]string{}, Bills: map[string][]BOM{}}
	var err error
	if r.BuiltWith, err = registry.ReadBuiltWith(dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "packages"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(dir, "packages", e.Name(), "*.yaml"))
		var pkg *Package
		for _, f := range files {
			if filepath.Base(f) == "registry.yaml" {
				continue
			}
			p, err := pkgspec.Load(f)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			if pkg == nil {
				pkg = &Package{Name: p.Name, Images: map[string][]string{}, Names: map[string][]string{}}
			}
			pkg.Images[p.Version], pkg.Names[p.Version] = packageImages(p), p.BuiltWithNames()
			// The newest version's title, source and names stand for them all.
			if len(pkg.Versions) == 0 || pkgspec.CompareVersions(p.Version, pkg.Versions[len(pkg.Versions)-1]) > 0 {
				pkg.Title, pkg.Source, pkg.Repo, pkg.BuiltWith = p.Title, p.About.Source, GitHubRepo(p.About.Source), p.BuiltWithNames()
			}
			pkg.Versions = append(pkg.Versions, p.Version)
		}
		if pkg == nil {
			continue
		}
		slices.SortFunc(pkg.Versions, pkgspec.CompareVersions)
		r.Packages = append(r.Packages, *pkg)
	}
	if err := r.readAdvisories(); err != nil {
		return nil, err
	}
	if err := r.readBOMs(); err != nil {
		return nil, err
	}
	return r, nil
}

// readAdvisories reads the published advisories' IDs, CVEs and links.
func (r *Repo) readAdvisories() error {
	files, _ := filepath.Glob(filepath.Join(r.Dir, registry.AdvisoriesDir, "*.yaml"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var a struct {
			CVEs []string `yaml:"cves"`
			Link string   `yaml:"link"`
		}
		if err := yaml.Unmarshal(data, &a); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		about := slices.Clone(a.CVEs)
		if id := idInLink(a.Link); id != "" {
			about = append(about, id)
		}
		r.Advisories[strings.TrimSuffix(filepath.Base(f), ".yaml")] = about
	}
	return nil
}

var advisoryIDInLink = regexp.MustCompile(`(GHSA(-[23456789cfghjmpqrvwx]{4}){3}|CVE-[0-9]{4}-[0-9]{4,})`)

// idInLink is the GitHub advisory or CVE ID an advisory's link names.
func idInLink(link string) string {
	return advisoryIDInLink.FindString(link)
}

// Published says whether an advisory about any of ids is published.
func (r *Repo) Published(ids ...string) bool {
	for _, about := range r.Advisories {
		for _, id := range ids {
			if id != "" && slices.Contains(about, id) {
				return true
			}
		}
	}
	return false
}

// NextID is the next free advisory ID in a year (2026-0007), counting the
// published ones and those taken already in this run.
func (r *Repo) NextID(year int, taken []string) string {
	n := 0
	for _, id := range append(slices.Collect(mapKeys(r.Advisories)), taken...) {
		y, num, ok := strings.Cut(id, "-")
		if !ok || y != strconv.Itoa(year) {
			continue
		}
		if v, err := strconv.Atoi(num); err == nil && v > n {
			n = v
		}
	}
	return fmt.Sprintf("%d-%04d", year, n+1)
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// Package is the named package, if the registry has it.
func (r *Repo) Package(name string) *Package {
	i := slices.IndexFunc(r.Packages, func(p Package) bool { return p.Name == name })
	if i < 0 {
		return nil
	}
	return &r.Packages[i]
}

// WriteAdvisory writes a draft advisory into the repository's advisories
// folder, refusing to overwrite one.
func (r *Repo) WriteAdvisory(id string, data []byte) (string, error) {
	path := filepath.Join(r.Dir, registry.AdvisoriesDir, id+".yaml")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s exists already", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	return path, os.WriteFile(path, data, 0o644)
}

// isHTTPS is whether a link is an https address, as advisory links must be.
func isHTTPS(link string) bool {
	u, err := url.Parse(link)
	return err == nil && u.Scheme == "https" && u.Host != ""
}
