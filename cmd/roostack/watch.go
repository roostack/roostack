package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"roostack.cloud/roostack/internal/registry/watch"
)

const watchUsage = `Usage:
  roostack registry watch [-repo dir] [-summary file] [-since 720h]
  roostack registry watch -digest file [-repo dir]

Run hourly by the registry's CI (docs/RED-BUTTON-PLAN.md §10.1): reads
GitHub's advisories for each package's source and Roostack's own parts,
CISA's list of exploited vulnerabilities, CrowdSec's virtual-patch rules,
EPSS, and OSV for the library versions in the images' bills of materials
(boms/). Each strong match (an exact registry version, critical or being
used in attacks) is drafted into advisories/ for a pull request; the
summary (JSON) says what the CI should open, and which are urgent. Weaker
matches wait in watch/state.json for the weekly digest, which -digest
writes as markdown (nothing when there are none) and clears. It never
publishes anything. GITHUB_TOKEN, if set, is sent to GitHub's API.
`

// registryWatch runs the registry's watcher, or writes its digest.
func registryWatch(args []string) error {
	fs := flag.NewFlagSet("registry watch", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, watchUsage) }
	repo := fs.String("repo", ".", "the registry repository's checkout")
	summary := fs.String("summary", "", "file for the run's summary (JSON; default: standard output)")
	since := fs.Duration("since", 30*24*time.Hour, "how far back the first run looks")
	digest := fs.String("digest", "", "write the weekly digest to this file and clear it, instead of running")
	fs.Parse(args)
	statePath := filepath.Join(*repo, watch.StateFile)
	st, err := watch.LoadState(statePath)
	if err != nil {
		return err
	}
	if *digest != "" {
		md := watch.Digest(st.Pending, time.Now())
		if md == "" {
			fmt.Println("Nothing for the digest.")
			return nil
		}
		if err := os.WriteFile(*digest, []byte(md), 0o644); err != nil {
			return err
		}
		fmt.Printf("Wrote %d weaker matches to %s.\n", len(st.Pending), *digest)
		st.Pending = nil
		return st.Save(statePath)
	}
	r, err := watch.ReadRepo(*repo)
	if err != nil {
		return err
	}
	feeds := watch.DefaultFeeds()
	feeds.GitHubToken = os.Getenv("GITHUB_TOKEN")
	w := &watch.Watcher{Repo: r, Feeds: feeds, State: st, Since: *since, Now: time.Now,
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	res, err := w.Run(context.Background())
	if err != nil {
		return err
	}
	if err := st.Save(statePath); err != nil {
		return err
	}
	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	if *summary == "" {
		_, err = os.Stdout.Write(append(out, '\n'))
		return err
	}
	fmt.Fprintf(os.Stderr, "%d drafted, %d for the digest, %d notes: %s\n", len(res.Drafts), len(res.Weak), len(res.Notes), *summary)
	return os.WriteFile(*summary, append(out, '\n'), 0o644)
}
