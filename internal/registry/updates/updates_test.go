package updates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/imagecheck"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// fakeRegs is image registries in memory: each repository's tags, what
// each tag points to, and each digest's platforms.
type fakeRegs struct {
	tags      map[string][]string // registry/repo
	digests   map[string]string   // registry/repo:tag
	platforms map[string][]string // digest; both processors when absent
}

func (f fakeRegs) Tags(r, repo string) ([]string, error) { return f.tags[r+"/"+repo], nil }
func (f fakeRegs) TagDigest(r, repo, tag string) (string, error) {
	if d, ok := f.digests[r+"/"+repo+":"+tag]; ok {
		return d, nil
	}
	return "", imagecheck.ErrGone
}
func (f fakeRegs) Platforms(_, _, d string) ([]string, error) {
	if p, ok := f.platforms[d]; ok {
		return p, nil
	}
	return []string{"linux/amd64", "linux/arm64"}, nil
}

// digest makes a fake digest from a short name.
func digest(s string) string { return "sha256:" + strings.Repeat("0", 64-len(s)) + s }

// repo is a registry checkout holding the named fixtures, and rules as
// updates.yaml.
func repo(t *testing.T, rules string, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		src := filepath.Join(pkgtest.Dir(), n)
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(dir, "packages", n), 0o755)
		for _, e := range entries {
			data, _ := os.ReadFile(filepath.Join(src, e.Name()))
			os.WriteFile(filepath.Join(dir, "packages", n, e.Name()), data, 0o644)
		}
	}
	if rules != "" {
		os.WriteFile(filepath.Join(dir, "updates.yaml"), []byte(rules), 0o644)
	}
	return dir
}

// pinned is a fixture's current digest for a tag, so a fake registry can
// say it hasn't moved.
const (
	vwPinned  = "sha256:84fd8a47f58d79a1ad824c27be0a9492750c0fa5216b35c749863093bfa3c3d7"
	vwRepo    = "docker.io/vaultwarden/server"
	fjRepo    = "codeberg.org/forgejo/forgejo"
	fjPinned  = "sha256:caf1bca332f95cdcf124227a4bfa3b49bbbfbbc8a5e4a97406921cb581165413"
	srvRepo   = "ghcr.io/immich-app/immich-server"
	mlRepo    = "ghcr.io/immich-app/immich-machine-learning"
	pgRepo    = "ghcr.io/immich-app/postgres"
	pgTag     = "14-vectorchord0.4.3-pgvectors0.2.0"
	pgPinned  = "sha256:bcf63357191b76a916ae5eb93464d65c07511da41e3bf7a8416db519b40b1c23"
	srvPinned = "sha256:72a9b9de6c6abfa7a9c9cdc244ae4d2bd9fea2ae00997f194cbd10aca72ea210"
	mlPinned  = "sha256:24bfef29bc5c0923c64c98810931eda1449a4b237e6704a715605761bc107ae4"
)

var day = time.Date(2026, 10, 29, 3, 0, 0, 0, time.UTC)

// A newer app version is a new package version, -roostack.1, written from
// the newest file's text (its comments kept), and checked as a package.
// Pre-releases, variants and "latest" never count.
func TestNewVersion(t *testing.T) {
	dir := repo(t, "", "vaultwarden")
	regs := fakeRegs{
		tags:    map[string][]string{vwRepo: {"1.34.3", "1.34.4", "1.35.0", "1.36.0-alpine", "2.0.0-rc1", "latest", "1.35"}},
		digests: map[string]string{vwRepo + ":1.34.3": vwPinned, vwRepo + ":1.34.4": digest("a"), vwRepo + ":1.35.0": digest("b")},
	}
	rep, err := Check(Options{Repo: dir, Write: true, Now: day}, regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Updates) != 1 || len(rep.Skipped) != 0 {
		t.Fatalf("report %+v", rep)
	}
	u := rep.Updates[0]
	if u.From != "1.34.3-roostack.1" || u.To != "1.35.0-roostack.1" || u.Kind != pkgspec.UpdateMinor || u.Tier != "official" {
		t.Errorf("update %+v", u)
	}
	want := vwRepo + ":1.35.0@" + digest("b")
	if len(u.Images) != 1 || u.Images[0].To != want || u.Images[0].Rebuilt {
		t.Errorf("images %+v", u.Images)
	}
	data, err := os.ReadFile(filepath.Join(dir, u.File))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "version: 1.35.0-roostack.1          # upstream version + package revision") ||
		!strings.Contains(string(data), "image: "+want) {
		t.Errorf("the new file:\n%s", data)
	}
	if p, err := pkgspec.Parse(data); err != nil || p.Version != u.To {
		t.Errorf("the new file doesn't check: %v", err)
	}
}

