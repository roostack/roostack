package pkgspec

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// About holds facts about the project a package runs, reviewed with the
// package and shown the same way in the lab's catalog and on the website
// (SPEC.md §8.3.6). Every field is optional for a package of your own; the
// package policy requires most of them for registry packages.
type About struct {
	// Maker says who makes the app, in a few words ("a volunteer
	// community", "Daniel García and volunteer maintainers").
	Maker string `yaml:"maker"`
	// Website is the project's own site; Source is where its code is
	// published. Both https.
	Website string `yaml:"website"`
	Source  string `yaml:"source"`
	// Licence is the app's licence as an SPDX expression (AGPL-3.0-only,
	// MIT OR Apache-2.0), or LicenceClosed when its code isn't published.
	Licence string `yaml:"licence"`
	// Support is the project's own page for donating or sponsoring.
	Support string `yaml:"support"`
	// Icon is the project's logo: a PNG file beside the package file,
	// square and at least IconMinSize pixels (CheckIcon).
	Icon string `yaml:"icon"`
	// Categories are one to three of Categories' keys, for the catalog's
	// filters; Keywords are anything else people might search for.
	Categories []string `yaml:"categories"`
	Keywords   []string `yaml:"keywords"`
}

// LicenceClosed is the licence of an app whose code isn't published
// (e.g. Plex): it has a website instead of a source.
const LicenceClosed = "closed"

// Category is one of the catalog's filters.
type Category struct {
	Key   string
	Title string
}

// Categories is the fixed list a package's categories come from, agreed
// with the Owner on 2026-10-14. Adding one is a Roostack change, so the
// catalog's filters stay few and tidy; keywords cover the rest.
var Categories = []Category{
	{"photos", "Photos and videos"},
	{"media", "Films, TV and music"},
	{"documents", "Documents and notes"},
	{"productivity", "Tasks and planning"},
	{"home", "Smart home and cameras"},
	{"passwords", "Passwords and security"},
	{"development", "Developer tools"},
	{"publishing", "Websites and blogs"},
	{"network", "Network"},
}

// CategoryTitle returns a category's title, or "" for an unknown key.
func CategoryTitle(key string) string {
	for _, c := range Categories {
		if c.Key == key {
			return c.Title
		}
	}
	return ""
}

// MaxCategories and MaxKeywords keep a package's filters and search terms
// to the point.
const (
	MaxCategories = 3
	MaxKeywords   = 10
)

// Requires says what a package needs to run (SPEC.md §8.3.5).
type Requires struct {
	// Roostack is the oldest Roostack release that can run the package:
	// "0.6" or "0.6.1".
	Roostack string `yaml:"roostack"`
}

// UpgradeNote is something to know before updating to a version, shown
// when a service updates from an older one (SPEC.md §8.3.5). A package
// keeps the notes it still needs from earlier versions, so updating past
// several versions at once shows all of theirs.
type UpgradeNote struct {
	// Version is the package version the note arrived in.
	Version string `yaml:"version"`
	Note    string `yaml:"note"`
}

