package watch

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
)

// The weekly digest lists only what needs a maintainer's decision
// (docs/REGISTRY-WATCH.md, "The digest"). The digest of 2026-11-16 had
// over 40 entries, most of which needed nothing: problems the registry's
// newest version had fixed since, and libraries in images' base systems
// that only their projects can update. Each match is now one of:
const (
	// digestOpen: the registry's newest version is still affected, or
	// it isn't known to be fixed. Listed, one entry per problem with a
	// line for each package.
	digestOpen = iota
	// digestFixed: the registry's newest version isn't affected any more
	// (its version is outside the advisory's ranges, or its image's bill
	// of materials no longer has the library version), so labs that
	// update get the fix. Collapsed.
	digestFixed
	// digestBackground: a library inside an app's image (from its bill
	// of materials: a base system's package, or one the app uses), neither
	// critical nor being used in attacks. Only the app's project can
	// update it, and the update bot will propose its new image when it
	// does, so it needs a decision only when it's serious. Collapsed. (The
	// digest of 2026-11-16, rebuilt this way, had 112 problems to decide
	// with only base systems and unfixed libraries here, 95 of them
	// libraries rated low.)
	digestBackground
)

// digestKind is which part of the digest a match goes in, and the
// registry's newest version of its package ("" when it has none).
func digestKind(f Finding, repo *Repo) (int, string) {
	newest := ""
	if repo != nil {
		if p := repo.Package(f.Package); p != nil && len(p.Versions) > 0 {
			newest = p.Versions[len(p.Versions)-1]
		}
	}
	purl := f.Purl
	if purl == "" && strings.HasPrefix(f.Source, "OSV, for ") {
		// Matches found before the watcher kept the package URL.
		purl = strings.TrimSuffix(strings.TrimPrefix(f.Source, "OSV, for "), " in its image")
	}
	if newest != "" && !slices.Contains(f.Affected, newest) {
		fixed := false
		switch {
		case purl != "":
			bom := repo.BOMs[f.Package+" "+newest]
			fixed = len(bom) > 0 && !slices.Contains(bom, purl) && carries(repo, f, purl)
		case len(f.Ranges) > 0:
			fixed = !slices.ContainsFunc(f.Ranges, func(r string) bool { return InRange(Upstream(newest), r) })
		case f.FixedUpstream != "":
			fixed = InRange(Upstream(newest), f.FixedUpstream)
		}
		if fixed {
			return digestFixed, newest
		}
	}
	if purl != "" && f.Severity != registry.SeverityCritical && !f.Exploited {
		return digestBackground, newest
	}
	return digestOpen, newest
}

// carries says whether the versions a match says are affected do carry
// its library, as far as their bills of materials show. Before 2026-11-19
// the watcher could give a match another package's library version (one
// problem in several images named whichever was read last), so such a
// match still waiting for the digest isn't taken as fixed.
func carries(repo *Repo, f Finding, purl string) bool {
	known := false
	for _, v := range f.Affected {
		bom, ok := repo.BOMs[f.Package+" "+v]
		if !ok || len(bom) == 0 {
			continue
		}
		if slices.Contains(bom, purl) {
			return true
		}
		known = true
	}
	return !known
}

// digestGroup is one problem in the digest, with a match for each package.
type digestGroup struct {
	matches []Finding
	newest  []string // the registry's newest version of each match's package
}

func (g *digestGroup) first() Finding { return g.matches[0] }

// rank sorts the most urgent first: being used in attacks, then critical,
// then the likeliest to be exploited.
func (g *digestGroup) rank() (bool, bool, float64) {
	var exploited, critical bool
	var epss float64
	for _, f := range g.matches {
		exploited = exploited || f.Exploited
		critical = critical || f.Severity == registry.SeverityCritical
		epss = max(epss, f.EPSS)
	}
	return exploited, critical, epss
}