// Track and ignore limit which versions are offered.
func TestRules(t *testing.T) {
	regs := fakeRegs{
		tags:    map[string][]string{vwRepo: {"1.34.3", "1.34.4", "1.34.5", "1.35.0", "2.0.0"}},
		digests: map[string]string{vwRepo + ":1.34.3": vwPinned, vwRepo + ":1.34.4": digest("a"), vwRepo + ":1.34.5": digest("c"), vwRepo + ":1.35.0": digest("b"), vwRepo + ":2.0.0": digest("d")},
	}
	for _, c := range []struct{ rules, to, kind string }{
		{"", "2.0.0-roostack.1", pkgspec.UpdateMajor},
		{"packages: {vaultwarden: {track: minor}}", "1.35.0-roostack.1", pkgspec.UpdateMinor},
		{"packages: {vaultwarden: {track: patch}}", "1.34.5-roostack.1", pkgspec.UpdatePatch},
		{"packages: {vaultwarden: {track: patch, ignore: [1.34.5]}}", "1.34.4-roostack.1", pkgspec.UpdatePatch},
	} {
		rep, err := Check(Options{Repo: repo(t, c.rules, "vaultwarden")}, regs)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Updates) != 1 || rep.Updates[0].To != c.to || rep.Updates[0].Kind != c.kind {
			t.Errorf("%q: %+v", c.rules, rep)
		}
	}
	rep, _ := Check(Options{Repo: repo(t, "packages: {vaultwarden: {hold: true, reason: testing}}", "vaultwarden")}, regs)
	if len(rep.Updates) != 0 || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "held in updates.yaml: testing") {
		t.Errorf("held: %+v", rep)
	}
	if _, err := ReadRules(repo(t, "packages: {vaultwarden: {track: sideways}}")); err == nil {
		t.Error("a bad track was taken")
	}
	if _, err := ReadRules(repo(t, "packages: {vaultwarden: {tags: {app: latest}}}")); err == nil {
		t.Error("a tag without {version} was taken")
	}
}

// Immich's two app images must both have the new version (the newest both
// have wins), and its Postgres, a sidecar, comes along when it was rebuilt.
func TestSeveralImages(t *testing.T) {
	regs := fakeRegs{
		tags: map[string][]string{
			srvRepo: {"v2.1.0", "v2.1.1", "v2.2.0"},
			mlRepo:  {"v2.1.0", "v2.1.1", "v2.1.1-cuda", "v2.2.0-cuda"},
		},
		digests: map[string]string{
			srvRepo + ":v2.1.0": srvPinned, srvRepo + ":v2.1.1": digest("s1"), srvRepo + ":v2.2.0": digest("s2"),
			mlRepo + ":v2.1.0": mlPinned, mlRepo + ":v2.1.1": digest("m1"),
			pgRepo + ":" + pgTag: digest("pg2"),
		},
	}
	rep, err := Check(Options{Repo: repo(t, "", "immich")}, regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Updates) != 1 {
		t.Fatalf("report %+v", rep)
	}
	u := rep.Updates[0]
	if u.To != "2.1.1-roostack.1" || u.Kind != pkgspec.UpdatePatch || len(u.Images) != 3 {
		t.Fatalf("update %+v", u)
	}
	for _, c := range u.Images {
		if c.Container == "database.main" && (!c.Rebuilt || c.To != pgRepo+":"+pgTag+"@"+digest("pg2")) {
			t.Errorf("postgres %+v", c)
		}
		if c.Container != "database.main" && (c.Rebuilt || !strings.Contains(c.To, ":v2.1.1@")) {
			t.Errorf("app image %+v", c)
		}
	}
}

