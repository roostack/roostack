package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/pkgtest"
	"roostack.cloud/roostack/internal/state"
)

// testRegistry is a registry repository made from the test fixtures, its
// keys, and a server publishing what Build writes.
type testRegistry struct {
	t       *testing.T
	repo    string // the repository checkout
	site    string // what CI publishes
	root    ed25519.PrivateKey
	signing ed25519.PrivateKey
	srv     *httptest.Server
	now     time.Time
	// platforms are an image's builds, as the image registries would say
	// (nil: amd64 and arm64 for every image).
	platforms func(image string) []string
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	k, err := ParsePrivate(priv)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newTestRegistry(t *testing.T) *testRegistry {
	t.Helper()
	r := &testRegistry{t: t, repo: t.TempDir(), site: t.TempDir(), root: newKey(t), signing: newKey(t),
		now: time.Date(2026, 10, 14, 5, 0, 0, 0, time.UTC)}
	if err := os.CopyFS(filepath.Join(r.repo, "packages"), os.DirFS(pkgtest.Dir())); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(r.repo, "packages", "README.md"))
	r.delegate(1, r.signing)
	r.srv = httptest.NewServer(http.FileServer(http.Dir(r.site)))
	t.Cleanup(r.srv.Close)
	return r
}

// delegate writes keys/keys.json naming the signing keys, signed by the
// root key, as roostack registry delegate does.
func (r *testRegistry) delegate(serial int64, signing ...ed25519.PrivateKey) {
	r.t.Helper()
	data, sig, err := Delegate(serial, r.now, publics(signing), r.root)
	if err != nil {
		r.t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(r.repo, "keys"), 0o755)
	os.WriteFile(filepath.Join(r.repo, "keys", KeysFile), data, 0o644)
	os.WriteFile(filepath.Join(r.repo, "keys", KeysSigFile), sig, 0o644)
}

func publics(keys []ed25519.PrivateKey) []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, k := range keys {
		out = append(out, k.Public().(ed25519.PublicKey))
	}
	return out
}

// build runs CI: builds and signs the index an hour after the last one.
func (r *testRegistry) build() *Index {
	r.t.Helper()
	ix, err := r.tryBuild(r.signing)
	if err != nil {
		r.t.Fatal(err)
	}
	return ix
}

func (r *testRegistry) tryBuild(key ed25519.PrivateKey) (*Index, error) {
	r.now = r.now.Add(time.Hour)
	return Build(BuildOptions{Repo: r.repo, Out: r.site, Key: key, Roots: publics([]ed25519.PrivateKey{r.root}),
		Commit: "abc123", Now: r.now,
		Platforms: func(image string) ([]string, error) {
			if r.platforms != nil {
				return r.platforms(image), nil
			}
			return []string{"linux/amd64", "linux/arm64"}, nil
		}})
}

func (r *testRegistry) write(path, content string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.repo, path), []byte(content), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRegistry) fetcher(dir string) *Fetcher {
	return &Fetcher{URL: r.srv.URL + "/", Dir: dir, Roots: publics([]ed25519.PrivateKey{r.root}), Version: "v0.9.0",
		Arches: func() []string { return []string{"amd64"} }}
}

