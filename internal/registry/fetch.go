package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/pkgspec"
)

// DefaultURL is the registry's address (SPEC.md §8.3.3, the Owner
// 2026-10-14): GitHub Pages behind a name of ours, so the host can change
// without a release. The lab's registry.url setting replaces it (a
// mirror).
const DefaultURL = "https://registry.roostack.cloud/v1/"

// SourceRepo is the registry repository the default registry is built
// from, for links to the commit an index names.
const SourceRepo = "https://github.com/roostack/services"

// Limits on what a lab downloads, so a bad mirror can't fill the disk.
const (
	maxIndex = 16 << 20 // index.json; about 1 KB a package version
	maxFile  = 1 << 20  // a package file or an icon (icons: at most 100 KB)
	maxSmall = 64 << 10 // keys.json and the signatures
)

// StaleAfter is how old the newest index a lab has may get before it's
// shown as a warning (the Owner, 2026-10-14). CI signs a new one every
// day, so an older one means the lab can't reach the registry, CI is
// broken, or someone is holding back newer indexes (a "freeze"). It
// warns rather than refusing to install.
const StaleAfter = 14 * 24 * time.Hour

// statusFile is the fetcher's record, beside what it fetched.
const statusFile = "status.json"

// IconFile is a package's logo in its catalog folder, whatever its
// package calls it (the index names the registry's packages/<name>/icon.png).
const IconFile = "icon.png"

// Fetcher keeps the controller's copy of the registry (SPEC.md §8.3.3):
// the index, the key statement, and for each package the version this
// lab would install and its icon, at Dir/<name>/<version>.yaml and
// Dir/<name>/icon.png, where the catalog reads them
// (state.LoadCatalogPackage). Fetches run one at a time.
type Fetcher struct {
	URL string // the registry's address, ending in /
	Dir string // config.Paths.CatalogDir
	// Roots are the root keys (RootKeys).
	Roots []ed25519.PublicKey
	// Version is the running Roostack's (main.version): only versions it
	// can run are kept.
	Version string
	// Arches are the lab's processors (GOARCH: the controller's and every
	// other server's it knows): a package's newest version with a build
	// for one of them is kept. In a lab mixing processors that may not run
	// on every server; installing checks the one chosen (SPEC.md §8.3.3).
	Arches func() []string
	Client *http.Client
	Log    *slog.Logger
	// Fetched, if set, is called after each fetch, whether it worked or
	// not: the manager checks whether a withdrawal changed what should
	// run (SPEC.md §8.3.4).
	Fetched func()

	mu sync.Mutex
	// last is the status the last fetch recorded, kept here too, so its
	// error shows even when the file can't be written (a catalog folder
	// the sandbox makes read-only); fetching is set while one runs.
	statusMu sync.Mutex
	last     *Status
	fetching bool
	// The parsed index (Index).
	ixMu sync.Mutex
	indexCache
}

// Status is what the last fetches did, for Settings → Updates.
type Status struct {
	URL     string    `json:"url"`
	Checked time.Time `json:"checked"` // the last try
	Fetched time.Time `json:"fetched"` // the last that worked
	// Error is why the last try failed; empty if it worked.
	Error string `json:"error,omitempty"`
	// From the newest index the lab has.
	Serial   int64     `json:"serial"`
	Date     time.Time `json:"date"`
	Commit   string    `json:"commit,omitempty"`
	Packages int       `json:"packages"`
	// KeysSerial is the newest key statement's serial seen.
	KeysSerial int64 `json:"keys_serial"`
	// Fetching: a fetch is running now. NoRootKey: this build has no
	// registry root key, so it can't check what it fetches (until the
	// key ceremony, docs/REGISTRY-KEYS.md). Neither is kept.
	Fetching  bool `json:"-"`
	NoRootKey bool `json:"-"`
	// ETag is the index's, to ask whether it changed.
	ETag string `json:"etag,omitempty"`
	// Skipped are packages with no version this lab can install, and why.
	Skipped map[string]string `json:"skipped,omitempty"`
	// Arches are the lab's processors the last fetch kept versions for: a
	// server with another one joining fetches again.
	Arches []string `json:"arches,omitempty"`
}

