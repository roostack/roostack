package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"roostack.cloud/roostack/internal/registry/updates"
)

const updatesUsage = `Usage: roostack registry updates [-repo dir] [-only a,b] [-write]

Run hourly by the registry's CI (SPEC.md §8.3.8, docs/REGISTRY-UPDATES.md):
looks at each package's newest version and finds what it could update to.
A newer tag of the app's images (those whose tag carries the app's
version) is a new version, <version>-roostack.1; an image whose tag now
points to another digest (a rebuild) is the next revision of the same
version. updates.yaml in the registry says what each package follows. The
report (JSON, on standard output) gives each update's kind, as labs read
it (patch, minor, major, other, or revision for a rebuild), and why any
package couldn't be checked; -write puts each new revision in the
repository. It never commits or opens anything: the workflow does.
`

// registryUpdates finds the registry's updates, and with -write writes
// their new revisions, printing the report as JSON and a summary for a
// person on standard error.
func registryUpdates(args []string) error {
	fs := flag.NewFlagSet("registry updates", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(fs.Output(), updatesUsage); fs.PrintDefaults() }
	repo := fs.String("repo", ".", "the registry repository's checkout")
	only := fs.String("only", "", "only these packages, separated by commas")
	write := fs.Bool("write", false, "write each update's new revision into the repository")
	fs.Parse(args)
	rep, err := updates.Check(updates.Options{Repo: *repo, Only: splitList(*only), Write: *write, Now: time.Now()}, updates.Online{})
	if err != nil {
		return err
	}
	for _, u := range rep.Updates {
		fmt.Fprintf(os.Stderr, "%s: %s → %s (%s)\n", u.Package, u.From, u.To, u.Kind)
	}
	for _, s := range rep.Skipped {
		fmt.Fprintf(os.Stderr, "%s: not checked: %s\n", s.Package, s.Reason)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rep)
}