// A lab fetches a signed index and the files it needs, which the catalog
// then reads.
func TestFetch(t *testing.T) {
	r := newTestRegistry(t)
	ix := r.build()
	if len(ix.Packages) != 3 || ix.Packages["vaultwarden"].Tier != TierOfficial ||
		len(ix.Packages["vaultwarden"].Versions["1.34.3-roostack.1"].Architectures) != 2 {
		t.Fatalf("index: %+v", ix.Packages["vaultwarden"])
	}
	dir := t.TempDir()
	f := r.fetcher(dir)
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := f.Status()
	if st.Error != "" || st.Packages != 3 || st.Serial != ix.Serial || st.Commit != "abc123" || st.Fetched.IsZero() {
		t.Fatalf("status: %+v", st)
	}
	paths := config.Paths{CatalogDir: dir}
	p, err := state.LoadCatalogPackage(paths, "vaultwarden")
	if err != nil || p.Version != "1.34.3-roostack.1" {
		t.Fatalf("catalog: %v %v", p, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vaultwarden", "icon.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "vaultwarden", "registry.yaml")); err == nil {
		t.Error("registry.yaml was fetched: the index carries its facts")
	}
	// Again, unchanged: fine.
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A newer version replaces the older one; a withdrawn one is never
// offered; a whole package withdrawn leaves the catalog.
func TestFetchVersionsAndWithdrawals(t *testing.T) {
	r := newTestRegistry(t)
	old := string(pkgtest.Read(t, "vaultwarden"))
	r.write("packages/vaultwarden/1.34.4-roostack.1.yaml", strings.Replace(old, "version: 1.34.3-roostack.1", "version: 1.34.4-roostack.1", 1))
	r.build()
	dir := t.TempDir()
	f := r.fetcher(dir)
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, _ := state.CatalogVersion(config.Paths{CatalogDir: dir}, "vaultwarden"); v != "1.34.4-roostack.1" {
		t.Fatalf("offered %q", v)
	}
	if _, err := os.Stat(filepath.Join(dir, "vaultwarden", "1.34.3-roostack.1.yaml")); err == nil {
		t.Error("the older version is kept too")
	}

	r.write("packages/vaultwarden/registry.yaml", "tier: official\nmaintainers: [roostack]\nwithdrawals:\n"+
		"  - version: 1.34.4-roostack.1\n    level: warning\n    reason: It loses data.\n")
	r.write("packages/immich/registry.yaml", "tier: official\nmaintainers: [roostack]\nwithdrawals:\n"+
		"  - level: stop\n    reason: Malicious.\n")
	r.build()
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, _ := state.CatalogVersion(config.Paths{CatalogDir: dir}, "vaultwarden"); v != "1.34.3-roostack.1" {
		t.Errorf("offered %q after the newer was withdrawn", v)
	}
	if _, err := os.Stat(filepath.Join(dir, "immich")); err == nil {
		t.Error("a withdrawn package is still in the catalog")
	}
	if st := f.Status(); !strings.Contains(st.Skipped["immich"], "Malicious") {
		t.Errorf("skipped: %v", st.Skipped)
	}
}

// A version needing a newer Roostack, or another processor, isn't
// offered.
func TestFetchCompatibility(t *testing.T) {
	r := newTestRegistry(t)
	r.build()
	dir := t.TempDir()
	f := r.fetcher(dir)
	f.Version = "v0.0.1"
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := f.Status(); st.Packages != 0 || !strings.Contains(st.Skipped["forgejo"], "needs Roostack") {
		t.Errorf("status: %+v", st)
	}
	// The catalog still lists it, with its logo, as needing a newer
	// Roostack; its package file isn't fetched.
	listing, err := f.Listing()
	if l := listing["forgejo"]; err != nil || l.Package == nil || l.NeedsRoostack == "" || !l.Icon || l.Title != "Forgejo" {
		t.Errorf("listing: %+v, %v", l, err)
	}
	if v, err := state.CatalogVersion(config.Paths{CatalogDir: dir}, "forgejo"); err == nil {
		t.Errorf("offered %s", v)
	}
	f.Version, f.Arches = "dev", func() []string { return []string{"riscv64"} }
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := f.Status(); st.Packages != 0 || !strings.Contains(st.Skipped["forgejo"], "no build for riscv64") {
		t.Errorf("status: %+v", st)
	}
	if listing, _ := f.Listing(); listing["forgejo"].NeedsRoostack != "" || listing["forgejo"].Icon {
		t.Errorf("no build for riscv64: listed %+v", listing["forgejo"])
	}
}

// Anything not signed by the right keys, or not what the index says, is
// refused and leaves the catalog as it was.
func TestFetchRefuses(t *testing.T) {
	r := newTestRegistry(t)
	r.build()
	dir := t.TempDir()
	f := r.fetcher(dir)
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	good := f.Status()

	expect := func(what, want string) {
		t.Helper()
		err := f.Fetch(context.Background())
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", what, err, want)
		}
		if st := f.Status(); st.Error == "" {
			t.Errorf("%s: the error isn't recorded", what)
		}
	}

	// An index by a key keys.json doesn't name: CI won't build it, and a
	// lab won't take one signed that way.
	stranger := newKey(t)
	if _, err := r.tryBuild(stranger); err == nil {
		t.Error("built an index with a key keys.json doesn't name")
	}
	data, _ := os.ReadFile(filepath.Join(r.site, IndexFile))
	os.WriteFile(filepath.Join(r.site, IndexSigFile), Sign(data, stranger), 0o644)
	expect("unknown signing key", "not signed by a key Roostack trusts")

	// A changed index, with the old signature.
	r.build()
	data, _ = os.ReadFile(filepath.Join(r.site, IndexFile))
	os.WriteFile(filepath.Join(r.site, IndexFile), []byte(strings.Replace(string(data), "official", "community", 1)), 0o644)
	expect("changed index", "not signed")

	// A package file that isn't the one the index names.
	r.build()
	pkg := filepath.Join(r.site, "packages", "forgejo", "15.0.9-roostack.1.yaml")
	data, _ = os.ReadFile(pkg)
	os.WriteFile(pkg, append(data, []byte("# changed\n")...), 0o644)
	os.RemoveAll(filepath.Join(dir, "forgejo"))
	expect("changed package", "forgejo 15.0.9-roostack.1")
	if _, err := os.Stat(filepath.Join(dir, "forgejo", "15.0.9-roostack.1.yaml")); err == nil {
		t.Error("a package that failed its hash is in the catalog")
	}

	// Building again, the lab takes the newest index.
	r.build()
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	newest := f.Status()
	if newest.Serial <= good.Serial {
		t.Fatalf("serial %d after %d", newest.Serial, good.Serial)
	}

	// An older index (a replay or a frozen mirror) is refused.
	r.now = r.now.Add(-48 * time.Hour)
	r.build()
	expect("older index", "older than")
}