// arches are the lab's processors, sorted, each once.
func (f *Fetcher) arches() []string {
	var out []string
	if f.Arches != nil {
		for _, a := range f.Arches() {
			if a != "" && !slices.Contains(out, a) {
				out = append(out, a)
			}
		}
	}
	slices.Sort(out)
	return out
}

// Stale says whether the newest index the lab has is older than
// StaleAfter, or there's none.
func (s Status) Stale(now time.Time) bool { return now.Sub(s.Date) > StaleAfter }

// Status reads the record of the last fetches; zero before the first.
func (f *Fetcher) Status() Status {
	f.statusMu.Lock()
	last, fetching := f.last, f.fetching
	f.statusMu.Unlock()
	var s Status
	if last != nil {
		s = *last
	} else {
		data, err := os.ReadFile(filepath.Join(f.Dir, statusFile))
		if err == nil {
			err = json.Unmarshal(data, &s)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) && f.Log != nil {
			f.Log.Error("reading the registry fetch status", "err", err)
		}
	}
	s.Fetching, s.NoRootKey = fetching, len(f.Roots) == 0
	return s
}

// Listed is what the catalog shows of a package from the index, beside
// what its package file says (SPEC.md §8.3.6).
type Listed struct {
	*Package
	// NeedsRoostack is the Roostack release it needs, when no version runs
	// on this one (Package.NeedsRoostack).
	NeedsRoostack string
	// Icon says its logo is in its catalog folder (IconFile).
	Icon bool
}

// Listing reads the newest index the lab has, for the catalog: every
// package in it, by name. Nil before the first fetch. The index was
// checked against its signature before it was written here.
func (f *Fetcher) Listing() (map[string]Listed, error) {
	data, err := os.ReadFile(filepath.Join(f.Dir, IndexFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, fmt.Errorf("%s: %w", IndexFile, err)
	}
	listing := make(map[string]Listed, len(ix.Packages))
	arches := f.arches()
	for name, p := range ix.Packages {
		if !pkgspec.ValidName(name) || p == nil {
			continue // Check refused such an index; this one is from disk
		}
		l := Listed{Package: p, NeedsRoostack: p.NeedsRoostack(f.Version, arches...)}
		if _, err := os.Stat(filepath.Join(f.Dir, name, IconFile)); err == nil {
			l.Icon = true
		}
		listing[name] = l
	}
	return listing, nil
}

// FetchIfOlder fetches unless the last try was less than age ago, or a
// fetch is running already (its result will do): for the catalog page,
// which checks when it's opened and mustn't wait behind a slow fetch. A
// lab whose processors changed since (a Pi joined) fetches anyway.
func (f *Fetcher) FetchIfOlder(ctx context.Context, age time.Duration) error {
	if !f.mu.TryLock() {
		return nil
	}
	defer f.mu.Unlock()
	if st := f.Status(); time.Since(st.Checked) < age && slices.Equal(st.Arches, f.arches()) {
		return nil
	}
	return f.fetchLocked(ctx)
}

// Fetch checks the registry for a newer index and brings the catalog up to
// date with it. On failure the catalog stays as it was, and the error is
// recorded in Status.
func (f *Fetcher) Fetch(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetchLocked(ctx)
}

func (f *Fetcher) fetchLocked(ctx context.Context) error {
	f.statusMu.Lock()
	f.fetching = true
	f.statusMu.Unlock()
	defer func() {
		f.statusMu.Lock()
		f.fetching = false
		f.statusMu.Unlock()
	}()
	st := f.Status()
	if st.URL != f.URL {
		// Another address: its ETag means nothing here. Serials stay, so
		// a mirror can't serve an older index either.
		st.ETag = ""
	}
	st.URL, st.Checked = f.URL, time.Now()
	err := f.fetch(ctx, &st)
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Error, st.Fetched = "", st.Checked
	}
	if werr := f.writeStatus(st); werr != nil {
		err = errors.Join(err, werr)
		st.Error = err.Error()
	}
	f.statusMu.Lock()
	f.last = &st
	f.statusMu.Unlock()
	if f.Fetched != nil {
		f.Fetched()
	}
	return err
}

