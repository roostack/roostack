// Package watch is the service registry's watcher (docs/RED-BUTTON-PLAN.md
// §10.1; SPEC.md §6.16): run hourly by the registry's CI as `roostack
// registry watch`, it reads free sources of security problems, matches
// them to the registry's packages, and drafts advisories for a maintainer
// to approve. It never publishes anything: a draft is a file the CI opens a
// pull request for, and a weaker match waits for the weekly digest.
package watch

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
)

// Finding is a security problem the watcher found that may concern the
// registry.
type Finding struct {
	// ID is what it's tracked by (a GitHub advisory, OSV or CVE ID), and
	// Aliases its other IDs.
	ID      string   `json:"id"`
	Aliases []string `json:"aliases,omitempty"`
	// Source says where it came from, for people.
	Source string `json:"source"`
	Title  string `json:"title"`
	Link   string `json:"link"`
	// Package is the registry's package it concerns ("" for Roostack's
	// own parts, or a match by name only), Affected the registry's
	// versions of it affected (none: not known exactly), and Fixed the
	// registry's first version that fixes it, if there's one yet;
	// FixedUpstream is the fixed version the source names.
	Package       string   `json:"package,omitempty"`
	Affected      []string `json:"affected,omitempty"`
	Fixed         string   `json:"fixed,omitempty"`
	FixedUpstream string   `json:"fixed_upstream,omitempty"`
	// Concerns says what it may concern when it isn't a package version
	// exactly (a built-with name, one of Roostack's parts).
	Concerns string `json:"concerns,omitempty"`
	Signals
	// Severity is the one the watcher suggests (Roostack's own), and Why
	// says why.
	Severity string `json:"severity"`
	Why      string `json:"why"`
	// Weak says why it's only for the digest ("": a strong match, drafted).
	Weak string `json:"weak,omitempty"`
	// Found is when the watcher found it.
	Found time.Time `json:"found"`
}