// A new signing key, named by a newer statement, replaces the old one for
// good: an older statement naming the old key is refused afterwards.
func TestKeyRotation(t *testing.T) {
	r := newTestRegistry(t)
	r.build()
	oldKeys, _ := os.ReadFile(filepath.Join(r.site, KeysFile))
	oldSig, _ := os.ReadFile(filepath.Join(r.site, KeysSigFile))
	dir := t.TempDir()
	f := r.fetcher(dir)
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}

	leaked := r.signing
	r.signing = newKey(t)
	r.delegate(2, r.signing)
	r.build()
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := f.Status(); st.KeysSerial != 2 {
		t.Fatalf("keys serial %d", st.KeysSerial)
	}

	// Someone with the leaked key serves the old statement and an index
	// signed with it.
	r.now = r.now.Add(time.Hour)
	os.WriteFile(filepath.Join(r.site, KeysFile), oldKeys, 0o644)
	os.WriteFile(filepath.Join(r.site, KeysSigFile), oldSig, 0o644)
	data, _ := os.ReadFile(filepath.Join(r.site, IndexFile))
	os.WriteFile(filepath.Join(r.site, IndexSigFile), Sign(data, leaked), 0o644)
	err := f.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "older than the 2") {
		t.Errorf("old statement: %v", err)
	}
}

// A statement must be signed by a root key built into Roostack.
func TestKeysNeedRoot(t *testing.T) {
	r := newTestRegistry(t)
	r.build()
	f := r.fetcher(t.TempDir())
	f.Roots = publics([]ed25519.PrivateKey{newKey(t)})
	if err := f.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "keys.json: not signed") {
		t.Errorf("got %v", err)
	}
	f.Roots = nil
	if err := f.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "no registry root key") {
		t.Errorf("got %v", err)
	}
}

// CI refuses to publish a registry with a broken package.
func TestBuildRefusesBrokenPackage(t *testing.T) {
	r := newTestRegistry(t)
	r.write("packages/forgejo/registry.yaml", "tier: official\nmaintainers: [roostack]\nwithdrawals:\n  - version: 9.9.9\n    level: notice\n    reason: x\n")
	if _, err := r.tryBuild(r.signing); err == nil || !strings.Contains(err.Error(), "withdraws version 9.9.9") {
		t.Errorf("got %v", err)
	}
	r.write("packages/forgejo/registry.yaml", "tier: official\nmaintainers: [roostack]\n")
	r.write("packages/forgejo/16.0.0-roostack.1.yaml", string(pkgtest.Read(t, "forgejo")))
	if _, err := r.tryBuild(r.signing); err == nil || !strings.Contains(err.Error(), "name the file after its version") {
		t.Errorf("got %v", err)
	}
}

