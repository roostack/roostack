package watch

import (
	"context"
	"fmt"
	"strings"

	"roostack.cloud/roostack/internal/pkgspec"
)

// Fix is a known security problem that an app's newer version fixes
// (SPEC.md §8.3.8): the update bot labels an update carrying one
// "security", and it merges without waiting for a person once its test
// has passed, even when it's a minor.
type Fix struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Link     string `json:"link"`
	Severity string `json:"severity,omitempty"`
}

// Fixes are the known security problems that an app's version from has
// and to doesn't: from its source's advisories on GitHub (affecting from,
// and not to), or its releases on a Forgejo host after from and up to to
// whose notes list security fixes. None for a source elsewhere.
func (f Feeds) Fixes(ctx context.Context, source, from, to string) ([]Fix, error) {
	from, to = strings.TrimPrefix(from, "v"), strings.TrimPrefix(to, "v")
	var out []Fix
	if repo := GitHubRepo(source); repo != "" {
		advs, err := f.RepoAdvisories(ctx, repo)
		if err != nil {
			return nil, err
		}
		for _, a := range advs {
			if !a.Withdrawn.IsZero() {
				continue
			}
			had, has := false, false
			for _, v := range a.Vulnerables {
				had = had || InRange(from, v.Range)
				has = has || InRange(to, v.Range)
			}
			if had && !has {
				out = append(out, Fix{ID: a.ID, Title: oneLine(a.Summary, maxTitle), Link: a.URL, Severity: a.Severity})
			}
		}
		return out, nil
	}
	if host, repo := ForgeRepo(source); host != "" {
		releases, err := f.ForgeReleases(ctx, host, repo)
		if err != nil {
			return nil, err
		}
		for _, r := range releases {
			v := strings.TrimPrefix(r.Tag, "v")
			if r.Draft || r.Prerelease || pkgspec.CompareVersions(v, from) <= 0 || pkgspec.CompareVersions(v, to) > 0 {
				continue
			}
			notes, err := f.ReleaseNotes(ctx, host, r)
			if err != nil {
				return nil, err
			}
			if fixes := SecurityFixes(notes); len(fixes) > 0 {
				out = append(out, Fix{ID: host + "/" + repo + "@" + r.Tag, Link: r.URL,
					Title: oneLine(fmt.Sprintf("%s in %s: %s", plural(len(fixes), "security fix", "security fixes"), v, fixes[0]), maxTitle)})
			}
		}
	}
	return out, nil
}
