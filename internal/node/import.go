package node

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/importer"
	"roostack.cloud/roostack/internal/state"
)

// Bringing your own data into a service (SPEC.md §8.4): the server the
// service runs on mounts the person's copy read-only, keeps what they
// said about it (where it is, the old setup, where its folders are now,
// what they chose), checks it, and copies it into the service's folders'
// staging copies while everything runs. The controller drives the switch
// (manager/import.go) through SwitchImport's steps, holding this server's
// maintenance queue while it does. Its rules are internal/importer's.

// Imports is bringing data into a server's services.
type Imports interface {
	// Import is what's known about bringing data into a service, with
	// what goes where worked out afresh.
	Import(ctx context.Context, service string) (ImportView, error)
	// SetImportSource mounts the person's copy, read-only.
	SetImportSource(ctx context.Context, service string, src ImportSource) error
	// SetImportSetup keeps the old setup (nil forgets it), guessing where
	// its folders are now; SetImportPrefixes says so; SetImportChoices
	// keeps what the person chose by hand.
	SetImportSetup(ctx context.Context, service string, setup *importer.Setup) error
	SetImportPrefixes(ctx context.Context, service string, prefixes []importer.Prefix) error
	SetImportChoices(ctx context.Context, service string, ch importer.Choices) error
	// BrowseImport lists a folder of the person's copy.
	BrowseImport(ctx context.Context, service, rel string) (ImportListing, error)
	// CheckImport and CopyImport start the check and the copy in the
	// background (Import says how they're going); StopImportCopy stops
	// the copy, which carries on from there next time.
	CheckImport(ctx context.Context, service string) error
	CopyImport(ctx context.Context, service string) error
	StopImportCopy(ctx context.Context, service string) error
	// SwitchImport starts one step of the switch (SwitchStop…) in the
	// background; Import's Step says how it went, and what it set aside.
	SwitchImport(ctx context.Context, service, step string) error
	// CancelImport stops, unmounts and forgets the import, and deletes
	// what was copied.
	CancelImport(ctx context.Context, service string) error
}

// Steps of the switch, in order (SwitchImport).
const (
	SwitchStop   = "stop"   // stop the service's units
	SwitchCopy   = "copy"   // copy what changed, once more
	SwitchCommit = "commit" // the copy in place, its data set aside
	SwitchLoad   = "load"   // load the dumps (starting only the databases)
	SwitchStart  = "start"  // start what runs now
	SwitchUndo   = "undo"   // put the data set aside back
	SwitchFinish = "finish" // unmount the copy, and note the switch
)

// ImportAgent is what roostack-agent does for an import.
type ImportAgent interface {
	MountImport(ctx context.Context, service string, l agent.Library) (string, error)
	UnmountImport(ctx context.Context, service string) error
}

// ImportSource is where the person's copy is.
type ImportSource struct {
	// Kind is drive, folder or share; Library is its place, as a media
	// library's (a share's password only on its way to the agent).
	Kind    string        `json:"kind"`
	Library agent.Library `json:"library"`
	// Title says where, for people: "T7 Shield › immich".
	Title string `json:"title"`
}

// ImportState is what a server keeps about bringing data into a service,
// in its runtime settings, until the import is cancelled or done.
type ImportState struct {
	Source   *ImportSource     `json:"source,omitempty"`
	Setup    *importer.Setup   `json:"setup,omitempty"`
	Prefixes []importer.Prefix `json:"prefixes,omitempty"`
	// Guessed: Prefixes are Roostack's guess, not yet the person's.
	Guessed bool `json:"guessed,omitempty"`
	// SetupDone: the person went on from the old setup (where its folders
	// are now said, or no setup given).
	SetupDone bool             `json:"setup_done,omitempty"`
	Choices   importer.Choices `json:"choices,omitzero"`
	// Check is the last check; Checking while one runs, CheckErr if it
	// couldn't finish.
	Check    *importer.Report `json:"check,omitempty"`
	Checking bool             `json:"checking,omitempty"`
	CheckErr string           `json:"check_err,omitempty"`
	Copy     *ImportCopy      `json:"copy,omitempty"`
	// Step is the switch's last step on this server.
	Step *SwitchStep `json:"step,omitempty"`
	// Switched is when the data was switched in, Kept what it set aside
	// then, Brought what was brought (titles, for its page) and
	// BroughtLibraries the media libraries shown at old folders.
	Switched         time.Time          `json:"switched,omitzero"`
	Kept             []string           `json:"kept,omitempty"`
	Brought          []string           `json:"brought,omitempty"`
	BroughtLibraries []importer.Library `json:"brought_libraries,omitempty"`
}

