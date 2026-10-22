package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/importer"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/units"
	"roostack.cloud/roostack/internal/usererr"
)

// The copy and the switch (SPEC.md §8.4, docs/MIGRATION-PLAN.md §3.7):
// each storage brought is copied into the service's staging folders
// (<data>.incoming/<storage>, as a move's are) and each dump beside them as
// its database's backup dump (db-dump/<database>.sql) while everything
// runs, again and again; the switch stops the service, copies once more,
// puts the copy in place (the data there aside, as .before-import-<time>),
// loads the dumps with the restore loader, and starts it.

// keptImport names a service's data set aside by a switch.
const keptImport = ".before-import-"

// importSaveEvery is how often a copy's progress is saved, so a restart
// knows how far it got.
const importSaveEvery = 10 * time.Second

func (n *Local) CopyImport(ctx context.Context, service string) error {
	s, err := n.importService(service)
	if err != nil {
		return err
	}
	if st := n.loadImport(ctx, service); !st.Check.Ready() {
		return usererr.New("Check your files first: the copy starts once the check finds nothing in the way.")
	}
	return n.startImportRun(service, func(ctx context.Context) { n.runImportCopy(ctx, s) })
}

// runImportCopy copies what's brought into the staging folders.
func (n *Local) runImportCopy(ctx context.Context, s state.Service) error {
	service, title := s.Name(), s.Title()
	st := n.loadImport(ctx, service)
	var act *activity.Log
	if n.Activity != nil {
		act = n.Activity.Start(activity.ImportRunFor(service), "Copying "+title+"'s data")
	}
	total := st.Check.Bytes
	cp := &ImportCopy{Running: true, Started: time.Now(), Files: st.Check.Files, Bytes: total}
	if st.Copy != nil {
		cp.Passes = st.Copy.Passes
	}
	save := func() {
		c := *cp
		n.updateImport(context.WithoutCancel(ctx), service, func(st *ImportState) error {
			st.Copy = &c
			return nil
		})
	}
	save()
	err := n.copyImport(ctx, s, st, act, func(p importer.Progress) {
		cp.Progress = p
		if time.Since(cp.Started) > importSaveEvery {
			save()
		}
	}, &cp.Progress)
	cp.Running, cp.Finished = false, time.Now()
	switch {
	case errors.Is(err, context.Canceled):
		cp.Stopped, cp.Err = true, ""
		act.Printf("Stopped: copy again to carry on from here.")
	case err != nil:
		cp.Err = err.Error()
	default:
		cp.Passes++
	}
	save()
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	act.Finish(err)
	return err
}

// copyImport copies each storage and dump brought; progress hears how far
// it got overall, and at is left where it ended.
func (n *Local) copyImport(ctx context.Context, s state.Service, st ImportState, act *activity.Log,
	progress func(importer.Progress), at *importer.Progress) error {
	service, p := s.Name(), s.Package
	files := importFiles(service, st)
	if files == nil {
		return fmt.Errorf("your files aren't mounted at %s", filepath.Join(importRoot, service))
	}
	base := filepath.Join(importRoot, service)
	plan := importer.Match(p, st.Setup, st.Prefixes, st.Choices, files, n.libraryFinder(s, files))
	var done importer.Progress
	for _, sp := range plan.Storage {
		dst := n.stagingDir(service, sp)
		if sp.From == "" {
			continue
		}
		act.Step("Copying %s", strings.ToLower(sp.Title[:1])+sp.Title[1:])
		act.Printf("From %s, into %s%s", sp.From, dst, excludedText(sp.Exclude))
		before := done
		got, err := importer.Copy(ctx, importer.Rel(base, sp.From), dst, sp.Exclude, func(pr importer.Progress) {
			now := before
			now.Files, now.Bytes, now.Copied = now.Files+pr.Files, now.Bytes+pr.Bytes, now.Copied+pr.Copied
			now.Now = sp.Title + ": " + pr.Now
			*at = now
			progress(now)
		})
		done.Files, done.Bytes, done.Copied, done.Removed, done.Special =
			done.Files+got.Files, done.Bytes+got.Bytes, done.Copied+got.Copied, done.Removed+got.Removed, done.Special+got.Special
		*at = done
		progress(done)
		if err != nil {
			return err
		}
		act.Printf("%s: %d files looked at, %s copied, %d removed", sp.Title, got.Files, units.Bytes(got.Copied), got.Removed)
		if err := n.chownStorage(ctx, p, sp.Name, dst); err != nil {
			return err
		}
	}
	for _, db := range plan.Databases {
		if db.From == "" {
			continue
		}
		act.Step("Copying the dump of database %s", db.Name)
		dst := filepath.Join(n.Lab.Paths.ServicesDir, service+staging, "db-dump", db.Name+".sql")
		if err := copyOne(importer.Rel(base, db.From), dst); err != nil {
			return err
		}
	}
	return nil
}

