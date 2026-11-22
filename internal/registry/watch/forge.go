package watch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/pkgspec"
)

// Forgejo hosts (Codeberg) have no security advisories to read: their
// API has no advisories (it answers 404), and OSV has nothing for Forgejo.
// Forgejo says which of its releases fix security problems in their
// notes, under a "Security bug fixes" entry, so that's what the watcher
// reads for a package whose source is on one (checked 2026-11-19).

// forgeHosts are the Forgejo hosts the watcher reads releases from.
var forgeHosts = []string{"codeberg.org"}

var forgeRepoRe = regexp.MustCompile(`^https://([a-z0-9.-]+)/([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(\.git)?/?$`)

// ForgeRepo is a source address's Forgejo host and repository
// ("codeberg.org", "forgejo/forgejo"), or "" for anything else.
func ForgeRepo(source string) (host, repo string) {
	m := forgeRepoRe.FindStringSubmatch(source)
	if m == nil || !slices.Contains(forgeHosts, m[1]) {
		return "", ""
	}
	return m[1], m[2] + "/" + m[3]
}

// Release is a release on a Forgejo host, as its API lists it.
type Release struct {
	Tag        string    `json:"tag_name"`
	Body       string    `json:"body"`
	URL        string    `json:"html_url"`
	Published  time.Time `json:"published_at"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
}

// forgeBase is a Forgejo host's address, or the test server standing in
// for every host.
func (f Feeds) forgeBase(host string) string {
	if f.ForgeURL != "" {
		return f.ForgeURL
	}
	return "https://" + host
}

// ForgeReleases are a repository's 50 newest releases on a Forgejo host.
func (f Feeds) ForgeReleases(ctx context.Context, host, repo string) ([]Release, error) {
	var out []Release
	err := f.get(ctx, f.forgeBase(host)+"/api/v1/repos/"+repo+"/releases?limit=50", &out)
	return out, err
}

var seeNotesRe = regexp.MustCompile(`^See (https://\S+\.md)$`)

// ReleaseNotes are a release's notes: its text, or the file on the same
// host it points to instead (Forgejo's releases say only "See
// https://codeberg.org/forgejo/forgejo/src/branch/…/15.0.12.md"), read raw.
func (f Feeds) ReleaseNotes(ctx context.Context, host string, r Release) (string, error) {
	m := seeNotesRe.FindStringSubmatch(strings.TrimSpace(r.Body))
	if m == nil {
		return r.Body, nil
	}
	rest, ok := strings.CutPrefix(m[1], "https://"+host+"/")
	if !ok {
		return r.Body, nil
	}
	addr := f.forgeBase(host) + "/" + strings.Replace(rest, "/src/", "/raw/", 1)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "roostack-registry-watch")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", addr, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return string(data), err
}

var (
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	mdLinkRe      = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	prPrefixRe    = regexp.MustCompile(`^PR( \(backported\))?: *`)
)

// SecurityFixes are the security fixes a release's notes list: the items
// under a heading or a top-level entry that names security ("- Security
// bug fixes", "## Security"), each as one line of at most 200 bytes.
func SecurityFixes(notes string) []string {
	var out []string
	// Under a heading naming security, its entries are the fixes; under
	// any other, an entry naming security heads the fixes nested in it.
	heading, entry := false, false
	security := func(s string) bool { return strings.Contains(strings.ToLower(cleanNote(s)), "security") }
	for _, line := range strings.Split(notes, "\n") {
		trimmed := strings.TrimSpace(line)
		item := strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ")
		switch {
		case strings.HasPrefix(line, "#"):
			heading, entry = security(trimmed), false
			continue
		case !item:
			continue
		case line == trimmed && !heading:
			entry = security(trimmed)
			continue
		case line != trimmed && !entry && !heading:
			continue
		}
		if fix := cleanNote(trimmed[2:]); fix != "" {
			out = append(out, oneLine(fix, 200))
		}
	}
	return out
}

// cleanNote is a line of release notes without comments, link targets or
// Forgejo's "PR:" prefix.
func cleanNote(s string) string {
	s = htmlCommentRe.ReplaceAllString(s, "")
	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = strings.TrimLeft(strings.TrimSpace(s), "#*- ")
	return strings.TrimSpace(prPrefixRe.ReplaceAllString(s, ""))
}

// sameLine says whether two app versions share their major version, as a
// Forgejo release fixes its own line (15.0.12 for 15.0.x, while 17.0.2
// fixes 17.0.x).
func sameLine(a, b string) bool {
	ma, _, _ := strings.Cut(strings.TrimPrefix(a, "v"), ".")
	mb, _, _ := strings.Cut(strings.TrimPrefix(b, "v"), ".")
	return ma == mb
}

// fromForge are the releases on a package's Forgejo host since the cutoff
// whose notes list security fixes, as findings: the registry's versions on
// the same line before each are affected. They're weak, as the notes don't
// say how serious each problem is.
func (w *Watcher) fromForge(ctx context.Context, p *Package, host, repo string, cutoff time.Time, note func(string, ...any)) []Finding {
	releases, err := w.Feeds.ForgeReleases(ctx, host, repo)
	if err != nil {
		note("%s: its releases on %s couldn't be read: %v", p.Title, host, err)
		return nil
	}
	var out []Finding
	for _, r := range releases {
		id := host + "/" + repo + "@" + r.Tag
		if r.Draft || r.Prerelease || r.Published.Before(cutoff) || w.State.seen(id) {
			continue
		}
		notes, err := w.Feeds.ReleaseNotes(ctx, host, r)
		if err != nil {
			note("%s: the notes for %s couldn't be read: %v", p.Title, r.Tag, err)
			continue
		}
		fixes := SecurityFixes(notes)
		if len(fixes) == 0 {
			continue
		}
		ver := strings.TrimPrefix(r.Tag, "v")
		f := Finding{ID: id, Source: "the release notes on " + host, Link: r.URL, Package: p.Name, FixedUpstream: ">= " + ver, Found: w.Now(),
			Title: oneLine(fmt.Sprintf("%s %s fixes %s: %s", p.Title, ver, plural(len(fixes), "security problem", "security problems"), fixes[0]), maxTitle),
			Weak:  "its release notes list security fixes but don't say how serious they are: read them"}
		for _, v := range p.Versions {
			up := Upstream(v)
			switch {
			case !sameLine(up, ver):
			case pkgspec.CompareVersions(up, ver) < 0:
				f.Affected = append(f.Affected, v)
			case f.Fixed == "":
				f.Fixed = v
			}
		}
		if len(f.Affected) == 0 {
			w.State.Seen[id] = "doesn't affect the registry's versions"
			continue
		}
		out = append(out, f)
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