// SwitchStep is a step of the switch, running or done.
type SwitchStep struct {
	Name    string `json:"name"`
	Running bool   `json:"running,omitempty"`
	Err     string `json:"err,omitempty"`
}

// ImportCopy is the copy into the service's staging folders.
type ImportCopy struct {
	Running  bool              `json:"running,omitempty"`
	Started  time.Time         `json:"started"`
	Finished time.Time         `json:"finished,omitzero"`
	Progress importer.Progress `json:"progress"`
	// Files and Bytes are all there is to copy (the check's count).
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
	// Err says why it stopped; Stopped: the person stopped it.
	Err     string `json:"err,omitempty"`
	Stopped bool   `json:"stopped,omitempty"`
	// Passes counts copies finished (a second copies only what changed).
	Passes int `json:"passes,omitempty"`
}

// ImportView is an import as its page shows it.
type ImportView struct {
	ImportState
	Plan importer.Plan `json:"plan"`
	// Mounted: the person's copy can be read now.
	Mounted bool `json:"mounted"`
}

// ImportListing is a folder of the person's copy.
type ImportListing struct {
	Path    string        `json:"path"`
	Folders []ImportEntry `json:"folders"`
	Files   []ImportEntry `json:"files"`
}

// ImportEntry is a file or folder in a listing.
type ImportEntry struct {
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
}

// Places, variables for tests.
var (
	importRoot  = config.ImportRoot
	libraryRoot = config.LibraryRoot
)

// imports are this server's imports in progress.
type imports struct {
	mu sync.Mutex
	// running cancels a service's check or copy.
	running map[string]context.CancelFunc
	// samples are file names old data names under an old container path
	// (service → path → names), for finding media libraries.
	samples map[string]map[string][]string
	resumed bool
}

// Imports is bringing data into this server's services; nil without an
// agent to mount the person's copy.
func (n *Local) Imports() Imports {
	if n.ImportAgent == nil || n.State == nil {
		return nil
	}
	return n
}

func importKey(service string) string { return "import." + service }

// loadImport reads a service's import (empty if none).
func (n *Local) loadImport(ctx context.Context, service string) ImportState {
	var st ImportState
	n.loadSetting(ctx, importKey(service), &st)
	return st
}