// The index's date is how stale a lab's catalog is.
func TestStale(t *testing.T) {
	now := time.Now()
	if !(Status{}).Stale(now) || (Status{Date: now.Add(-24 * time.Hour)}).Stale(now) || !(Status{Date: now.Add(-15 * 24 * time.Hour)}).Stale(now) {
		t.Error("Stale")
	}
}

// A pull request's check needs no keys, and writes nothing.
func TestBuildCheckOnly(t *testing.T) {
	r := newTestRegistry(t)
	os.RemoveAll(filepath.Join(r.repo, "keys"))
	out := t.TempDir()
	ix, err := Build(BuildOptions{Repo: r.repo, Out: out, Now: r.now})
	if err != nil || len(ix.Packages) != 3 {
		t.Fatalf("%v %v", ix, err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("wrote %d files", len(entries))
	}
}

// An unchanged index is one small request (If-None-Match), and the copy on
// disk is checked again against the current key statement.
func TestFetchUnchanged(t *testing.T) {
	r := newTestRegistry(t)
	r.build()
	var notModified int
	r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		data, err := os.ReadFile(filepath.Join(r.site, filepath.FromSlash(req.URL.Path)))
		if err != nil {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("ETag", `"`+sum(data)+`"`)
		if req.Header.Get("If-None-Match") == `"`+sum(data)+`"` {
			notModified++
		}
		http.ServeContent(w, req, "", time.Time{}, bytes.NewReader(data))
	})
	dir := t.TempDir()
	f := r.fetcher(dir)
	for range 2 {
		if err := f.Fetch(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if notModified != 1 || f.Status().ETag == "" {
		t.Errorf("%d unchanged answers; status %+v", notModified, f.Status())
	}
	// The signing key replaced, the index not yet rebuilt: the copy the
	// lab has is no longer signed by a key it trusts.
	r.signing = newKey(t)
	r.delegate(2, r.signing)
	os.WriteFile(filepath.Join(r.site, KeysFile), mustRead(t, filepath.Join(r.repo, "keys", KeysFile)), 0o644)
	os.WriteFile(filepath.Join(r.site, KeysSigFile), mustRead(t, filepath.Join(r.repo, "keys", KeysSigFile)), 0o644)
	if err := f.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "index.json: not signed") {
		t.Errorf("got %v", err)
	}
	if notModified != 2 {
		t.Errorf("%d unchanged answers", notModified)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Opening the catalog doesn't wait behind a fetch that's running, and
// doesn't fetch again soon after one.
func TestFetchIfOlder(t *testing.T) {
	r := newTestRegistry(t)
	r.build()
	f := r.fetcher(t.TempDir())
	f.mu.Lock()
	if err := f.FetchIfOlder(context.Background(), 0); err != nil || !f.Status().Checked.IsZero() {
		t.Errorf("fetched while another fetch held the lock: %v", err)
	}
	f.mu.Unlock()
	if err := f.FetchIfOlder(context.Background(), time.Hour); err != nil || f.Status().Checked.IsZero() {
		t.Fatalf("first fetch: %v", err)
	}
	checked := f.Status().Checked
	if err := f.FetchIfOlder(context.Background(), time.Hour); err != nil || !f.Status().Checked.Equal(checked) {
		t.Errorf("fetched again within the hour: %v", err)
	}
}

// A withdrawal reaches the lab's copy of a version only if it's the file
// the index names; the package's and the version's, the more severe wins.
func TestWithdrawn(t *testing.T) {
	file := []byte("name: forgejo\n")
	ix := &Index{Packages: map[string]*Package{"forgejo": {Versions: map[string]*Version{
		"1.0": {File: File{SHA256: sum(file)}, Withdrawn: &Withdrawal{Level: LevelNotice, Reason: "n"}},
		"2.0": {File: File{SHA256: sum(file)}}}}}}
	if w := ix.Withdrawn("forgejo", "1.0", file); w == nil || w.Level != LevelNotice {
		t.Errorf("version: %+v", w)
	}
	if w := ix.Withdrawn("forgejo", "2.0", file); w != nil {
		t.Errorf("not withdrawn: %+v", w)
	}
	ix.Packages["forgejo"].Withdrawn = &Withdrawal{Level: LevelStop, Reason: "s"}
	if w := ix.Withdrawn("forgejo", "1.0", file); w == nil || w.Level != LevelStop {
		t.Errorf("package's stop wins: %+v", w)
	}
	if w := ix.Withdrawn("forgejo", "1.0", []byte("another file")); w != nil {
		t.Errorf("a file of your own: %+v", w)
	}
	if w := ix.Withdrawn("forgejo", "3.0", file); w != nil || ix.Withdrawn("immich", "1.0", file) != nil {
		t.Error("unknown versions and packages")
	}
}

// TestFetchMixedLab: in a lab mixing processors, a package with a build
// for only one of them is still kept, for the servers it can run on
// (installing checks the server chosen); a lab gaining a processor
// fetches again (SPEC.md §8.3.3).
func TestFetchMixedLab(t *testing.T) {
	r := newTestRegistry(t)
	r.platforms = func(image string) []string {
		if strings.Contains(image, "vaultwarden") {
			return []string{"linux/amd64"}
		}
		return []string{"linux/amd64", "linux/arm64"}
	}
	ix := r.build()
	if a := ix.Architectures("vaultwarden", "1.34.3-roostack.1"); !slices.Equal(a, []string{"amd64"}) {
		t.Fatalf("vaultwarden's architectures: %v", a)
	}
	if a := ix.Architectures("vaultwarden", "9.9.9"); a != nil {
		t.Errorf("a version the index doesn't have: %v", a)
	}
	dir := t.TempDir()
	f := r.fetcher(dir)
	arches := []string{"arm64"} // a lab of Pis
	f.Arches = func() []string { return arches }
	if err := f.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := f.Status(); !strings.Contains(st.Skipped["vaultwarden"], "no build for arm64") || !slices.Equal(st.Arches, []string{"arm64"}) {
		t.Errorf("only arm64: %+v", st)
	}
	// A PC joins: Vaultwarden is offered, for it. The catalog page's
	// check fetches again at once, though the last fetch was just now.
	arches = []string{"arm64", "amd64", "arm64", ""}
	if err := f.FetchIfOlder(context.Background(), time.Hour); err != nil {
		t.Fatal(err)
	}
	if v, _ := state.CatalogVersion(config.Paths{CatalogDir: dir}, "vaultwarden"); v != "1.34.3-roostack.1" {
		t.Errorf("a lab with a PC: offered %q", v)
	}
	if st := f.Status(); st.Skipped["vaultwarden"] != "" || !slices.Equal(st.Arches, []string{"amd64", "arm64"}) {
		t.Errorf("amd64 and arm64: %+v", st)
	}
	checked := f.Status().Checked
	if err := f.FetchIfOlder(context.Background(), time.Hour); err != nil || !f.Status().Checked.Equal(checked) {
		t.Errorf("nothing changed: fetched again (%v)", err)
	}
}

// Offer keeps the newest version with a build for any of the lab's
// processors.
func TestOfferArches(t *testing.T) {
	p := &Package{Versions: map[string]*Version{
		"1.0.0-roostack.1": {Architectures: []string{"amd64", "arm64"}},
		"1.1.0-roostack.1": {Architectures: []string{"amd64"}},
	}}
	for _, c := range []struct {
		arches []string
		want   string
	}{{[]string{"arm64"}, "1.0.0-roostack.1"}, {[]string{"amd64"}, "1.1.0-roostack.1"},
		{[]string{"amd64", "arm64"}, "1.1.0-roostack.1"}, {[]string{"riscv64"}, ""}} {
		if v, _, _ := p.Offer("dev", c.arches...); v != c.want {
			t.Errorf("%v: offered %q, want %q", c.arches, v, c.want)
		}
	}
}
