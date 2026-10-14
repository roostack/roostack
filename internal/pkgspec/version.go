package pkgspec

import "strings"

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