func (f *Fetcher) fetch(ctx context.Context, st *Status) error {
	if len(f.Roots) == 0 {
		return errors.New("this build of Roostack has no registry root key, so it can't check the registry's signatures")
	}
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		return err
	}
	// The key statement, by a root key.
	keysData, _, err := f.get(ctx, KeysFile, maxSmall, "")
	if err != nil {
		return err
	}
	keysSig, _, err := f.get(ctx, KeysSigFile, maxSmall, "")
	if err != nil {
		return err
	}
	statement, err := ParseKeys(keysData, keysSig, f.Roots)
	if err != nil {
		return err
	}
	if statement.Serial < st.KeysSerial {
		return fmt.Errorf("%s has serial %d, older than the %d this lab has seen: refused, in case it names a key that's since been replaced", KeysFile, statement.Serial, st.KeysSerial)
	}
	signing, _ := statement.Keys()

	// The index, by a signing key the statement names. Unchanged (304),
	// the copy on disk is checked again: the signing key may have changed.
	etag := st.ETag
	if _, err := os.Stat(filepath.Join(f.Dir, IndexFile)); err != nil {
		etag = ""
	}
	indexData, newTag, err := f.get(ctx, IndexFile, maxIndex, etag)
	if err != nil {
		return err
	}
	var indexSig []byte
	if indexData == nil {
		if indexData, err = os.ReadFile(filepath.Join(f.Dir, IndexFile)); err == nil {
			indexSig, err = os.ReadFile(filepath.Join(f.Dir, IndexSigFile))
		}
		if err != nil {
			return err
		}
	} else if indexSig, _, err = f.get(ctx, IndexSigFile, maxSmall, ""); err != nil {
		return err
	}
	ix, err := ParseIndex(indexData, indexSig, signing)
	if err != nil {
		return err
	}
	if ix.Serial < st.Serial {
		return fmt.Errorf("%s has serial %d, older than the %d this lab has: refused (an old index may hide withdrawals)", IndexFile, ix.Serial, st.Serial)
	}
	if ix.Serial == st.Serial && st.Serial != 0 {
		old, _ := os.ReadFile(filepath.Join(f.Dir, IndexFile))
		if !bytes.Equal(old, indexData) {
			return fmt.Errorf("%s has the serial of the one this lab has (%d) but different contents: refused", IndexFile, ix.Serial)
		}
	}

	// The package files this lab would install, and their icons.
	keep := map[string]map[string]bool{} // package → file names
	offered := 0
	arches := f.arches()
	skipped := map[string]string{}
	var errs []error
	for name, p := range ix.Packages {
		version, v, ok := p.Offer(f.Version, arches...)
		if !ok {
			skipped[name] = whyNotOffered(p, f.Version, arches)
			// One that needs a newer Roostack is still listed in the
			// catalog, with its logo (SPEC.md §8.3.6).
			if p.Icon != nil && p.NeedsRoostack(f.Version, arches...) != "" {
				if err := f.fetchFile(ctx, name, IconFile, *p.Icon, pkgspec.CheckIcon); err != nil {
					errs = append(errs, fmt.Errorf("%s icon: %w", name, err))
				} else {
					keep[name] = map[string]bool{IconFile: true}
				}
			}
			continue
		}
		offered++
		file := version + ".yaml"
		if err := f.fetchFile(ctx, name, file, v.File, func(data []byte) error {
			pkg, err := pkgspec.Parse(data)
			if err != nil {
				return err
			}
			if pkg.Name != name || pkg.Version != version {
				return fmt.Errorf("it's %s %s", pkg.Name, pkg.Version)
			}
			return nil
		}); err != nil {
			errs = append(errs, fmt.Errorf("%s %s: %w", name, version, err))
			continue
		}
		keep[name] = map[string]bool{file: true}
		if p.Icon != nil {
			if err := f.fetchFile(ctx, name, IconFile, *p.Icon, pkgspec.CheckIcon); err != nil {
				// The package still installs without its logo.
				errs = append(errs, fmt.Errorf("%s icon: %w", name, err))
			} else {
				keep[name][IconFile] = true
			}
		}
	}

	// The index last, once its files are in place; then what it no longer
	// offers goes (other versions, withdrawn ones, removed packages).
	for _, w := range []struct {
		name string
		data []byte
	}{{KeysFile, keysData}, {KeysSigFile, keysSig}, {IndexFile, indexData}, {IndexSigFile, indexSig}} {
		if err := writeFile(filepath.Join(f.Dir, w.name), w.data); err != nil {
			return err
		}
	}
	if err := f.prune(keep); err != nil {
		errs = append(errs, err)
	}
	st.Serial, st.Date, st.Commit, st.Packages = ix.Serial, ix.Date, ix.Commit, offered
	st.KeysSerial, st.ETag, st.Skipped, st.Arches = statement.Serial, newTag, skipped, arches
	return errors.Join(errs...)
}

