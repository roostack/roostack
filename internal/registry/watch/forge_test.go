package watch

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// forgejoNotes is the start of Forgejo 15.0.12's notes, as published.
const forgejoNotes = `

<!--start release-notes-assistant-->

## Release notes
<!--URL:https://codeberg.org/forgejo/forgejo-->
- Security bug fixes
  - [PR](https://codeberg.org/forgejo/forgejo/pulls/15299): <!--number 15299 --><!--line 0 --><!--description c2VhcmNo-->search for authorized SSH public keys by exact check.  When Forgejo's internal SSH server is in-use, the content of public SSH keys was checked with a SQL ` + "`LIKE`" + ` operation.<!--description-->
  - [PR](https://codeberg.org/forgejo/forgejo/pulls/15299): <!--number 15299 --><!--line 1 --><!--description Y2hlY2s=-->check email activation in MakeEmailAddressPrimary.<!--description-->
- Bug fixes
  - [PR](https://codeberg.org/forgejo/forgejo/pulls/15099) ([backported](https://codeberg.org/forgejo/forgejo/pulls/15110)): <!--number 15110 -->fix: populate 'access' table<!--description-->
- Security changes without a feature or bug label
  - [PR](https://codeberg.org/forgejo/forgejo/pulls/15242) ([backported](https://codeberg.org/forgejo/forgejo/pulls/15304)): <!--number 15304 -->packages(debian): use pault.ag/go/debian/deb to parse .deb files<!--description-->
- Included for completeness but not user-facing (chores, etc.)
  - [PR](https://codeberg.org/forgejo/forgejo/pulls/15063): Update module golang.org/x/net to v0.62.0 [SECURITY] (v15.0/forgejo)
<!--end release-notes-assistant-->
`

func TestSecurityFixes(t *testing.T) {
	got := SecurityFixes(forgejoNotes)
	if len(got) != 3 || !strings.HasPrefix(got[0], "search for authorized SSH public keys by exact check.") ||
		got[1] != "check email activation in MakeEmailAddressPrimary." || !strings.HasPrefix(got[2], "packages(debian):") {
		t.Errorf("fixes: %q", got)
	}
	if got := SecurityFixes("## 1.2.0\n\n- Faster pages\n- A new theme\n"); len(got) != 0 {
		t.Errorf("notes without security fixes: %q", got)
	}
	if got := SecurityFixes("## Security\n\n- Escape titles (CVE-2026-1)\n\n## Features\n\n- Dark mode\n"); len(got) != 1 || got[0] != "Escape titles (CVE-2026-1)" {
		t.Errorf("a heading: %q", got)
	}
	for source, want := range map[string]string{"https://codeberg.org/forgejo/forgejo": "codeberg.org forgejo/forgejo",
		"https://codeberg.org/forgejo/forgejo.git": "codeberg.org forgejo/forgejo", "https://github.com/a/b": " ", "https://gitlab.com/a/b": " "} {
		if host, repo := ForgeRepo(source); host+" "+repo != want {
			t.Errorf("ForgeRepo(%q) = %q %q", source, host, repo)
		}
	}
}

// forgejoReleases is Codeberg answering for Forgejo's releases: 15.0.12
// with security fixes in the file its notes point to, 17.0.2 (another
// line) with some in its own text, 15.0.11 without, and a pre-release.
func forgejoReleases(now time.Time) map[string]string {
	rel, _ := json.Marshal([]map[string]any{
		{"tag_name": "v17.0.2", "body": "- Security bug fixes\n  - escape the wiki's titles\n", "html_url": "https://codeberg.org/forgejo/forgejo/releases/tag/v17.0.2", "published_at": now.Add(-time.Hour)},
		{"tag_name": "v15.0.13-rc1", "body": "- Security bug fixes\n  - something\n", "prerelease": true, "published_at": now.Add(-time.Hour)},
		{"tag_name": "v15.0.12", "body": "See https://codeberg.org/forgejo/forgejo/src/branch/forgejo/release-notes-published/15.0.12.md",
			"html_url": "https://codeberg.org/forgejo/forgejo/releases/tag/v15.0.12", "published_at": now.Add(-2 * time.Hour)},
		{"tag_name": "v15.0.11", "body": "- Bug fixes\n  - faster\n", "html_url": "https://codeberg.org/forgejo/forgejo/releases/tag/v15.0.11", "published_at": now.Add(-48 * time.Hour)},
	})
	return map[string]string{
		"/api/v1/repos/forgejo/forgejo/releases":                                 string(rel),
		"/forgejo/forgejo/raw/branch/forgejo/release-notes-published/15.0.12.md": forgejoNotes,
	}
}