// Digest is the weekly digest's markdown, of the weaker matches waiting
// (State.Pending), with the registry as it is now (nil: every match is
// listed as needing a decision); empty when there are none.
func Digest(pending []Finding, repo *Repo, now time.Time) string {
	if len(pending) == 0 {
		return ""
	}
	parts := [3][]*digestGroup{}
	byKey := map[string]*digestGroup{} // part and problem → its group
	seen := map[string]int{}           // problem and package → its match in its group
	for _, f := range pending {
		kind, newest := digestKind(f, repo)
		key := strconv.Itoa(kind) + " " + f.ID
		g := byKey[key]
		if g == nil {
			g = &digestGroup{}
			byKey[key] = g
			parts[kind] = append(parts[kind], g)
		}
		// The same problem and package found twice: one line, every version.
		if i, ok := seen[key+" "+f.Package]; ok && f.Package != "" {
			m := &g.matches[i]
			for _, v := range f.Affected {
				if !slices.Contains(m.Affected, v) {
					m.Affected = append(m.Affected, v)
				}
			}
			slices.SortFunc(m.Affected, pkgspec.CompareVersions)
			continue
		}
		seen[key+" "+f.Package] = len(g.matches)
		g.matches = append(g.matches, f)
		g.newest = append(g.newest, newest)
	}
	for _, gs := range parts {
		slices.SortStableFunc(gs, func(a, b *digestGroup) int {
			ae, ac, ap := a.rank()
			be, bc, bp := b.rank()
			if ae != be {
				return boolFirst(ae)
			}
			if ac != bc {
				return boolFirst(ac)
			}
			return cmp.Compare(bp, ap)
		})
	}

	var b strings.Builder
	open, fixed, background := len(parts[digestOpen]), len(parts[digestFixed]), len(parts[digestBackground])
	fmt.Fprintf(&b, "Weaker matches found by `roostack registry watch` up to %s. ", now.Format(time.DateOnly))
	if open == 0 {
		b.WriteString("None needs a decision this week. ")
	} else {
		fmt.Fprintf(&b, "%s %s a decision: write an advisory for any that matters (or ask for one to be drafted), or say why not. ",
			plural(open, "problem", "problems"), map[bool]string{true: "needs", false: "need"}[open == 1])
	}
	b.WriteString("Close this issue when done.\n")
	for _, g := range parts[digestOpen] {
		b.WriteString("\n")
		writeGroup(&b, g, repo, true)
	}
	if fixed > 0 {
		fmt.Fprintf(&b, "\n<details><summary>%s already fixed in the registry's newest version</summary>\n\n"+
			"Labs that update will get the fix, so these need nothing unless one is serious enough for an advisory "+
			"that tells labs on older versions.\n\n", plural(fixed, "problem", "problems"))
		for _, g := range parts[digestFixed] {
			writeGroup(&b, g, repo, false)
		}
		b.WriteString("\n</details>\n")
	}
	if background > 0 {
		fmt.Fprintf(&b, "\n<details><summary>%s in the background</summary>\n\n"+
			"Libraries inside apps' images, neither critical nor being used in attacks: only each app's project can update them, "+
			"and the update bot will propose its new image when it does.\n\n", plural(background, "problem", "problems"))
		for _, g := range parts[digestBackground] {
			writeGroup(&b, g, repo, false)
		}
		b.WriteString("\n</details>\n")
	}
	return b.String()
}

func boolFirst(a bool) int {
	if a {
		return -1
	}
	return 1
}

// writeGroup writes one problem: a line, then (in full) a line for each
// package it concerns, or (not full) the packages on the same line.
func writeGroup(b *strings.Builder, g *digestGroup, repo *Repo, full bool) {
	f := g.first()
	title := f.Title
	if len(g.matches) > 1 && repo != nil {
		// The same problem in several packages: the title without the
		// first package's name.
		if p := repo.Package(f.Package); p != nil {
			title = strings.TrimPrefix(title, p.Title+": ")
		}
	}
	fmt.Fprintf(b, "- **[%s](%s)** %s", f.ID, f.Link, title)
	if !full {
		var pkgs []string
		for i, m := range g.matches {
			what := nonEmpty(m.Concerns, "unknown")
			if m.Package != "" {
				what = "`" + m.Package + "`"
				if g.newest[i] != "" {
					what += " (newest `" + g.newest[i] + "`)"
				}
			}
			pkgs = append(pkgs, what)
		}
		fmt.Fprintf(b, ": %s\n", strings.Join(pkgs, ", "))
		return
	}
	b.WriteString("\n")
	for i, m := range g.matches {
		what := m.Concerns
		if m.Package != "" {
			what = "`" + m.Package + "` " + versionsLine(m.Affected, g.newest[i])
		}
		fmt.Fprintf(b, "  - %s, from %s. Suggested severity %s", nonEmpty(what, "unknown"), m.Source, m.Severity)
		if m.Exploited {
			b.WriteString(", being used in attacks")
		}
		if m.EPSS > 0 {
			fmt.Fprintf(b, ", EPSS %s%%", strconv.FormatFloat(m.EPSS*100, 'f', 1, 64))
		}
		fmt.Fprintf(b, ". Only in the digest because %s.\n", m.Weak)
	}
}

// versionsLine says which of the registry's versions are affected,
// shortly: the newest affected, and how many older ones.
func versionsLine(affected []string, newest string) string {
	if len(affected) == 0 {
		return "(versions not known)"
	}
	last := affected[len(affected)-1]
	s := "`" + last + "`"
	if last == newest {
		s += " (the newest)"
	}
	if n := len(affected) - 1; n > 0 {
		s += " and " + plural(n, "older version", "older versions")
	}
	return s
}