// A tag pointing to a new digest, with no newer version, is the next
// revision of the same version.
func TestRebuild(t *testing.T) {
	regs := fakeRegs{
		tags:    map[string][]string{fjRepo: {"15.0.9-rootless", "16.0.0-rootless"}},
		digests: map[string]string{fjRepo + ":15.0.9-rootless": digest("rebuilt")},
	}
	rep, err := Check(Options{Repo: repo(t, "packages: {forgejo: {track: minor, reason: the long-term support line}}", "forgejo")}, regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Updates) != 1 || rep.Updates[0].To != "15.0.9-roostack.2" || rep.Updates[0].Kind != pkgspec.UpdateRevision ||
		!rep.Updates[0].Images[0].Rebuilt {
		t.Fatalf("report %+v", rep)
	}
	// Unchanged digests: nothing.
	regs.digests[fjRepo+":15.0.9-rootless"] = fjPinned
	if rep, _ := Check(Options{Repo: repo(t, "packages: {forgejo: {track: minor}}", "forgejo")}, regs); len(rep.Updates) != 0 {
		t.Errorf("unchanged: %+v", rep)
	}
	// rebuilds: false leaves a rebuild alone.
	regs.digests[fjRepo+":15.0.9-rootless"] = digest("rebuilt")
	if rep, _ := Check(Options{Repo: repo(t, "packages: {forgejo: {track: minor, rebuilds: false}}", "forgejo")}, regs); len(rep.Updates) != 0 {
		t.Errorf("rebuilds off: %+v", rep)
	}
}

// An image that loses a processor the pinned one had is skipped, saying
// why, rather than offered.
func TestPlatforms(t *testing.T) {
	regs := fakeRegs{
		tags:      map[string][]string{vwRepo: {"1.34.3", "1.34.4"}},
		digests:   map[string]string{vwRepo + ":1.34.3": vwPinned, vwRepo + ":1.34.4": digest("a")},
		platforms: map[string][]string{digest("a"): {"linux/amd64"}},
	}
	rep, err := Check(Options{Repo: repo(t, "", "vaultwarden")}, regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Updates) != 0 || len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, "no build for linux/arm64") {
		t.Errorf("report %+v", rep)
	}
}

// Tag patterns: the app's version where the template says, numbers only,
// and * for anything (Plex's build hash).
func TestTagPattern(t *testing.T) {
	for _, c := range []struct {
		tmpl, app, tag, want string
	}{
		{"v{version}", "2.1.0", "v2.1.1", "2.1.1"},
		{"v{version}", "2.1.0", "v2.1.1-cuda", ""},
		{"{version}-rootless", "15.0.9", "15.0.10-rootless", "15.0.10"},
		{"{version}-rootless", "15.0.9", "15.1-rootless", ""},
		{"{version}-*", "1.43.4.10903", "1.43.5.11001-abc1234", "1.43.5.11001"},
		{"{version}-*", "1.43.4.10903", "1.43.5.11001-abc1234-arm64", ""},
		{"{version}", "261007", "261020", "261020"},
		{"{version}", "261007", "latest", ""},
		{"{version}", "261007", "20220121", ""},
	} {
		m := tagPattern(c.tmpl, c.app).FindStringSubmatch(c.tag)
		got := ""
		if m != nil {
			got = m[1]
		}
		if got != c.want {
			t.Errorf("%s for %s: %q, want %q", c.tmpl, c.tag, got, c.want)
		}
	}
}

// A package whose newest pin is gone, with no tag to find instead, is
// skipped with the registry's reason.
func TestGone(t *testing.T) {
	regs := fakeRegs{tags: map[string][]string{vwRepo: {"1.34.3"}}, digests: map[string]string{}}
	rep, err := Check(Options{Repo: repo(t, "", "vaultwarden")}, regs)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0].Reason, imagecheck.ErrGone.Error()) {
		t.Errorf("report %+v", rep)
	}
}