// TestWatchForge: a package whose source is on Codeberg has its releases'
// notes read, and a release on its line listing security fixes is a weak
// match for the registry's older versions on that line.
func TestWatchForge(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	ff := &fakeFeeds{advisories: map[string]string{"dani-garcia/vaultwarden": "[]"}, forge: forgejoReleases(now)}
	repo, err := ReadRepo(testRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Seen: map[string]string{}}
	w := &Watcher{Repo: repo, Feeds: ff.serve(t), State: st, Now: func() time.Time { return now }}
	res, err := w.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var forgejo []Finding
	for _, f := range res.Weak {
		if f.Package == "forgejo" {
			forgejo = append(forgejo, f)
		}
	}
	if len(forgejo) != 1 {
		t.Fatalf("Forgejo's matches: %+v (notes %v)", forgejo, res.Notes)
	}
	f := forgejo[0]
	if f.ID != "codeberg.org/forgejo/forgejo@v15.0.12" || strings.Join(f.Affected, " ") != "15.0.9-roostack.1" || f.FixedUpstream != ">= 15.0.12" ||
		!strings.HasPrefix(f.Title, "Forgejo 15.0.12 fixes 3 security problems: search for authorized SSH public keys") || f.Weak == "" ||
		f.Link != "https://codeberg.org/forgejo/forgejo/releases/tag/v15.0.12" {
		t.Errorf("match: %+v", f)
	}
	// 17.0.2 fixes another line, so nothing of the registry's.
	if st.Seen["codeberg.org/forgejo/forgejo@v17.0.2"] != "doesn't affect the registry's versions" {
		t.Errorf("seen: %v", st.Seen)
	}
	if strings.Contains(strings.Join(res.Notes, "\n"), "Forgejo") {
		t.Errorf("notes: %v", res.Notes)
	}
}

// TestFixes: what a new version fixes, from GitHub's advisories (affecting
// the old version and not the new) or Forgejo's release notes (the
// releases after the old version, up to the new).
func TestFixes(t *testing.T) {
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	critical := "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"
	withdrawn := ghsa("GHSA-wwww-wwww-wwww", "", "high", critical, "< 1.35.0", "1.35.0", now)
	withdrawn["withdrawn_at"] = now
	advs, _ := json.Marshal([]map[string]any{
		ghsa("GHSA-aaaa-bbbb-cccc", "", "critical", critical, "< 1.35.0", "1.35.0", now),
		ghsa("GHSA-dddd-eeee-ffff", "", "high", critical, "<= 1.35.0", "", now),
		ghsa("GHSA-gggg-hhhh-jjjj", "", "low", critical, "< 1.30.0", "1.30.0", now),
		withdrawn,
	})
	ff := &fakeFeeds{advisories: map[string]string{"dani-garcia/vaultwarden": string(advs)}, forge: forgejoReleases(now)}
	feeds := ff.serve(t)
	got, err := feeds.Fixes(context.Background(), "https://github.com/dani-garcia/vaultwarden", "1.34.3", "1.35.0")
	if err != nil || len(got) != 1 || got[0].ID != "GHSA-aaaa-bbbb-cccc" || got[0].Severity != "critical" || got[0].Title != "lets anyone read every vault" {
		t.Errorf("GitHub: %+v %v", got, err)
	}
	got, err = feeds.Fixes(context.Background(), "https://codeberg.org/forgejo/forgejo", "15.0.11", "15.0.12")
	if err != nil || len(got) != 1 || got[0].ID != "codeberg.org/forgejo/forgejo@v15.0.12" ||
		!strings.HasPrefix(got[0].Title, "3 security fixes in 15.0.12: search for") {
		t.Errorf("Codeberg: %+v %v", got, err)
	}
	if got, err := feeds.Fixes(context.Background(), "https://codeberg.org/forgejo/forgejo", "15.0.12", "15.0.12"); err != nil || len(got) != 0 {
		t.Errorf("no newer release: %+v %v", got, err)
	}
	if got, err := feeds.Fixes(context.Background(), "https://gitlab.com/a/b", "1", "2"); err != nil || got != nil {
		t.Errorf("elsewhere: %+v %v", got, err)
	}
}

