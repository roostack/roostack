package watch

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
)

// InRange says whether an app's version is in a range as GitHub's
// advisories write them: comparisons separated by commas, all of which
// hold (">= 1.2.0, < 1.2.5", "<= 2.0", "= 1.0.1", "1.0.1"). A leading v
// is ignored on either side; versions compare in natural order
// (pkgspec.CompareVersions). An empty or unreadable range holds nothing.
func InRange(version, ranges string) bool {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	any := false
	for _, c := range strings.Split(ranges, ",") {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		op := ""
		for _, o := range []string{">=", "<=", ">", "<", "="} {
			if strings.HasPrefix(c, o) {
				op = o
				break
			}
		}
		v := strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(c, op)), "v")
		if v == "" {
			return false
		}
		cmp := pkgspec.CompareVersions(version, v)
		ok := map[string]bool{">=": cmp >= 0, "<=": cmp <= 0, ">": cmp > 0, "<": cmp < 0, "=": cmp == 0, "": cmp == 0}[op]
		if !ok {
			return false
		}
		any = true
	}
	return any
}

// PatchedRange is a GitHub advisory's patched versions as a range: a bare
// version ("1.37.0") is that one or newer; one with a comparison ("> 7.11.1")
// is as written.
func PatchedRange(patched string) string {
	patched = strings.TrimSpace(patched)
	if patched == "" || strings.ContainsAny(patched[:1], "<>=") {
		return patched
	}
	return ">= " + patched
}

var cveInText = regexp.MustCompile(`CVE-[0-9]{4}-[0-9]{4,}`)

// CVEsIn are the CVE IDs a text names, each once.
func CVEsIn(text string) []string {
	var out []string
	for _, c := range cveInText.FindAllString(text, -1) {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// CVSS is what the watcher reads from a CVSS 3 or 4 vector: whether
// anyone on the network can use it without an account or anyone's help,
// and whether it reaches data or the system (confidentiality or
// integrity high).
type CVSS struct {
	Network, NoPrivileges, NoUserAction, High bool
	Score                                     float64
}

// ParseCVSS reads a vector ("CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
// and its score, if there's one.
func ParseCVSS(vector string, score float64) CVSS {
	c := CVSS{Score: score}
	for _, part := range strings.Split(vector, "/") {
		k, v, _ := strings.Cut(part, ":")
		switch k {
		case "AV":
			c.Network = v == "N"
		case "PR":
			c.NoPrivileges = v == "N"
		case "UI":
			c.NoUserAction = v == "N"
		case "C", "I", "VC", "VI":
			c.High = c.High || v == "H"
		}
	}
	return c
}

// Signals are what the watcher knows about a problem, for its severity in
// Roostack's own meaning and how urgent it is.
type Signals struct {
	CVSS CVSS
	// Severity is the source's own (critical, high, medium, low).
	Severity string
	// Exploited: on CISA's list of exploited vulnerabilities, or
	// CrowdSec has a virtual-patch rule for it (a public exploit).
	Exploited bool
	// EPSS is the chance it's exploited in the next 30 days (0 to 1).
	EPSS float64
}

// SuggestedSeverity is Roostack's severity for a problem (registry
// Severity*), with why, for a maintainer to confirm: critical only when
// anyone on the internet can use it without signing in or anyone's help
// and it reaches data or the system; high for anything the source rates
// high or critical, or that's being used in attacks; low otherwise.
func SuggestedSeverity(s Signals) (string, string) {
	c := s.CVSS
	switch {
	case c.Network && c.NoPrivileges && c.NoUserAction && c.High:
		return registry.SeverityCritical, "its CVSS vector says anyone on the network can use it, without an account or anyone's help, " +
			"to reach data or the system" + score(c.Score)
	case s.Severity == "critical" || s.Severity == "high" || s.Exploited:
		why := "the source rates it " + s.Severity
		if s.Exploited {
			why = "it's being used in attacks"
		}
		return registry.SeverityHigh, why + ", but nothing says anyone on the internet can use it without signing in" + score(c.Score)
	default:
		return registry.SeverityLow, "the source rates it " + nonEmpty(s.Severity, "unrated") + score(c.Score)
	}
}

func score(s float64) string {
	if s == 0 {
		return ""
	}
	return " (CVSS " + strconv.FormatFloat(s, 'f', 1, 64) + ")"
}

func nonEmpty(s, or string) string {
	if s == "" {
		return or
	}
	return s
}