// updateImport changes a service's import and saves it.
func (n *Local) updateImport(ctx context.Context, service string, change func(*ImportState) error) error {
	n.imp.mu.Lock()
	defer n.imp.mu.Unlock()
	st := n.loadImport(ctx, service)
	if err := change(&st); err != nil {
		return err
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return n.State.SetSetting(ctx, importKey(service), string(b))
}

// importService is a service of this server's, for an import.
func (n *Local) importService(service string) (state.Service, error) {
	s, ok := n.service(service)
	if !ok {
		return s, ErrNotFound
	}
	return s, nil
}

// importFiles is the person's copy, mounted; nil when it isn't there.
func importFiles(service string, st ImportState) fs.FS {
	if st.Source == nil {
		return nil
	}
	dir := filepath.Join(importRoot, service)
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	return os.DirFS(dir)
}

func (n *Local) Import(ctx context.Context, service string) (ImportView, error) {
	s, err := n.importService(service)
	if err != nil {
		return ImportView{}, err
	}
	st := n.loadImport(ctx, service)
	v := ImportView{ImportState: st}
	files := importFiles(service, st)
	if files != nil {
		_, err := fs.ReadDir(files, ".")
		v.Mounted = err == nil
	}
	v.Plan = importer.Match(s.Package, st.Setup, st.Prefixes, st.Choices, files, n.libraryFinder(s, files))
	return v, nil
}

func (n *Local) SetImportSource(ctx context.Context, service string, src ImportSource) error {
	if _, err := n.importService(service); err != nil {
		return err
	}
	if n.importBusy(service) {
		return formErrorf("Wait for the check or copy to finish, or stop it, first.")
	}
	if !slices.Contains([]string{"drive", "folder", "share"}, src.Kind) {
		return formErrorf("Choose a drive, a folder or a share.")
	}
	if _, err := n.ImportAgent.MountImport(ctx, service, src.Library); err != nil {
		return formErrorf("Roostack couldn't open those files: %v.", strings.TrimSuffix(err.Error(), "."))
	}
	if src.Library.Share != nil {
		share := *src.Library.Share
		share.Password = "" // the agent keeps it, root's only
		src.Library.Share = &share
	}
	n.forgetSamples(service)
	return n.updateImport(ctx, service, func(st *ImportState) error {
		st.Source, st.Check = &src, nil
		if st.Setup != nil && (len(st.Prefixes) == 0 || st.Guessed) {
			n.guessPrefixes(service, st)
		}
		return nil
	})
}

// guessPrefixes guesses where the old folders are in the person's copy.
func (n *Local) guessPrefixes(service string, st *ImportState) {
	s, ok := n.service(service)
	if !ok {
		return
	}
	st.Prefixes = importer.GuessPrefixes(s.Package, st.Setup, nil, importFiles(service, *st))
	st.Guessed = len(st.Prefixes) > 0
}

func (n *Local) SetImportSetup(ctx context.Context, service string, setup *importer.Setup) error {
	if _, err := n.importService(service); err != nil {
		return err
	}
	if setup != nil {
		if err := setup.Clean(); err != nil {
			return formErrorf("%s.", capitalise(strings.TrimSuffix(err.Error(), ".")))
		}
	}
	n.forgetSamples(service)
	return n.updateImport(ctx, service, func(st *ImportState) error {
		st.Setup, st.Check, st.SetupDone = setup, nil, setup == nil
		if len(st.Prefixes) == 0 || st.Guessed {
			st.Prefixes, st.Guessed = nil, false
			if setup != nil {
				n.guessPrefixes(service, st)
			}
		}
		return nil
	})
}

func (n *Local) SetImportPrefixes(ctx context.Context, service string, prefixes []importer.Prefix) error {
	if _, err := n.importService(service); err != nil {
		return err
	}
	var keep []importer.Prefix
	for _, p := range prefixes {
		p.Old, p.Now = strings.TrimSpace(p.Old), strings.Trim(strings.TrimSpace(p.Now), "/")
		if p.Old == "" {
			continue
		}
		if p.Now == "" {
			p.Now = "."
		}
		if !fs.ValidPath(p.Now) || len(p.Old) > 4096 || strings.ContainsAny(p.Old, "\x00\n") {
			return formErrorf("%q isn't a folder Roostack can use.", p.Old)
		}
		keep = append(keep, p)
	}
	n.forgetSamples(service)
	return n.updateImport(ctx, service, func(st *ImportState) error {
		st.Prefixes, st.Guessed, st.Check, st.SetupDone = keep, false, nil, true
		return nil
	})
}

func (n *Local) SetImportChoices(ctx context.Context, service string, ch importer.Choices) error {
	s, err := n.importService(service)
	if err != nil {
		return err
	}
	for name, rel := range ch.Storage {
		if _, ok := s.Package.Storage[name]; !ok || (rel != importer.Leave && !fs.ValidPath(rel)) {
			return formErrorf("Choose a folder in your files for %s.", name)
		}
	}
	for name, rel := range ch.Dumps {
		if _, ok := s.Package.Databases[name]; !ok || (rel != importer.Leave && !fs.ValidPath(rel)) {
			return formErrorf("Choose a dump in your files for the database %s.", name)
		}
	}
	return n.updateImport(ctx, service, func(st *ImportState) error {
		st.Choices, st.Check = ch, nil
		return nil
	})
}

// maxListing caps a folder's listing.
const maxListing = 1000

func (n *Local) BrowseImport(ctx context.Context, service, rel string) (ImportListing, error) {
	if _, err := n.importService(service); err != nil {
		return ImportListing{}, err
	}
	files := importFiles(service, n.loadImport(ctx, service))
	rel = strings.Trim(rel, "/")
	if rel == "" {
		rel = "."
	}
	if files == nil || !fs.ValidPath(rel) {
		return ImportListing{}, ErrNotFound
	}
	entries, err := fs.ReadDir(files, rel)
	if err != nil {
		return ImportListing{}, formErrorf("Roostack can't open that folder: %v.", unwrapPathError(err))
	}
	l := ImportListing{Path: rel}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && e.Name() != ".env" || len(l.Folders)+len(l.Files) >= maxListing {
			continue
		}
		switch {
		case e.IsDir():
			l.Folders = append(l.Folders, ImportEntry{Name: e.Name()})
		case e.Type().IsRegular():
			ie := ImportEntry{Name: e.Name()}
			if info, err := e.Info(); err == nil {
				ie.Size = info.Size()
			}
			l.Files = append(l.Files, ie)
		}
	}
	return l, nil
}