// TestDigest: the digest lists what needs a decision, one entry per
// problem with a line for each package, and collapses what the registry's
// newest version has fixed since and what's in images' base systems,
// libraries inside images that aren't serious, keeping those that are
// critical or being used in attacks listed.
func TestDigest(t *testing.T) {
	now := time.Date(2026, 11, 23, 8, 0, 0, 0, time.UTC)
	repo, err := ReadRepo(testRepo(t)) // Vaultwarden 1.34.3 and 1.34.4, Forgejo 15.0.9
	if err != nil {
		t.Fatal(err)
	}
	repo.BOMs["vaultwarden 1.34.4-roostack.1"] = []string{"pkg:npm/fast-xml-parser@5.8.0", "pkg:deb/ubuntu/python3.12@3.12.3"}
	old, newest := "1.34.3-roostack.1", "1.34.4-roostack.1"
	pending := []Finding{
		// Fixed since: 1.34.4 is outside its range.
		{ID: "GHSA-fixd-fixd-fixd", Package: "vaultwarden", Title: "Vaultwarden: fixed since", Ranges: []string{"< 1.34.4"}, Affected: []string{old}, Severity: "low", Weak: "w"},
		// Still affected.
		{ID: "GHSA-open-open-open", Package: "vaultwarden", Title: "Vaultwarden: still there", Ranges: []string{"<= 1.34.4"},
			Affected: []string{old, newest}, Severity: "high", Signals: Signals{EPSS: 0.01}, Weak: "it isn't critical"},
		// A library the newest image no longer has (found before the
		// package URL was kept: read from the source).
		{ID: "GHSA-gone-gone-gone", Package: "vaultwarden", Title: "Vaultwarden: gone from the image", Source: "OSV, for pkg:npm/node-forge@1.4.0 in its image",
			Affected: []string{old}, FixedUpstream: "node-forge 1.4.1", Severity: "high", Weak: "w"},
		// A base system's package, low: in the background.
		{ID: "UBUNTU-CVE-2026-1", Package: "vaultwarden", Title: "Vaultwarden: python", Purl: "pkg:deb/ubuntu/python3.12@3.12.3",
			Affected: []string{newest}, FixedUpstream: "python3.12 3.12.4", Severity: "low", Weak: "w"},
		// The same, critical: listed.
		{ID: "UBUNTU-CVE-2026-2", Package: "vaultwarden", Title: "Vaultwarden: python, badly", Purl: "pkg:deb/ubuntu/python3.12@3.12.3",
			Affected: []string{newest}, Severity: "critical", Weak: "it was published before the watcher's window"},
		// One problem in two packages, being used in attacks: one entry,
		// a line each.
		{ID: "GHSA-474x-hrwr-fgx4", Package: "vaultwarden", Title: "Vaultwarden: fast-xml-parser: quadratic CPU", Purl: "pkg:npm/fast-xml-parser@5.8.0",
			Affected: []string{newest}, FixedUpstream: "fast-xml-parser 5.8.1", Severity: "low", Signals: Signals{EPSS: 0.002, Exploited: true}, Source: "OSV", Weak: "it isn't critical"},
		{ID: "GHSA-474x-hrwr-fgx4", Package: "forgejo", Title: "Forgejo: fast-xml-parser: quadratic CPU", Purl: "pkg:npm/fast-xml-parser@5.8.0",
			Affected: []string{"15.0.9-roostack.1"}, FixedUpstream: "fast-xml-parser 5.8.1", Severity: "low", Signals: Signals{Exploited: true}, Source: "OSV", Weak: "it isn't critical"},
		// The same problem and package twice: one line, every version.
		{ID: "GHSA-open-open-open", Package: "vaultwarden", Title: "Vaultwarden: still there", Ranges: []string{"<= 1.34.4"},
			Affected: []string{old}, Severity: "high", Weak: "it isn't critical"},
	}
	md := Digest(pending, repo, now)
	main, rest, _ := strings.Cut(md, "<details>")
	if !strings.Contains(main, "3 problems need a decision") || strings.Count(main, "- **[") != 3 {
		t.Errorf("the main list:\n%s", main)
	}
	// Being used in attacks first, then critical, then by EPSS.
	if i, j, k := strings.Index(main, "GHSA-474x"), strings.Index(main, "UBUNTU-CVE-2026-2"), strings.Index(main, "GHSA-open"); !(i < j && j < k) {
		t.Errorf("order:\n%s", main)
	}
	for _, want := range []string{
		"**[GHSA-474x-hrwr-fgx4]()** fast-xml-parser: quadratic CPU\n  - `vaultwarden` `1.34.4-roostack.1` (the newest), from OSV.",
		"  - `forgejo` `15.0.9-roostack.1` (the newest), from OSV",
		"`vaultwarden` `1.34.4-roostack.1` (the newest) and 1 older version",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("the main list lacks %q:\n%s", want, main)
		}
	}
	if strings.Count(main, "GHSA-open-open-open") != 1 {
		t.Errorf("a problem listed twice:\n%s", main)
	}
	fixed, background, _ := strings.Cut(rest, "<details>")
	if !strings.Contains(fixed, "2 problems already fixed in the registry's newest version") || !strings.Contains(fixed, "GHSA-fixd-fixd-fixd") ||
		!strings.Contains(fixed, "GHSA-gone-gone-gone") || !strings.Contains(fixed, "`vaultwarden` (newest `1.34.4-roostack.1`)") {
		t.Errorf("the fixed part:\n%s", fixed)
	}
	if !strings.Contains(background, "1 problem in the background") || !strings.Contains(background, "UBUNTU-CVE-2026-1") || strings.Count(md, "</details>") != 2 {
		t.Errorf("the background:\n%s", background)
	}
	// A match from before 2026-11-19 naming another package's library (its
	// affected version's bill of materials doesn't have it) isn't taken as
	// fixed, though the newest image doesn't have it either.
	repo.BOMs["vaultwarden "+old] = []string{"pkg:npm/node-forge@1.3.0"}
	misnamed := Finding{ID: "GHSA-mis-named", Package: "vaultwarden", Title: "Vaultwarden: misnamed", Source: "OSV, for pkg:npm/node-forge@1.4.0 in its image",
		Affected: []string{old}, Severity: "critical", Weak: "w"}
	if md := Digest([]Finding{misnamed}, repo, now); !strings.Contains(md, "1 problem needs a decision") {
		t.Errorf("a misnamed library taken as fixed:\n%s", md)
	}
	delete(repo.BOMs, "vaultwarden "+old)
	// Nothing open.
	if md := Digest(pending[:1], repo, now); !strings.Contains(md, "None needs a decision this week.") {
		t.Errorf("nothing open:\n%s", md)
	}
	// Without the registry, everything is listed.
	if md := Digest(pending[:1], nil, now); !strings.Contains(md, "1 problem needs a decision") {
		t.Errorf("without the registry:\n%s", md)
	}
}
