package registry

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/pkgspec"
)

// Security advisories (SPEC.md §6.16; docs/RED-BUTTON-PLAN.md §5): a
// problem the registry's maintainers publish, naming the exact package
// versions it affects and the version that fixes it, reviewed like any
// change to the registry and signed with the index. A lab matches them to
// what it runs and, as its Owner chose, pauses affected services' public
// access until they're updated. The most a forged advisory could do is
// pause public access, which the Owner can override.

// AdvisoriesDir is the registry repository's folder of advisories, one
// file each, named after its ID (advisories/2026-0007.yaml).
const AdvisoriesDir = "advisories"

// Severity is Roostack's own (not CVSS), judged for how Roostack runs the
// app: critical is usable by anyone on the internet without signing in,
// to run code or read or change data; high is serious but needs more (an
// account, a setting that isn't the default); low is worth knowing.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityLow      = "low"
)

// Advisory is one security problem in the index.
type Advisory struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// BuiltWith is what it's about, a name from the registry's list
	// (php), if it's about something packages are built with.
	BuiltWith string   `json:"built_with,omitempty"`
	CVEs      []string `json:"cves,omitempty"`
	Severity  string   `json:"severity"`
	// Exploited: it's being used in attacks now (on a list of known
	// exploited vulnerabilities, or the project says so).
	Exploited bool `json:"exploited,omitempty"`
	// Published is the day it was published (YYYY-MM-DD).
	Published string `json:"published"`
	Link      string `json:"link,omitempty"`
	// Affects are the registry's packages it affects, by name.
	Affects map[string]*Affected `json:"affects"`
	// Local: packages of an Owner's own built with BuiltWith may be
	// affected too, so labs say so (never pausing them by themselves).
	Local bool `json:"local,omitempty"`
}

// Affected is the package versions an advisory affects, each with its
// file's SHA-256 (so a package of the Owner's own with the same name and
// version isn't matched), and the version that fixes it, if there's one.
type Affected struct {
	Versions map[string]string `json:"versions"`
	Fixed    string            `json:"fixed,omitempty"`
}

// advisoryFile is an advisory as the registry repository has it.
type advisoryFile struct {
	Title     string   `yaml:"title"`
	BuiltWith string   `yaml:"built_with"`
	CVEs      []string `yaml:"cves"`
	Severity  string   `yaml:"severity"`
	Exploited bool     `yaml:"exploited"`
	Published string   `yaml:"published"`
	Link      string   `yaml:"link"`
	Affects   map[string]struct {
		Versions []string `yaml:"versions"`
		Fixed    string   `yaml:"fixed"`
	} `yaml:"affects"`
	// Local is "possibly" (own packages built with BuiltWith may be
	// affected) or "no", the default.
	Local string `yaml:"local"`
}

var (
	advisoryIDRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{4}$`)
	cveRe        = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,}$`)
)

// readAdvisories reads the registry repository's advisories, turning
// each affected version into its file's SHA-256 from ix's packages (built
// first). A version that isn't in the registry fails the build.
func readAdvisories(repo string, ix *Index) ([]*Advisory, error) {
	dir := filepath.Join(repo, AdvisoriesDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Advisory
	var errs []error
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".yaml")
		if e.IsDir() || !ok {
			continue
		}
		at := AdvisoriesDir + "/" + e.Name()
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var f advisoryFile
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", at, err))
			continue
		}
		a := &Advisory{ID: id, Title: f.Title, BuiltWith: f.BuiltWith, CVEs: f.CVEs, Severity: f.Severity, Exploited: f.Exploited,
			Published: f.Published, Link: f.Link, Affects: map[string]*Affected{}, Local: f.Local == "possibly"}
		if f.Local != "" && f.Local != "possibly" && f.Local != "no" {
			errs = append(errs, fmt.Errorf("%s: local: %q must be possibly or no", at, f.Local))
		}
		for _, pkg := range slices.Sorted(maps.Keys(f.Affects)) {
			af := f.Affects[pkg]
			p := ix.Packages[pkg]
			if p == nil {
				errs = append(errs, fmt.Errorf("%s: affects %s, which isn't in the registry", at, pkg))
				continue
			}
			aff := &Affected{Versions: map[string]string{}, Fixed: af.Fixed}
			for _, v := range af.Versions {
				pv := p.Versions[v]
				if pv == nil {
					errs = append(errs, fmt.Errorf("%s: affects %s %s, which isn't in the registry", at, pkg, v))
					continue
				}
				aff.Versions[v] = pv.SHA256
			}
			if af.Fixed != "" && p.Versions[af.Fixed] == nil {
				errs = append(errs, fmt.Errorf("%s: %s is fixed in %s, which isn't in the registry yet: publish the fix first, or leave fixed out", at, pkg, af.Fixed))
			}
			a.Affects[pkg] = aff
		}
		if err := a.check(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", at, err))
			continue
		}
		out = append(out, a)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// check checks what a lab relies on: an ID, a title, a severity, the