var (
	// An SPDX licence identifier or LicenseRef-, with an optional + (or
	// later); WITH takes an exception identifier of the same form.
	spdxIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*\+?$`)
	// A plain file name, so the icon can only be beside the package.
	iconRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.png$`)
	// A Roostack release, as requires.roostack names it.
	roostackVersionRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\.(0|[1-9][0-9]*))?$`)
	// The release a running Roostack's version starts with: v0.6.0,
	// 0.6.0-3-gabc1234 (git describe after a tag), 0.6.
	runningVersionRe = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)(?:\.([0-9]+))?`)
	// A memory size: a number and a decimal unit (1 GB = 1000 MB, like
	// the rest of Roostack: the units package).
	memoryRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?) ?(MB|GB)$`)
)

// Memory bounds: anything outside them is a typo.
const (
	minMemory = 1_000_000         // 1 MB
	maxMemory = 1_000_000_000_000 // 1000 GB
)

// ParseMemory reads a memory size such as "300 MB" or "1.5 GB" (decimal:
// 1 GB = 1000 MB) and returns it in bytes.
func ParseMemory(s string) (int64, error) {
	m := memoryRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q must be a number and MB or GB, e.g. 300 MB or 1.5 GB", s)
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("%q: %w", s, err)
	}
	unit := 1e6
	if m[2] == "GB" {
		unit = 1e9
	}
	b := int64(n * unit)
	if b < minMemory || b > maxMemory {
		return 0, fmt.Errorf("%q must be between 1 MB and 1000 GB", s)
	}
	return b, nil
}

// Expected memory of Roostack's own databases when the package doesn't
// say: PostgreSQL and Valkey as a small service uses them (measured idle
// on a dev VM at about 10 MB each on 2026-10-14, with room for work).
const (
	DefaultPostgresMemory = 200_000_000
	DefaultRedisMemory    = 50_000_000
)

// ExpectedMemory returns the container's expected memory in bytes, or 0
// if the package doesn't say. The install summary adds up those of the
// containers that would run (SPEC.md §8.3.4).
func (c *Container) ExpectedMemory() int64 {
	b, _ := ParseMemory(c.Memory)
	return b
}

// ExpectedMemory returns the database's expected memory in bytes: the
// package's figure, or Roostack's default for its type. SQLite runs inside
// the app, so it has none of its own.
func (d *Database) ExpectedMemory() int64 {
	if b, err := ParseMemory(d.Memory); err == nil {
		return b
	}
	switch d.Type {
	case DBPostgres:
		return DefaultPostgresMemory
	case DBRedis:
		return DefaultRedisMemory
	}
	return 0
}

// RunsOn says whether a Roostack of this version can run the package
// (requires.roostack, SPEC.md §8.3.5); a lab offers only the versions it
// can, and says "Needs a newer Roostack" for the rest.
//
// The running version is what the build was given (git describe): the
// release it starts with is compared number by number, a missing patch
// counting as 0, and anything after it is ignored, so 0.6.0-3-gabc1234 (a
// build after 0.6.0) and 0.6.0-rc.1 both count as 0.6.0. A version that
// doesn't start with a release ("dev", a bare commit, before the first
// tag) is a development build and runs everything.
func (p *Package) RunsOn(roostack string) bool {
	need := roostackVersionRe.FindStringSubmatch(p.Requires.Roostack)
	if need == nil {
		return true // nothing required (Validate refuses anything else)
	}
	have := runningVersionRe.FindStringSubmatch(roostack)
	if have == nil {
		return true
	}
	return compareRelease(have[1:4], []string{need[1], need[2], need[4]}) >= 0
}

// compareRelease compares two [major, minor, patch] lists of decimal
// numbers, an empty part counting as 0.
func compareRelease(a, b []string) int {
	for i := range 3 {
		x, _ := strconv.Atoi(orZero(a[i]))
		y, _ := strconv.Atoi(orZero(b[i]))
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

// Icon limits (agreed with the Owner on 2026-10-14): square, sharp on a
// high-density screen, and small enough to bundle every package's.
const (
	IconMinSize  = 256     // pixels a side
	IconMaxBytes = 100_000 // 100 KB
)

// CheckIcon checks the package's icon, a PNG in dir (the package file's
// folder): it's there, a PNG, square, at least IconMinSize pixels a side
// and at most IconMaxBytes. A package without an icon passes.
func (p *Package) CheckIcon(dir string) error {
	if p.About.Icon == "" {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(dir, p.About.Icon))
	if err != nil {
		return fmt.Errorf("about.icon: %w", err)
	}
	if err := CheckIcon(data); err != nil {
		return fmt.Errorf("about.icon: %s: %w", p.About.Icon, err)
	}
	return nil
}

// CheckIcon checks an icon's file: a PNG, square, at least IconMinSize
// pixels a side and at most IconMaxBytes. Only its header is decoded.
func CheckIcon(data []byte) error {
	if len(data) > IconMaxBytes {
		return fmt.Errorf("%d bytes is too large (at most %d KB)", len(data), IconMaxBytes/1000)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return errors.New("not a PNG image")
	}
	if cfg.Width != cfg.Height {
		return fmt.Errorf("%d×%d isn't square", cfg.Width, cfg.Height)
	}
	if cfg.Width < IconMinSize {
		return fmt.Errorf("%d×%d is too small (at least %d×%d)", cfg.Width, cfg.Height, IconMinSize, IconMinSize)
	}
	return nil
}

// validateAbout checks the form of the about:, requires: and
// upgrade_notes: fields given; which must be given is the package
// policy's (Policy).
func (p *Package) validateAbout(v *validator) {
	a := p.About
	if len(a.Maker) > 100 {
		v.addf("about.maker: at most 100 characters; say who makes it in a few words")
	}
	for _, f := range []struct{ at, val string }{
		{"about.website", a.Website}, {"about.source", a.Source}, {"about.support", a.Support},
	} {
		if f.val != "" && !validHTTPS(f.val) {
			v.addf("%s: %q must be an https:// address", f.at, f.val)
		}
	}
	if a.Licence != "" && a.Licence != LicenceClosed {
		if err := checkSPDX(a.Licence); err != nil {
			v.addf("about.licence: %q isn't an SPDX licence expression (%v); use the project's SPDX identifier, e.g. AGPL-3.0-only, or %q if its code isn't published", a.Licence, err, LicenceClosed)
		}
	}
	if a.Licence == LicenceClosed && a.Source != "" {
		v.addf("about.source: a closed-source app has no published source; remove it or give the licence")
	}
	if a.Icon != "" && !iconRe.MatchString(a.Icon) {
		v.addf("about.icon: %q must be the name of a .png file beside the package file", a.Icon)
	}
	if len(a.Categories) > MaxCategories {
		v.addf("about.categories: at most %d", MaxCategories)
	}
	keys := make([]string, len(Categories))
	for i, c := range Categories {
		keys[i] = c.Key
	}
	for i, c := range a.Categories {
		if CategoryTitle(c) == "" {
			v.addf("about.categories[%d]: %q must be one of %s", i, c, strings.Join(keys, ", "))
		}
		if slices.Index(a.Categories, c) != i {
			v.addf("about.categories[%d]: %q is listed twice", i, c)
		}
	}
	if len(a.Keywords) > MaxKeywords {
		v.addf("about.keywords: at most %d", MaxKeywords)
	}
	seen := map[string]bool{}
	for i, k := range a.Keywords {
		if k == "" || k != strings.TrimSpace(k) || len(k) > 40 {
			v.addf("about.keywords[%d]: %q must be a word or short phrase (up to 40 characters, no spaces around it)", i, k)
		}
		if seen[strings.ToLower(k)] {
			v.addf("about.keywords[%d]: %q is listed twice", i, k)
		}
		seen[strings.ToLower(k)] = true
	}

	if r := p.Requires.Roostack; r != "" && !roostackVersionRe.MatchString(r) {
		v.addf("requires.roostack: %q must be a Roostack release such as \"0.6\" or \"0.6.1\"", r)
	}

	notes := map[string]bool{}
	for i, n := range p.UpgradeNotes {
		at := fmt.Sprintf("upgrade_notes[%d]", i)
		if !ValidVersion(n.Version) {
			v.addf("%s.version: %q must be the package version the note arrived in", at, n.Version)
		} else if notes[n.Version] {
			v.addf("%s.version: %q has two notes; put them in one", at, n.Version)
		}
		notes[n.Version] = true
		if strings.TrimSpace(n.Note) == "" {
			v.addf("%s.note: required", at)
		}
	}
}

// validHTTPS says whether s is an absolute https:// address.
func validHTTPS(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}

// checkSPDX checks the form of an SPDX licence expression: identifiers
// joined by AND and OR, with parentheses, and WITH an exception. Whether
// each identifier is on the SPDX list is the registry CI's check.
func checkSPDX(expr string) error {
	toks := strings.Fields(strings.NewReplacer("(", " ( ", ")", " ) ").Replace(expr))
	pos := 0
	var parseExpr, parseTerm func() error
	parseTerm = func() error {
		if pos >= len(toks) {
			return errors.New("it ends too soon")
		}
		t := toks[pos]
		pos++
		if t == "(" {
			if err := parseExpr(); err != nil {
				return err
			}
			if pos >= len(toks) || toks[pos] != ")" {
				return errors.New("a ( isn't closed")
			}
			pos++
			return nil
		}
		if !isSPDXID(t) {
			return fmt.Errorf("%q isn't an identifier", t)
		}
		if pos < len(toks) && toks[pos] == "WITH" {
			pos++
			if pos >= len(toks) || !isSPDXID(toks[pos]) {
				return errors.New("WITH needs an exception identifier")
			}
			pos++
		}
		return nil
	}
	parseExpr = func() error {
		if err := parseTerm(); err != nil {
			return err
		}
		for pos < len(toks) && (toks[pos] == "AND" || toks[pos] == "OR") {
			pos++
			if err := parseTerm(); err != nil {
				return err
			}
		}
		return nil
	}
	if err := parseExpr(); err != nil {
		return err
	}
	if pos < len(toks) {
		return fmt.Errorf("unexpected %q", toks[pos])
	}
	return nil
}

func isSPDXID(t string) bool {
	switch t {
	case "AND", "OR", "WITH", "(", ")":
		return false
	}
	return spdxIDRe.MatchString(t)
}

// deprecatedLicences are SPDX identifiers that don't say whether later
// versions of the licence apply: the project's licence notice does.
var deprecatedLicences = []string{
	"GPL-1.0", "GPL-2.0", "GPL-3.0", "LGPL-2.0", "LGPL-2.1", "LGPL-3.0",
	"AGPL-1.0", "AGPL-3.0", "GFDL-1.1", "GFDL-1.2", "GFDL-1.3",
}

// ambiguousLicences returns the identifiers in an SPDX expression that
// SPDX has deprecated for one ending -only or -or-later.
func ambiguousLicences(expr string) []string {
	var found []string
	for t := range strings.FieldsSeq(strings.NewReplacer("(", " ", ")", " ").Replace(expr)) {
		if slices.Contains(deprecatedLicences, strings.TrimSuffix(t, "+")) {
			found = append(found, t)
		}
	}
	return found
}