// CVEs are its CVE IDs.
func (f Finding) CVEs() []string {
	var out []string
	for _, id := range append([]string{f.ID}, f.Aliases...) {
		if cveRe.MatchString(id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// Urgent: critical and being used in attacks, so the maintainers' phones
// hear of it (docs/RED-BUTTON-PLAN.md §10.1.4).
func (f Finding) Urgent() bool { return f.Severity == registry.SeverityCritical && f.Exploited }

var cveRe = regexp.MustCompile(`^CVE-[0-9]{4}-[0-9]{4,}$`)

// State is what the watcher keeps between runs, in the registry
// repository (StateFile), so nothing is drafted twice.
type State struct {
	LastRun time.Time `json:"last_run"`
	// Seen are the problems handled already, by ID, each with what
	// became of it ("drafted 2026-0007", "digest", "doesn't affect it").
	Seen map[string]string `json:"seen"`
	// KEV and VPatches are CISA's list and CrowdSec's rules as last read,
	// to tell what's new.
	KEV      []string `json:"kev"`
	VPatches []string `json:"vpatches"`
	// Pending are weaker matches for the next weekly digest.
	Pending []Finding `json:"pending"`
}

// StateFile is the watcher's state in the registry repository.
const StateFile = "watch/state.json"

// LoadState reads the state, or an empty one the first time.
func LoadState(path string) (*State, error) {
	st := &State{Seen: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if st.Seen == nil {
		st.Seen = map[string]string{}
	}
	return st, nil
}

// Save writes the state.
func (st *State) Save(path string) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func dirOf(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i]
	}
	return "."
}

// seen says whether any of ids was handled already.
func (st *State) seen(ids ...string) bool {
	for _, id := range ids {
		if _, ok := st.Seen[id]; ok && id != "" {
			return true
		}
	}
	return false
}

// Part is one of Roostack's own parts, whose problems Roostack releases
// fix: an advisory about it names the affected Roostack releases
// (affects: roostack), the closed level's case (SPEC.md §6.16).
type Part struct{ Repo, Title string }

// Parts are Roostack's own parts the watcher reads advisories of.
var Parts = []Part{
	{"caddyserver/caddy", "Caddy"},
	{"crowdsecurity/crowdsec", "CrowdSec"},
	{"restic/restic", "restic"},
	{"roostack/roostack", "Roostack"},
}

// Watcher reads the sources and drafts advisories for one registry
// repository.
type Watcher struct {
	Repo  *Repo
	Feeds Feeds
	State *State
	// Since is how far back the first run looks (later runs look back to
	// the run before); a problem published before isn't drafted.
	Since time.Duration
	// MaxOSV caps how many OSV records one run reads.
	MaxOSV int
	Now    func() time.Time
	Log    *slog.Logger
}

// Draft is a strong match's advisory, written into the repository, with
// what the registry's CI needs to open its pull request.
type Draft struct {
	Finding
	Advisory string `json:"advisory"`
	File     string `json:"file"`
	Branch   string `json:"branch"`
	PRTitle  string `json:"pr_title"`
	PRBody   string `json:"pr_body"`
	Urgent   bool   `json:"urgent"`
}

// Result is what a run found.
type Result struct {
	Drafts []Draft   `json:"drafts"`
	Weak   []Finding `json:"weak"`
	Notes  []string  `json:"notes"`
	Ran    time.Time `json:"ran"`
}

// Run reads every source once, drafts each strong match into the
// repository, adds weaker ones to the state for the digest, and updates
// the state (not saved: the caller saves it).
func (w *Watcher) Run(ctx context.Context) (*Result, error) {
	now := w.Now()
	res := &Result{Ran: now}
	cutoff := w.State.LastRun.Add(-time.Hour)
	if w.State.LastRun.IsZero() {
		cutoff = now.Add(-cmp.Or(w.Since, 30*24*time.Hour))
	}
	note := func(format string, args ...any) {
		res.Notes = append(res.Notes, fmt.Sprintf(format, args...))
	}

	kev, err := w.Feeds.KEV(ctx)
	if err != nil {
		note("CISA's list of exploited vulnerabilities couldn't be read: %v", err)
	}
	vpatches, err := w.Feeds.VPatches(ctx)
	if err != nil {
		note("CrowdSec's virtual-patch rules couldn't be read: %v", err)
	}
	patched := map[string]string{} // CVE → the rule for it
	for name, v := range vpatches {
		for _, c := range v.CVEs {
			patched[c] = name
		}
	}

	var found []Finding
	// Each package's own advisories, on GitHub.
	for _, p := range w.Repo.Packages {
		if p.Repo == "" {
			note("%s: its source (%s) isn't on GitHub, so its own advisories aren't read", p.Title, nonEmpty(p.Source, "none given"))
			continue
		}
		advs, err := w.Feeds.RepoAdvisories(ctx, p.Repo)
		if err != nil {
			note("%s: %s's advisories couldn't be read: %v", p.Title, p.Repo, err)
			continue
		}
		for _, a := range advs {
			if f, ok := w.fromGHSA(a, &p, cutoff); ok {
				found = append(found, f)
			}
		}
	}
	// Roostack's own parts: a person decides which releases they affect.
	for _, part := range Parts {
		advs, err := w.Feeds.RepoAdvisories(ctx, part.Repo)
		if err != nil {
			note("%s: %s's advisories couldn't be read: %v", part.Title, part.Repo, err)
			continue
		}
		for _, a := range advs {
			if f, ok := w.fromGHSA(a, nil, cutoff); ok {
				f.Concerns = part.Title + ", one of Roostack's own parts"
				f.Weak = "it's about " + part.Title + ": if a Roostack release is affected, write the advisory with affects: roostack"
				found = append(found, f)
			}
		}
	}
	// Exact library versions in the images' bills of materials, from OSV.
	found = append(found, w.fromBOMs(ctx, cutoff, note)...)
	// What's new on CISA's list and in CrowdSec's rules, by name.
	found = append(found, w.newOnLists(kev, vpatches)...)

	// Signals, then ranking.
	var cves []string
	for _, f := range found {
		cves = append(cves, f.CVEs()...)
	}
	slices.Sort(cves)
	epss, err := w.Feeds.EPSS(ctx, slices.Compact(cves))
	if err != nil {
		note("EPSS couldn't be read: %v", err)
	}
	for i := range found {
		f := &found[i]
		for _, c := range f.CVEs() {
			if _, ok := kev[c]; ok {
				f.Exploited = true
				f.Why = strings.TrimSpace(f.Why + " " + c + " is on CISA's list of exploited vulnerabilities.")
			} else if rule, ok := patched[c]; ok {
				f.Exploited = true
				f.Why = strings.TrimSpace(f.Why + " CrowdSec has a virtual-patch rule for " + c + " (" + rule + "), so an exploit is public.")
			}
			f.EPSS = max(f.EPSS, epss[c])
		}
		sev, why := SuggestedSeverity(f.Signals)
		f.Severity, f.Why = sev, strings.TrimSpace(why+". "+f.Why)
		if f.Weak == "" && len(f.Affected) == 0 {
			f.Weak = "no registry version is known to be affected"
		}
		if f.Weak == "" && f.Severity != registry.SeverityCritical && !f.Exploited {
			f.Weak = "it isn't critical, and nothing says it's being used in attacks"
		}
	}
	slices.SortStableFunc(found, func(a, b Finding) int { return cmp.Compare(b.EPSS, a.EPSS) })

	var taken []string
	for _, f := range found {
		if f.Weak != "" {
			res.Weak = append(res.Weak, f)
			w.State.Pending = append(w.State.Pending, f)
			w.mark(f, "digest")
			continue
		}
		d, err := w.draft(f, now, taken)
		if err != nil {
			return res, err
		}
		taken = append(taken, d.Advisory)
		res.Drafts = append(res.Drafts, d)
		w.mark(f, "drafted "+d.Advisory)
	}

	if kev != nil {
		w.State.KEV = slices.Sorted(mapKeys(kev))
	}
	if vpatches != nil {
		w.State.VPatches = slices.Sorted(mapKeys(vpatches))
	}
	w.State.LastRun = now
	return res, nil
}

// mark records a finding's IDs as handled.
func (w *Watcher) mark(f Finding, what string) {
	for _, id := range append([]string{f.ID}, f.Aliases...) {
		if id != "" {
			w.State.Seen[id] = what
		}
	}
}

// fromGHSA is a GitHub advisory as a finding, for a package of the
// registry (p) or one of Roostack's parts (nil); ok is false when it's
// old, withdrawn, handled already, or affects none of the registry's
// versions.
func (w *Watcher) fromGHSA(a GHSA, p *Package, cutoff time.Time) (Finding, bool) {
	ids := []string{a.ID, a.CVE}
	if !a.Withdrawn.IsZero() || a.Updated.Before(cutoff) || w.State.seen(ids...) || w.Repo.Published(ids...) {
		return Finding{}, false
	}
	f := Finding{ID: a.ID, Link: a.URL, Found: w.Now(),
		Signals: Signals{CVSS: ParseCVSS(a.CVSSVector, a.CVSSScore), Severity: a.Severity}}
	// Its CVE, and any its text names (an advisory written before its
	// CVE was given, or about others'), for CISA's list and CrowdSec's
	// rules.
	for _, c := range append([]string{a.CVE}, CVEsIn(a.Summary+" "+a.Description)...) {
		if c != "" && !slices.Contains(f.Aliases, c) {
			f.Aliases = append(f.Aliases, c)
		}
	}
	if w.State.seen(f.Aliases...) || w.Repo.Published(f.Aliases...) {
		return Finding{}, false
	}
	title := strings.TrimSpace(a.Summary)
	if p == nil {
		f.Source, f.Title = "GitHub's advisories", title
		return f, true
	}
	f.Source, f.Package = "GitHub's advisories for "+p.Repo, p.Name
	if !strings.Contains(strings.ToLower(title), strings.ToLower(p.Title)) {
		title = p.Title + ": " + title
	}
	f.Title = title
	ranged := false
	for _, v := range a.Vulnerables {
		if v.Range == "" {
			continue
		}
		ranged = true
		for _, ver := range p.Versions {
			if InRange(Upstream(ver), v.Range) && !slices.Contains(f.Affected, ver) {
				f.Affected = append(f.Affected, ver)
			}
		}
		if v.Patched != "" {
			f.FixedUpstream = PatchedRange(v.Patched)
		}
	}
	switch {
	case !ranged:
		f.Concerns, f.Weak = p.Title, "the advisory names no versions"
	case len(f.Affected) == 0:
		w.State.Seen[a.ID] = "doesn't affect the registry's versions"
		return Finding{}, false
	}
	slices.SortFunc(f.Affected, pkgspec.CompareVersions)
	if f.FixedUpstream != "" {
		for _, ver := range p.Versions {
			if !slices.Contains(f.Affected, ver) && InRange(Upstream(ver), f.FixedUpstream) {
				f.Fixed = ver
				break
			}
		}
	}
	return f, true
}

// fromBOMs asks OSV about every library version in the images' bills of
// materials: an exact match for each package version carrying one.
func (w *Watcher) fromBOMs(ctx context.Context, cutoff time.Time, note func(string, ...any)) []Finding {
	if len(w.Repo.BOMs) == 0 {
		note("No bills of materials yet (%s/), so library versions inside the images aren't checked", BOMDir)
		return nil
	}
	var purls []string
	for _, ps := range w.Repo.BOMs {
		purls = append(purls, ps...)
	}
	slices.Sort(purls)
	purls = slices.Compact(purls)
	hits, err := w.Feeds.OSVQuery(ctx, purls)
	if err != nil {
		note("OSV couldn't be asked: %v", err)
		return nil
	}
	affects := map[string]map[string][]string{} // vuln → package → versions
	carried := map[string]string{}              // vuln → a package URL carrying it
	for key, ps := range w.Repo.BOMs {
		pkg, ver, _ := strings.Cut(key, " ")
		for _, purl := range ps {
			for _, id := range hits[purl] {
				if affects[id] == nil {
					affects[id] = map[string][]string{}
				}
				affects[id][pkg] = append(affects[id][pkg], ver)
				carried[id] = purl
			}
		}
	}
	var out []Finding
	read := 0
	for _, id := range slices.Sorted(mapKeys(affects)) {
		if w.State.seen(id) || w.Repo.Published(id) {
			continue
		}
		if read >= cmp.Or(w.MaxOSV, 200) {
			note("More OSV records than one run reads (%d): the rest wait for the next run", read)
			break
		}
		read++
		v, err := w.Feeds.OSVVuln(ctx, id)
		if err != nil {
			note("OSV's %s couldn't be read: %v", id, err)
			continue
		}
		if !v.Withdrawn.IsZero() || w.State.seen(v.Aliases...) || w.Repo.Published(v.Aliases...) {
			continue
		}
		sig := Signals{Severity: strings.ToLower(v.DatabaseSpecific.Severity)}
		for _, s := range v.Severity {
			if strings.HasPrefix(s.Type, "CVSS") {
				sig.CVSS = ParseCVSS(s.Score, 0)
			}
		}
		for _, pkg := range slices.Sorted(mapKeys(affects[id])) {
			p := w.Repo.Package(pkg)
			title := pkg
			if p != nil {
				title = p.Title
			}
			f := Finding{ID: id, Aliases: v.Aliases, Source: "OSV, for " + carried[id] + " in its image", Package: pkg,
				Title: title + ": " + nonEmpty(strings.TrimSpace(v.Summary), id) + " (in " + libName(carried[id]) + ")",
				Link:  "https://osv.dev/vulnerability/" + id, Signals: sig, Found: w.Now()}
			f.Affected = affects[id][pkg]
			slices.SortFunc(f.Affected, pkgspec.CompareVersions)
			f.Affected = slices.Compact(f.Affected)
			for _, aff := range v.Affected {
				for _, r := range aff.Ranges {
					for _, e := range r.Events {
						if fx := e["fixed"]; fx != "" {
							lib, _, _ := strings.Cut(libName(carried[id]), " ")
							f.FixedUpstream = lib + " " + fx
						}
					}
				}
			}
			if !v.Published.IsZero() && v.Published.Before(cutoff) {
				f.Weak = "it was published before the watcher's window, so it's long known"
			}
			out = append(out, f)
		}
	}
	return out
}

// libName is a package URL's library, without its type or version
// (log4j-core 2.14.1 → "log4j-core 2.14.1").
func libName(purl string) string {
	s := strings.TrimPrefix(purl, "pkg:")
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	return strings.Replace(s, "@", " ", 1)
}

// newOnLists are what's new on CISA's list and among CrowdSec's
// virtual-patch rules whose product is something the registry has, by
// name: weak matches, for the digest. The first run only learns the lists.
func (w *Watcher) newOnLists(kev map[string]KEVEntry, vpatches map[string]VPatch) []Finding {
	names := w.names()
	var out []Finding
	if len(w.State.KEV) > 0 {
		for _, c := range slices.Sorted(mapKeys(kev)) {
			e := kev[c]
			if slices.Contains(w.State.KEV, c) || w.State.seen(c) || w.Repo.Published(c) {
				continue
			}
			what := names.match(e.Vendor, e.Product)
			if what == "" {
				continue
			}
			out = append(out, Finding{ID: c, Source: "CISA's list of exploited vulnerabilities", Title: e.Name,
				Link: "https://nvd.nist.gov/vuln/detail/" + c, Concerns: what, Found: w.Now(),
				Weak: "it's matched by name only (" + e.Vendor + " " + e.Product + "): check which packages carry it"})
		}
	}
	if len(w.State.VPatches) > 0 {
		for _, name := range slices.Sorted(mapKeys(vpatches)) {
			v := vpatches[name]
			if slices.Contains(w.State.VPatches, name) || w.State.seen(name) {
				continue
			}
			product, _, _ := strings.Cut(v.Label, " - ")
			what := names.match("", product)
			if what == "" {
				continue
			}
			f := Finding{ID: name, Aliases: v.CVEs, Source: "CrowdSec's virtual-patch rules", Title: nonEmpty(v.Label, name) + ": " + v.Description,
				Link:     "https://app.crowdsec.net/hub/author/crowdsecurity/appsec-rules/" + strings.TrimPrefix(name, "crowdsecurity/"),
				Concerns: what, Found: w.Now(), Signals: Signals{Exploited: true},
				Weak: "CrowdSec wrote a rule for it, so an exploit is likely public: check which packages it affects"}
			out = append(out, f)
		}
	}
	return out
}

// names are what the registry has, as products are named: its packages
// and built-with names (titles too), and the vendor and product of each
// built-with CPE.
type names map[string]string // normalised name → what it concerns

func (w *Watcher) names() names {
	n := names{}
	for _, p := range w.Repo.Packages {
		n[norm(p.Title)], n[norm(p.Name)] = "the "+p.Title+" package", "the "+p.Title+" package"
	}
	for _, b := range w.Repo.BuiltWith {
		what := "packages built with " + b.Title
		n[norm(b.Name)], n[norm(b.Title)] = what, what
		for _, cpe := range b.CPEs {
			parts := strings.Split(cpe, ":")
			if len(parts) > 4 {
				n[norm(parts[3])+"|"+norm(parts[4])] = what
			}
		}
	}
	for _, part := range Parts {
		n[norm(part.Title)] = part.Title + ", one of Roostack's own parts"
	}
	return n
}

// match is what a vendor's product concerns, or "".
func (n names) match(vendor, product string) string {
	if what := n[norm(vendor)+"|"+norm(product)]; what != "" {
		return what
	}
	return n[norm(product)]
}

func norm(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