// unwrapPathError keeps an error's reason, not its path.
func unwrapPathError(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// importBusy says whether a service's check or copy is running.
func (n *Local) importBusy(service string) bool {
	n.imp.mu.Lock()
	defer n.imp.mu.Unlock()
	return n.imp.running[service] != nil
}

// startImportRun runs a service's check or copy in the background, one at
// a time.
func (n *Local) startImportRun(service string, run func(ctx context.Context)) error {
	n.imp.mu.Lock()
	defer n.imp.mu.Unlock()
	if n.imp.running[service] != nil {
		return formErrorf("Wait for the check or copy to finish, or stop it, first.")
	}
	if n.imp.running == nil {
		n.imp.running = map[string]context.CancelFunc{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.imp.running[service] = cancel
	go func() {
		defer func() {
			n.imp.mu.Lock()
			delete(n.imp.running, service)
			n.imp.mu.Unlock()
			cancel()
		}()
		run(ctx)
	}()
	return nil
}

func (n *Local) StopImportCopy(ctx context.Context, service string) error {
	n.imp.mu.Lock()
	cancel := n.imp.running[service]
	n.imp.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (n *Local) CancelImport(ctx context.Context, service string) error {
	if _, err := n.importService(service); err != nil {
		return err
	}
	n.StopImportCopy(ctx, service)
	for n.importBusy(service) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if err := n.ImportAgent.UnmountImport(ctx, service); err != nil {
		n.Log.Warn("unmounting the copy to bring data in from", "service", service, "err", err)
	}
	for _, d := range n.DataDirs(service) {
		if _, err := os.Stat(d + staging); err == nil {
			if out, err := n.unshare(ctx, "rm", "-rf", "--", d+staging).CombinedOutput(); err != nil {
				n.Log.Warn("deleting a copy", "dir", d+staging, "err", err, "out", string(out))
			}
		}
	}
	n.forgetSamples(service)
	n.imp.mu.Lock()
	defer n.imp.mu.Unlock()
	return n.State.SetSetting(ctx, importKey(service), "")
}

// libraryFinder finds media libraries for the old media folders, for a
// package that reads them (nil otherwise): by the files the old data
// names there, sampled from SQLite databases in the person's copy, else by
// the old folder's name.
func (n *Local) libraryFinder(s state.Service, files fs.FS) importer.Libraries {
	if !s.Package.ReadsLibraries() {
		return nil
	}
	n.mu.Lock()
	var libs []string
	for _, l := range n.assignment.Libraries {
		libs = append(libs, l.Name)
	}
	n.mu.Unlock()
	return libFinder{n: n, service: s.Name(), libs: libs, files: files}
}

type libFinder struct {
	n       *Local
	service string
	libs    []string
	files   fs.FS
}

// librarySample is how many file names are looked up.
const librarySample = 40

func (f libFinder) Find(target, source string) (string, string, int, int) {
	samples := f.n.samplesFor(f.service, f.files, target)
	best, found := "", 0
	for _, l := range f.libs {
		hits := 0
		for _, s := range samples {
			if _, err := os.Stat(filepath.Join(libraryRoot, l, filepath.FromSlash(s))); err == nil {
				hits++
			}
		}
		if hits > found {
			best, found = l, hits
		}
	}
	if best != "" {
		return best, importer.ByData, found, len(samples)
	}
	for _, name := range []string{path.Base(target), path.Base(source)} {
		slug := librarySlug(name)
		if slices.Contains(f.libs, slug) {
			return slug, importer.ByName, 0, len(samples)
		}
	}
	return "", "", 0, len(samples)
}

// librarySlug is a library's name for a folder's: lower case, digits, -.
func librarySlug(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 'a' - 'A'
		}
		return '-'
	}, s)
	return strings.Trim(s, "-")
}

// samplesFor finds file names the old data names under target, from the
// SQLite databases in the person's copy (looked for once, then kept).
func (n *Local) samplesFor(service string, files fs.FS, target string) []string {
	n.imp.mu.Lock()
	if got, ok := n.imp.samples[service][target]; ok {
		n.imp.mu.Unlock()
		return got
	}
	n.imp.mu.Unlock()
	var out []string
	if files != nil {
		for _, db := range findSQLite(files, 50000) {
			f, err := files.Open(db)
			if err != nil {
				continue
			}
			got, _ := importer.SamplePaths(f, strings.TrimSuffix(target, "/")+"/", librarySample-len(out))
			f.Close()
			out = append(out, got...)
			if len(out) >= librarySample {
				break
			}
		}
	}
	n.imp.mu.Lock()
	if n.imp.samples == nil {
		n.imp.samples = map[string]map[string][]string{}
	}
	if n.imp.samples[service] == nil {
		n.imp.samples[service] = map[string][]string{}
	}
	n.imp.samples[service][target] = out
	n.imp.mu.Unlock()
	return out
}

// forgetSamples drops a service's samples (its copy or setup changed).
func (n *Local) forgetSamples(service string) {
	n.imp.mu.Lock()
	delete(n.imp.samples, service)
	n.imp.mu.Unlock()
}

// findSQLite finds SQLite databases in files by name and header, looking
// at up to limit entries.
func findSQLite(files fs.FS, limit int) []string {
	var out []string
	seen := 0
	fs.WalkDir(files, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if seen++; seen > limit {
			return fs.SkipAll
		}
		if d.Type().IsRegular() && slices.ContainsFunc([]string{".db", ".sqlite", ".sqlite3", ".db3"}, func(e string) bool { return strings.HasSuffix(p, e) }) {
			if f, err := files.Open(p); err == nil {
				head := make([]byte, 16)
				n, _ := f.Read(head)
				f.Close()
				if n == 16 && string(head) == "SQLite format 3\x00" {
					out = append(out, p)
				}
			}
		}
		return nil
	})
	return out
}

// capitalise makes a sentence start with a capital.
func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// liveOutput is what runs of a service now: all of it, or only its
// network and databases while it waits for its data or is stopped by
// Roostack (generate.Stop), as the controller's assignment has it.
func (n *Local) liveOutput(service string) (*generate.Output, error) {
	s, ok := n.service(service)
	if !ok {
		return nil, ErrNotFound
	}
	return state.Output(n.currentLab(), s)
}