// stagingDir is where a storage's copy goes before the switch.
func (n *Local) stagingDir(service string, sp importer.Storage) string {
	dir := n.Lab.Paths.ServicesDir
	if sp.Media {
		dir = n.Lab.Paths.MediaDir
	}
	return filepath.Join(dir, service+staging, sp.Name)
}

// excludedText says what a copy leaves out.
func excludedText(ex []string) string {
	if len(ex) == 0 {
		return ""
	}
	return ", without " + strings.Join(ex, ", ")
}

// copyOne copies a file (a dump), unless one of the same size and time
// is there already: atomically, then with its time.
func copyOne(src, dst string) error {
	si, err := os.Stat(src)
	if err != nil {
		return err
	}
	if di, err := os.Stat(dst); err == nil && di.Size() == si.Size() && di.ModTime().Equal(si.ModTime()) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	if err := fsutil.CopyFile(src, dst, 0o600); err != nil {
		return err
	}
	return os.Chtimes(dst, si.ModTime(), si.ModTime())
}

// userRe is a container's numeric user in its Quadlet lines (User=1000 or
// 1000:1000).
var userRe = regexp.MustCompile(`^(\d+)(?::(\d+))?$`)

// chownStorage gives a storage's copy the owner its containers run as
// inside, if they run as a numeric user without keep-id (their files must
// be that user's mapped ID); otherwise everything stays Roostack's, the
// containers' root, as in folders Roostack makes.
func (n *Local) chownStorage(ctx context.Context, p *pkgspec.Package, storage, dir string) error {
	for _, cn := range slices.Sorted(maps.Keys(p.Containers)) {
		c := p.Containers[cn]
		if !slices.ContainsFunc(c.Mounts, func(m pkgspec.Mount) bool { return m.Storage == storage }) {
			continue
		}
		sec := c.Quadlet["Container"]
		user, _ := sec["User"].(string)
		if ns, _ := sec["UserNS"].(string); user == "" || strings.HasPrefix(ns, "keep-id") {
			continue
		}
		m := userRe.FindStringSubmatch(user)
		if m == nil {
			continue
		}
		owner := m[1] + ":" + firstNonEmpty(m[2], m[1])
		if out, err := n.unshare(ctx, "chown", "-R", owner, dir).CombinedOutput(); err != nil {
			return fmt.Errorf("giving %s to user %s: %w: %s", dir, owner, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (n *Local) SwitchImport(ctx context.Context, service, step string) error {
	s, err := n.importService(service)
	if err != nil {
		return err
	}
	if !slices.Contains([]string{SwitchStop, SwitchCopy, SwitchCommit, SwitchLoad, SwitchStart, SwitchUndo, SwitchFinish}, step) {
		return fmt.Errorf("no such step %q", step)
	}
	set := func(st SwitchStep) {
		n.updateImport(context.Background(), service, func(is *ImportState) error {
			is.Step = &st
			return nil
		})
	}
	set(SwitchStep{Name: step, Running: true})
	err = n.startImportRun(service, func(ctx context.Context) {
		err := n.switchStep(ctx, s, step)
		st := SwitchStep{Name: step}
		if err != nil {
			st.Err = err.Error()
		}
		set(st)
	})
	if err != nil {
		set(SwitchStep{Name: step, Err: err.Error()})
	}
	return err
}

// switchStep does one step of the switch.
func (n *Local) switchStep(ctx context.Context, s state.Service, step string) error {
	service := s.Name()
	switch step {
	case SwitchStop:
		live, err := state.Output(n.currentLab(), s)
		if err != nil {
			return err
		}
		return n.Reconciler.Systemd.Stop(ctx, live.Units...)
	case SwitchCopy:
		var at importer.Progress
		return n.copyImport(ctx, s, n.loadImport(ctx, service), nil, func(importer.Progress) {}, &at)
	case SwitchCommit:
		st := n.loadImport(ctx, service)
		files := importFiles(service, st)
		plan := importer.Match(s.Package, st.Setup, st.Prefixes, st.Choices, files, n.libraryFinder(s, files))
		var brought []string
		for _, sp := range plan.Storage {
			if sp.From != "" {
				brought = append(brought, sp.Title)
			}
		}
		for _, db := range plan.Databases {
			if db.From != "" {
				brought = append(brought, fmt.Sprintf("database %q", db.Name))
			}
		}
		var libs []importer.Library
		for _, l := range plan.Libraries {
			if l.Library != "" {
				libs = append(libs, l)
			}
		}
		kept, err := n.commitStaged(service, keptImport)
		n.updateImport(ctx, service, func(st *ImportState) error {
			st.Kept, st.Brought, st.BroughtLibraries = kept, brought, libs
			return nil
		})
		if err != nil {
			return err
		}
		out, err := generate.Service(n.currentLab(), s.Package, s.Instance)
		if err != nil {
			return err
		}
		// Its folders that weren't brought (a database's, say) are made
		// empty, as an apply would.
		for _, d := range out.Dirs {
			if err := os.MkdirAll(d, 0o750); err != nil {
				return err
			}
		}
		return nil
	case SwitchLoad:
		return n.loadImported(ctx, s)
	case SwitchStart:
		live, err := state.Output(n.currentLab(), s)
		if err != nil {
			return err
		}
		return n.Reconciler.Systemd.Start(ctx, live.Units...)
	case SwitchUndo:
		return n.undoImport(ctx, s, n.loadImport(ctx, service).Kept)
	case SwitchFinish:
		if err := n.ImportAgent.UnmountImport(ctx, service); err != nil {
			n.Log.Warn("unmounting the copy data was brought in from", "service", service, "err", err)
		}
		n.forgetSamples(service)
		return n.updateImport(ctx, service, func(st *ImportState) error {
			st.Switched = time.Now()
			return nil
		})
	}
	return nil
}

// commitStaged puts a service's staging folders in place, setting aside
// what's there under suffix and the time; it returns where.
func (n *Local) commitStaged(service, suffix string) ([]string, error) {
	if _, err := n.dataDir(service, 0); err != nil {
		return nil, err
	}
	var kept []string
	at := suffix + time.Now().Format(timeLayout)
	for _, d := range n.DataDirs(service) {
		in := d + staging
		if _, err := os.Stat(in); err != nil {
			continue
		}
		if _, err := os.Stat(d); err == nil {
			// A service waiting for its data may have only the empty
			// folders its install made: nothing to keep, as the page
			// promised. (Folders a container's user owns may not go: then
			// they're kept like data.)
			if !onlyFolders(d) || os.RemoveAll(d) != nil {
				if err := os.Rename(d, d+at); err != nil {
					return kept, err
				}
				kept = append(kept, d+at)
			}
		}
		if err := os.Rename(in, d); err != nil {
			return kept, err
		}
	}
	return kept, nil
}

// onlyFolders says whether dir holds no files, only (empty) folders.
func onlyFolders(dir string) bool {
	empty := true
	filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil || !e.IsDir() {
			empty = false
			return fs.SkipAll
		}
		return nil
	})
	return empty
}

// loadImported loads the dumps brought (SQLite came with its folder),
// starting only the databases; the service's units must be stopped.
func (n *Local) loadImported(ctx context.Context, s state.Service) error {
	out, err := generate.Service(n.currentLab(), s.Package, s.Instance)
	if err != nil {
		return err
	}
	loading := *out
	loading.Backup.Prepare = slices.DeleteFunc(slices.Clone(out.Backup.Prepare), func(st generate.PrepareStep) bool {
		_, err := os.Stat(st.Target)
		return st.Type == pkgspec.DBSQLite || err != nil
	})
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	r := &backup.Restorer{Units: n.Reconciler.Systemd, Launch: n.Launch, RoostackBin: exe}
	return r.LoadDumps(ctx, &loading)
}

// undoImport puts a service's data from before the switch back: what the
// switch brought is set aside in turn (as .failed-import-<time>), its
// units stopped first.
func (n *Local) undoImport(ctx context.Context, s state.Service, kept []string) error {
	live, err := state.Output(n.currentLab(), s)
	if err != nil {
		return err
	}
	if err := n.Reconciler.Systemd.Stop(ctx, live.Units...); err != nil {
		return err
	}
	at := ".failed-import-" + time.Now().Format(timeLayout)
	var errs []error
	for _, d := range n.DataDirs(s.Name()) {
		var from string
		for _, k := range kept {
			if strings.HasPrefix(k, d+keptImport) {
				from = k
			}
		}
		if _, err := os.Stat(d); err == nil {
			if err := os.Rename(d, d+at); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		if from != "" {
			if err := os.Rename(from, d); err != nil {
				errs = append(errs, fmt.Errorf("the data from before is still at %s: %w", from, err))
			}
		}
	}
	return errors.Join(errs...)
}

// resumeImports carries on copies a restart interrupted.
func (n *Local) resumeImports(ctx context.Context) {
	n.mu.Lock()
	services := slices.Clone(n.assignment.Services)
	n.mu.Unlock()
	for _, s := range services {
		st := n.loadImport(ctx, s.Name())
		if st.Copy != nil && st.Copy.Running {
			n.Log.Info("carrying on bringing data in", "service", s.Name())
			n.startImportRun(s.Name(), func(ctx context.Context) { n.runImportCopy(ctx, s) })
		}
		if st.Checking {
			n.updateImport(ctx, s.Name(), func(st *ImportState) error {
				st.Checking, st.CheckErr = false, "Roostack restarted during the check: check again."
				return nil
			})
		}
	}
}