// affected versions' names and hashes.
func (a *Advisory) check() error {
	var errs []error
	if !advisoryIDRe.MatchString(a.ID) {
		errs = append(errs, fmt.Errorf("ID %q must be the year and a number, as 2026-0007", a.ID))
	}
	if a.Title == "" || len(a.Title) > 120 || strings.ContainsAny(a.Title, "\n<>") {
		errs = append(errs, errors.New("title: one line of up to 120 characters, saying what the problem lets someone do"))
	}
	if a.BuiltWith != "" && !pkgspec.ValidBuiltWith(a.BuiltWith) {
		errs = append(errs, fmt.Errorf("built_with: %q isn't a name built_with: takes", a.BuiltWith))
	}
	for _, c := range a.CVEs {
		if !cveRe.MatchString(c) {
			errs = append(errs, fmt.Errorf("cves: %q isn't a CVE ID, as CVE-2026-41234", c))
		}
	}
	if !slices.Contains([]string{SeverityCritical, SeverityHigh, SeverityLow}, a.Severity) {
		errs = append(errs, fmt.Errorf("severity: %q must be critical, high or low", a.Severity))
	}
	if _, err := time.Parse(time.DateOnly, a.Published); err != nil {
		errs = append(errs, fmt.Errorf("published: %q must be a date, as 2026-10-22", a.Published))
	}
	if a.Link != "" {
		if u, err := url.Parse(a.Link); err != nil || u.Scheme != "https" || u.Host == "" {
			errs = append(errs, fmt.Errorf("link: %q must be an https:// address", a.Link))
		}
	}
	if len(a.Affects) == 0 && !a.Local {
		errs = append(errs, errors.New("affects: name the package versions it affects (or local: possibly for packages of the Owners' own)"))
	}
	for pkg, aff := range a.Affects {
		if !pkgspec.ValidName(pkg) || aff == nil || len(aff.Versions) == 0 {
			errs = append(errs, fmt.Errorf("affects %s: name its versions", pkg))
			continue
		}
		for v, sha := range aff.Versions {
			if !pkgspec.ValidVersion(v) || !sha256Re.MatchString(sha) {
				errs = append(errs, fmt.Errorf("affects %s %s: not a version and its file's SHA-256", pkg, v))
			}
		}
		if aff.Fixed != "" && !pkgspec.ValidVersion(aff.Fixed) {
			errs = append(errs, fmt.Errorf("affects %s: fixed %q isn't a version", pkg, aff.Fixed))
		}
		if _, still := aff.Versions[aff.Fixed]; still && aff.Fixed != "" {
			errs = append(errs, fmt.Errorf("affects %s: %s is both affected and the fix", pkg, aff.Fixed))
		}
	}
	return errors.Join(errs...)
}

// Affecting are the advisories that affect a package version a lab has
// installed, newest first. file is the lab's copy of the package file
// (SPEC.md §8.3.1): only the registry's own file counts, as for
// withdrawals.
func (ix *Index) Affecting(name, version string, file []byte) []*Advisory {
	if ix == nil {
		return nil
	}
	var out []*Advisory
	for _, a := range ix.Advisories {
		if aff := a.Affects[name]; aff != nil && aff.Versions[version] != "" && aff.Versions[version] == sum(file) {
			out = append(out, a)
		}
	}
	slices.SortStableFunc(out, func(x, y *Advisory) int { return strings.Compare(y.ID, x.ID) })
	return out
}

// Possibly are the advisories that say packages of an Owner's own built
// with one of builtWith may be affected (Advisory.Local), newest first.
func (ix *Index) Possibly(builtWith []string) []*Advisory {
	if ix == nil {
		return nil
	}
	var out []*Advisory
	for _, a := range ix.Advisories {
		if a.Local && a.BuiltWith != "" && slices.Contains(builtWith, a.BuiltWith) {
			out = append(out, a)
		}
	}
	slices.SortStableFunc(out, func(x, y *Advisory) int { return strings.Compare(y.ID, x.ID) })
	return out
}
