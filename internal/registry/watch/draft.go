package watch

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// maxTitle is the longest title an advisory may have (registry's check).
const maxTitle = 120

// draft writes a strong match's advisory into the repository and says
// what its pull request will say.
func (w *Watcher) draft(f Finding, now time.Time, taken []string) (Draft, error) {
	id := w.Repo.NextID(now.Year(), taken)
	data, err := AdvisoryFile(f, now)
	if err != nil {
		return Draft{}, err
	}
	path, err := w.Repo.WriteAdvisory(id, data)
	if err != nil {
		return Draft{}, err
	}
	d := Draft{Finding: f, Advisory: id, File: path, Branch: "watch/" + id, Urgent: f.Urgent()}
	d.PRTitle = "Advisory " + id + ": " + f.Title
	d.PRBody = prBody(d)
	return d, nil
}

// AdvisoryFile is a finding as an advisory file (registry.AdvisoriesDir),
// headed by a comment saying it's a draft to check.
func AdvisoryFile(f Finding, now time.Time) ([]byte, error) {
	type affected struct {
		Versions []string `yaml:"versions,flow"`
		Fixed    string   `yaml:"fixed,omitempty"`
	}
	a := struct {
		Title     string              `yaml:"title"`
		CVEs      []string            `yaml:"cves,omitempty,flow"`
		Severity  string              `yaml:"severity"`
		Exploited bool                `yaml:"exploited"`
		Published string              `yaml:"published"`
		Link      string              `yaml:"link,omitempty"`
		Affects   map[string]affected `yaml:"affects"`
	}{Title: oneLine(f.Title, maxTitle), CVEs: f.CVEs(), Severity: f.Severity, Exploited: f.Exploited,
		Published: now.Format(time.DateOnly), Affects: map[string]affected{f.Package: {Versions: f.Affected, Fixed: f.Fixed}}}
	if isHTTPS(f.Link) {
		a.Link = f.Link
	}
	body, err := yaml.Marshal(a)
	if err != nil {
		return nil, err
	}
	head := "# Drafted by roostack registry watch on " + now.Format(time.DateOnly) + " from " + f.Source + ".\n" +
		"# Check every line before merging: the title (what it lets someone do), the\n" +
		"# severity in Roostack's meaning, and the versions (docs/RED-BUTTON-PLAN.md §10.1.3).\n"
	return append([]byte(head), body...), nil
}

// oneLine is s on one line, cut to at most n bytes with an ellipsis.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(strings.NewReplacer("<", "‹", ">", "›").Replace(s)), " ")
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

// prBody is a draft's pull request: what it is, the evidence, and what to
// check.
func prBody(d Draft) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Drafted by `roostack registry watch` from %s: [%s](%s).\n\n", d.Source, d.ID, d.Link)
	if d.Urgent {
		b.WriteString("**Urgent: critical, and being used in attacks.** Labs that pause for exploited problems (the default) will pause it as soon as this is merged and published.\n\n")
	}
	fmt.Fprintf(&b, "- **Package:** `%s`, the registry's versions affected: %s\n", d.Package, code(d.Affected))
	switch {
	case d.Fixed != "":
		fmt.Fprintf(&b, "- **Fixed in:** `%s` (upstream: %s), which the registry has\n", d.Fixed, d.FixedUpstream)
	case d.FixedUpstream != "":
		fmt.Fprintf(&b, "- **Fixed upstream in:** %s, which the registry doesn't have yet: add `fixed:` once the update bot's pull request is merged\n", d.FixedUpstream)
	default:
		b.WriteString("- **Fixed in:** nothing yet\n")
	}
	fmt.Fprintf(&b, "- **Suggested severity:** %s, because %s\n", d.Severity, d.Why)
	if d.Exploited {
		b.WriteString("- **Being used in attacks:** yes\n")
	} else {
		b.WriteString("- **Being used in attacks:** nothing says so yet\n")
	}
	if d.EPSS > 0 {
		fmt.Fprintf(&b, "- **EPSS:** %s%% chance of being exploited in the next 30 days\n", strconv.FormatFloat(d.EPSS*100, 'f', 1, 64))
	}
	if cves := d.CVEs(); len(cves) > 0 {
		fmt.Fprintf(&b, "- **CVEs:** %s\n", strings.Join(cves, ", "))
	}
	b.WriteString("\nBefore merging, check:\n\n" +
		"1. The title says what the problem lets someone do, in plain words.\n" +
		"2. The severity in Roostack's meaning: critical only if anyone on the internet can use it without signing in, " +
		"to run code or read or change data, as Roostack runs the app (its private paths stay private).\n" +
		"3. The versions: every affected registry version, and `fixed` only when the registry has it.\n")
	return b.String()
}

func code(vs []string) string {
	var out []string
	for _, v := range vs {
		out = append(out, "`"+v+"`")
	}
	return strings.Join(out, ", ")
}

// Digest is the weekly digest's markdown, of the weaker matches waiting
// (State.Pending), most likely to be exploited first; empty when there
// are none.
func Digest(pending []Finding, now time.Time) string {
	if len(pending) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Weaker matches found by `roostack registry watch` up to %s, for a maintainer to look through. "+
		"Write an advisory for any that matters (or ask for one to be drafted), and close this issue when done.\n\n", now.Format(time.DateOnly))
	for _, f := range pending {
		fmt.Fprintf(&b, "- **[%s](%s)** %s\n", f.ID, f.Link, f.Title)
		what := f.Concerns
		if f.Package != "" {
			what = "`" + f.Package + "`"
			if len(f.Affected) > 0 {
				what += " " + code(f.Affected)
			}
		}
		fmt.Fprintf(&b, "  - %s, from %s. Suggested severity %s", nonEmpty(what, "unknown"), f.Source, f.Severity)
		if f.Exploited {
			b.WriteString(", being used in attacks")
		}
		if f.EPSS > 0 {
			fmt.Fprintf(&b, ", EPSS %s%%", strconv.FormatFloat(f.EPSS*100, 'f', 1, 64))
		}
		fmt.Fprintf(&b, ". Only in the digest because %s.\n", f.Weak)
	}
	return b.String()
}