// whyNotOffered says, for Settings, why a package has no version this lab
// can install.
func whyNotOffered(p *Package, roostack string, arches []string) string {
	if p.Withdrawn.Blocks() {
		return "withdrawn: " + p.Withdrawn.Reason
	}
	// The newest version's reason.
	newest := ""
	for version := range p.Versions {
		if newest == "" || pkgspec.CompareVersions(version, newest) > 0 {
			newest = version
		}
	}
	switch v := p.Versions[newest]; {
	case v == nil:
		return "no versions"
	case v.Withdrawn.Blocks():
		return newest + " is withdrawn: " + v.Withdrawn.Reason
	case !pkgspec.RunsOn(v.RequiresRoostack, roostack):
		return newest + " needs Roostack " + v.RequiresRoostack + " or newer"
	default:
		return newest + " has no build for " + strings.Join(arches, " or ")
	}
}

// fetchFile makes Dir/<name>/<file> the file the index vouches for,
// downloading it unless the lab already has it, and checking it with
// check before it's used.
func (f *Fetcher) fetchFile(ctx context.Context, name, file string, want File, check func([]byte) error) error {
	dst := filepath.Join(f.Dir, name, file)
	if have, err := os.ReadFile(dst); err == nil && sum(have) == want.SHA256 {
		return nil
	}
	data, _, err := f.get(ctx, want.Path, want.Size, "")
	if err != nil {
		return err
	}
	if int64(len(data)) != want.Size || sum(data) != want.SHA256 {
		return fmt.Errorf("%s isn't the file the index names (its SHA-256 differs)", want.Path)
	}
	if err := check(data); err != nil {
		return fmt.Errorf("%s: %w", want.Path, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return writeFile(dst, data)
}

// prune removes package folders and files the index no longer offers.
func (f *Fetcher) prune(keep map[string]map[string]bool) error {
	dirs, err := os.ReadDir(f.Dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(f.Dir, d.Name())
		files, ok := keep[d.Name()]
		if !ok {
			errs = append(errs, os.RemoveAll(dir))
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, e := range entries {
			if !files[e.Name()] {
				errs = append(errs, os.RemoveAll(filepath.Join(dir, e.Name())))
			}
		}
	}
	return errors.Join(errs...)
}

// get downloads a file from the registry, at most limit bytes. With an
// etag, an unchanged file returns nil data.
func (f *Fetcher) get(ctx context.Context, path string, limit int64, etag string) ([]byte, string, error) {
	u := strings.TrimSuffix(f.URL, "/") + "/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && etag != "":
		return nil, etag, nil
	case resp.StatusCode != http.StatusOK:
		return nil, "", fmt.Errorf("%s: the registry answered %s", u, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", u, err)
	}
	if int64(len(data)) > limit {
		return nil, "", fmt.Errorf("%s is larger than %d bytes", u, limit)
	}
	return data, resp.Header.Get("ETag"), nil
}

func (f *Fetcher) writeStatus(st Status) error {
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(f.Dir, statusFile), append(data, '\n'))
}

// writeFile replaces a file in one step, so the catalog never reads half
// of one. Not secret: 0644, so people can read what the lab fetched.
func writeFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func sum(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}
