package pkgspec

import (
	"slices"
	"strconv"
	"strings"
)

// CompareVersions orders two package versions (<upstream>-roostack.<n>,
// SPEC.md §8.3.5), returning -1, 0 or 1. Runs of digits compare as
// numbers and everything else as text, so 1.10.0 is newer than 1.9.0 and
// revision 10 newer than revision 9. It's for picking a catalog's newest
// version; updates (§7) read the upstream part as semantic versioning.
func CompareVersions(a, b string) int {
	for a != "" && b != "" {
		x, restA := nextRun(a)
		y, restB := nextRun(b)
		if c := compareRun(x, y); c != 0 {
			return c
		}
		a, b = restA, restB
	}
	switch {
	case a == b:
		return 0
	case a == "":
		return -1
	default:
		return 1
	}
}

// nextRun splits off s's first run of digits, or of anything else.
func nextRun(s string) (run, rest string) {
	digits := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == digits {
		i++
	}
	return s[:i], s[i:]
}

// compareRun compares two runs: numbers by value (without parsing them,
// so any length works), a number before text, and text as text.
func compareRun(x, y string) int {
	dx, dy := isDigit(x[0]), isDigit(y[0])
	switch {
	case dx && dy:
		x, y = strings.TrimLeft(x, "0"), strings.TrimLeft(y, "0")
		if len(x) != len(y) {
			if len(x) < len(y) {
				return -1
			}
			return 1
		}
	case dx:
		return -1
	case dy:
		return 1
	}
	return strings.Compare(x, y)
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// Kinds of update between two package versions (SPEC.md §7, §8.3.5).
const (
	// UpdateRevision is a new definition of the same app version.
	UpdateRevision = "revision"
	UpdatePatch    = "patch"
	UpdateMinor    = "minor"
	UpdateMajor    = "major"
	// UpdateOther is an app version that doesn't look like semantic
	// versioning (or goes backwards), so no policy but "all" takes it.
	UpdateOther = "other"
)

// revisionSep separates a package version's upstream part from its
// revision (SPEC.md §8.1).
const revisionSep = "-roostack."

// Upstream is the app's own version in a package version: what comes
// before "-roostack.<revision>" (all of it if there's no revision).
func Upstream(version string) string {
	if i := strings.LastIndex(version, revisionSep); i >= 0 {
		return version[:i]
	}
	return version
}

// UpdateKind says what kind of update going from one package version to a
// newer one is, from the upstream parts: the same is a revision; one, two
// or three numbers (an optional leading v) compare as major.minor.patch,
// missing ones as 0; anything else is UpdateOther.
func UpdateKind(from, to string) string {
	a, b := Upstream(from), Upstream(to)
	if a == b {
		return UpdateRevision
	}
	x, ok1 := semver(a)
	y, ok2 := semver(b)
	switch {
	case !ok1 || !ok2:
		return UpdateOther
	case y[0] > x[0]:
		return UpdateMajor
	case y[0] < x[0]:
		return UpdateOther
	case y[1] > x[1]:
		return UpdateMinor
	case y[1] < x[1]:
		return UpdateOther
	case y[2] > x[2]:
		return UpdatePatch
	}
	return UpdateOther
}

// semver reads "1", "1.2" or "1.2.3" (optionally "v1.2.3").
func semver(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) > 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || (len(p) > 1 && p[0] == '0') {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// NotesSince are the package's upgrade notes for versions newer than
// from, oldest first: what to read before updating from it to this
// package's version (SPEC.md §8.3.5).
func (p *Package) NotesSince(from string) []UpgradeNote {
	var out []UpgradeNote
	for _, n := range p.UpgradeNotes {
		if CompareVersions(n.Version, from) > 0 && CompareVersions(n.Version, p.Version) <= 0 {
			out = append(out, n)
		}
	}
	slices.SortStableFunc(out, func(a, b UpgradeNote) int { return CompareVersions(a.Version, b.Version) })
	return out
}
