package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/pkgtest"
)

// Advisories (SPEC.md §6.16) are built into the index from the registry's
// advisories/ folder: each affected version with its file's SHA-256, the
// fix a version the registry has; anything else fails the build. A lab
// matches them by the exact file it has, and packages of an Owner's own by
// what they're built with when the advisory says they may be affected.
// What it's about must be on the registry's list of names, once there is one.
func TestAdvisories(t *testing.T) {
	r := newTestRegistry(t)
	fixed := strings.Replace(string(pkgtest.Read(t, "forgejo")), "version: 15.0.9-roostack.1", "version: 15.0.10-roostack.1", 1)
	r.write("packages/forgejo/15.0.10-roostack.1.yaml", fixed)
	os.MkdirAll(filepath.Join(r.repo, AdvisoriesDir), 0o755)
	advisory := `title: Forgejo lets anyone read private repositories
built_with: go
cves: [CVE-2026-41234]
severity: critical
exploited: true
published: 2026-10-22
link: https://codeberg.org/forgejo/forgejo/releases
affects:
  forgejo:
    versions: [15.0.9-roostack.1]
    fixed: 15.0.10-roostack.1
local: possibly
`
	r.write(AdvisoriesDir+"/2026-0007.yaml", advisory)
	ix, err := Build(BuildOptions{Repo: r.repo, Out: t.TempDir(), Now: r.now})
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Advisories) != 1 {
		t.Fatalf("advisories: %+v", ix.Advisories)
	}
	a := ix.Advisories[0]
	aff := a.Affects["forgejo"]
	if a.ID != "2026-0007" || !a.Exploited || !a.Local || aff.Fixed != "15.0.10-roostack.1" ||
		aff.Versions["15.0.9-roostack.1"] != ix.Packages["forgejo"].Versions["15.0.9-roostack.1"].SHA256 {
		t.Errorf("advisory: %+v %+v", a, aff)
	}
	file := pkgtest.Read(t, "forgejo")
	if got := ix.Affecting("forgejo", "15.0.9-roostack.1", file); len(got) != 1 {
		t.Errorf("Affecting: %v", got)
	}
	if ix.Affecting("forgejo", "15.0.9-roostack.1", []byte("an own package with the same name")) != nil ||
		ix.Affecting("forgejo", "15.0.10-roostack.1", []byte(fixed)) != nil {
		t.Error("only the registry's own file of an affected version")
	}
	if len(ix.Possibly([]string{"rust", "go"})) != 1 || ix.Possibly([]string{"rust"}) != nil {
		t.Error("Possibly")
	}

	for change, want := range map[string]string{
		"severity: critical":        "severity: \"urgent\" must be critical, high or low",
		"[15.0.9-roostack.1]":       "affects forgejo 9.9.9, which isn't in the registry",
		"fixed: 15.0.10-roostack.1": "fixed in 16.0.0, which isn't in the registry yet",
		"published: 2026-10-22":     "published: \"yesterday\" must be a date",
		"cves: [CVE-2026-41234]":    "isn't a CVE ID",
		"local: possibly":           "local: \"maybe\" must be possibly or no",
	} {
		bad := map[string]string{
			"severity: critical":        "severity: urgent",
			"[15.0.9-roostack.1]":       "[9.9.9]",
			"fixed: 15.0.10-roostack.1": "fixed: 16.0.0",
			"published: 2026-10-22":     "published: yesterday",
			"cves: [CVE-2026-41234]":    "cves: [41234]",
			"local: possibly":           "local: maybe",
		}[change]
		r.write(AdvisoriesDir+"/2026-0007.yaml", strings.Replace(advisory, change, bad, 1))
		if _, err := Build(BuildOptions{Repo: r.repo, Out: t.TempDir(), Now: r.now}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", bad, err, want)
		}
	}
	// With the registry's list of names, an advisory's must be on it.
	r.write(BuiltWithFile, "- { name: go, title: Go }\n- { name: java, title: Java }\n- { name: spring, title: Spring }\n"+
		"- { name: node, title: Node.js }\n- { name: python, title: Python }\n- { name: rust, title: Rust }\n")
	r.write(AdvisoriesDir+"/2026-0007.yaml", strings.Replace(advisory, "built_with: go", "built_with: cobol", 1))
	if _, err := Build(BuildOptions{Repo: r.repo, Out: t.TempDir(), Now: r.now}); err == nil ||
		!strings.Contains(err.Error(), `2026-0007.yaml: built_with: "cobol" isn't in the registry's built-with.yaml`) {
		t.Errorf("a name not on the list: %v", err)
	}
	r.write(AdvisoriesDir+"/2026-0007.yaml", advisory)
	if _, err := Build(BuildOptions{Repo: r.repo, Out: t.TempDir(), Now: r.now}); err != nil {
		t.Errorf("a name on the list: %v", err)
	}

	// One about Roostack itself: its releases, which labs match by their
	// own version.
	roostack := "title: Caddy lets anyone read any file\nseverity: critical\npublished: 2026-10-23\naffects:\n  roostack:\n    versions: [0.4.0, 0.4.1]\n    fixed: 0.4.2\n"
	r.write(AdvisoriesDir+"/2026-0009.yaml", roostack)
	ix, err = Build(BuildOptions{Repo: r.repo, Out: t.TempDir(), Now: r.now})
	if err != nil {
		t.Fatal(err)
	}
	if got := ix.AffectingRoostack("0.4.1"); len(got) != 1 || got[0].ID != "2026-0009" || got[0].Roostack.Fixed != "0.4.2" ||
		ix.AffectingRoostack("0.4.2") != nil || len(ix.Affecting("forgejo", "15.0.9-roostack.1", file)) != 1 {
		t.Errorf("AffectingRoostack: %+v", got)
	}
	for bad, with := range map[string]string{"[0.4.0, 0.4.1]": "[0.4.0, v4]", "fixed: 0.4.2": "fixed: 0.4.1"} {
		want := map[string]string{"[0.4.0, 0.4.1]": "isn't a Roostack release", "fixed: 0.4.2": "both affected and the fix"}[bad]
		r.write(AdvisoriesDir+"/2026-0009.yaml", strings.Replace(roostack, bad, with, 1))
		if _, err := Build(BuildOptions{Repo: r.repo, Out: t.TempDir(), Now: r.now}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", with, err, want)
		}
	}
	os.Remove(filepath.Join(r.repo, AdvisoriesDir, "2026-0009.yaml"))
	os.Rename(filepath.Join(r.repo, AdvisoriesDir, "2026-0007.yaml"), filepath.Join(r.repo, AdvisoriesDir, "php-bug.yaml"))
	if _, err := Build(BuildOptions{Repo: r.repo, Out: t.TempDir(), Now: r.now}); err == nil || !strings.Contains(err.Error(), "must be the year and a number") {
		t.Errorf("an ID that isn't one: %v", err)
	}
}
