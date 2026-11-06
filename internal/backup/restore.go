package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/kept"
	"roostack.cloud/roostack/internal/pkgspec"
)

// Units starts and stops a service's systemd units.
type Units interface {
	Stop(ctx context.Context, units ...string) error
	Start(ctx context.Context, units ...string) error
}

// Restorer puts one service's data back from a snapshot (SPEC.md §6.9).
type Restorer struct {
	Repo  *Repo
	Units Units
	// Launch runs podman commands (via systemd-run --user in production).
	Launch      func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd
	RoostackBin string
	Now         func() time.Time
	// Log shows the restore's steps live in the UI (optional, per run).
	Log *activity.Log
	// DBWait is how long a database may take to answer before its dump is
	// loaded (dbStartWait if 0).
	DBWait time.Duration
}

// dbStartWait is long enough for a database container to make its first,
// empty database on a slow disk.
const dbStartWait = 5 * time.Minute

// RestoreResult says what happened, for the person running the restore.
type RestoreResult struct {
	Snapshot Snapshot
	// MovedAside maps each data directory to where its previous contents
	// were moved. Nothing is deleted; remove them once happy.
	MovedAside map[string]string
}

// DumpsError is a restore whose files are back from the backup but whose
// databases couldn't be loaded: the service must stay stopped until the
// Owner chooses what to do (SPEC.md §6.9, "A restore that didn't
// finish"). Its data from before is in the .before-restore folders.
type DumpsError struct{ Err error }

func (e *DumpsError) Error() string {
	return "restoring databases: " + e.Err.Error() + " (the previous data is in the .before-restore directories)"
}

func (e *DumpsError) Unwrap() error { return e.Err }

// NotLoadedMsg is a restore log's last line when a service's files are back
// from the backup taken at taken, but its databases didn't load (the
// Owner's wording, 2026-10-22).
func NotLoadedMsg(title string, taken time.Time) string {
	return fmt.Sprintf("%s's files are back from the backup of %s, but its database couldn't be loaded, so Roostack is keeping it stopped. "+
		"Choose what to do on %s's page.", title, taken.Local().Format("Mon 2 Jan 15:04"), title)
}

// RestoreService restores a service from one of its snapshots ("" = the
// latest).
//
// The service is stopped, its current data directories and live databases
// are moved aside, the snapshot is restored, databases start empty and are
// put back from their dumps, and the service is started again. If getting
// the files back fails, the original directories are put back. A database
// moved aside is its only copy of what changed since the backup, as a
// backup holds its dump and not its folder (SPEC.md §6.9, "What a restore
// keeps").
func (r *Restorer) RestoreService(ctx context.Context, svc *generate.Output, snapshotID string) (*RestoreResult, error) {
	snap, err := r.Repo.ServiceSnapshot(ctx, svc.Service, snapshotID)
	if err != nil {
		return nil, err
	}
	dirs := svc.Backup.Include
	if len(dirs) == 0 {
		return nil, fmt.Errorf("%s has no backed-up data", svc.Service)
	}
	r.Log.Printf("Backup taken %s (%s)", snap.Time.Local().Format("2006-01-02 15:04"), snap.Short)
	r.Log.Step("Stopping %s", svc.Service)
	if err := r.Units.Stop(ctx, svc.Units...); err != nil {
		return nil, fmt.Errorf("stopping %s: %w", svc.Service, err)
	}
	res := &RestoreResult{Snapshot: *snap, MovedAside: map[string]string{}}
	at := r.Now()
	suffix := kept.Suffix(kept.BeforeRestore, "", at)
	r.Log.Step("Moving the current data aside (nothing will be deleted)")
	for _, dir := range svc.Backup.KeptDirs() {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		aside := dir + suffix
		if err := os.Rename(dir, aside); err != nil {
			return nil, r.undo(ctx, svc, res, at, fmt.Errorf("moving %s aside: %w", dir, err))
		}
		res.MovedAside[dir] = aside
		r.Log.Printf("%s → %s", dir, aside)
	}

	r.Log.Step("Restoring files from the backup")
	for _, dir := range dirs {
		if !slices.Contains(snap.Paths, dir) {
			// It didn't exist when the backup was made (e.g. no media yet),
			// so it doesn't now either.
			r.Log.Printf("%s (not in the backup: it was empty)", dir)
			continue
		}
		r.Log.Printf("%s", dir)
		err := r.Repo.RestoreDir(ctx, snap.ID, dir)
		if err != nil {
			return nil, r.undo(ctx, svc, res, at, err)
		}
	}

	if len(svc.Backup.Prepare) > 0 {
		r.Log.Step("Putting the database back")
	}
	for _, step := range svc.Backup.Prepare {
		if step.DataDir == "" {
			continue
		}
		// Its container needs the folder there to start, and will make an
		// empty database in it.
		if err := os.MkdirAll(step.DataDir, 0o750); err != nil {
			return res, &DumpsError{Err: err}
		}
	}
	if err := r.LoadDumps(ctx, svc); err != nil {
		return res, &DumpsError{Err: err}
	}
	r.Log.Step("Starting %s", svc.Service)
	if err := r.Units.Start(ctx, svc.Units...); err != nil {
		return res, fmt.Errorf("starting %s: %w", svc.Service, err)
	}
	return res, nil
}

// ServiceSnapshot finds a backup of a service by ID, or its latest ("" or
// "latest").
func (r *Repo) ServiceSnapshot(ctx context.Context, service, id string) (*Snapshot, error) {
	snaps, err := r.Snapshots(ctx, TagRoostack, ServiceTag(service))
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, fmt.Errorf("there's no backup of %s yet", service)
	}
	slices.SortFunc(snaps, func(a, b Snapshot) int { return a.Time.Compare(b.Time) })
	if id == "" || id == "latest" {
		return &snaps[len(snaps)-1], nil
	}
	for i := range snaps {
		if snaps[i].ID == id || snaps[i].Short == id || strings.HasPrefix(snaps[i].ID, id) {
			return &snaps[i], nil
		}
	}
	return nil, fmt.Errorf("no backup with ID %q", id)
}

// undo puts the original data back after a failed restore (set aside at
// at) and starts the service again, as it was before. The error adds to
// cause whether that worked, so nobody is told their data is back when it
// isn't.
func (r *Restorer) undo(ctx context.Context, svc *generate.Output, res *RestoreResult, at time.Time, cause error) error {
	r.Log.Step("Putting the current data back")
	if err := r.rollback(res, at); err != nil {
		r.Log.Printf("%v", err)
		return fmt.Errorf("%w; putting your data back failed too, so %s stays stopped: %w", cause, svc.Service, err)
	}
	r.Log.Step("Starting %s again", svc.Service)
	// Started even if the restore was cancelled: its data is back.
	if err := r.Units.Start(context.WithoutCancel(ctx), svc.Units...); err != nil {
		return fmt.Errorf("%w; your data was put back unchanged, but starting %s again failed: %w", cause, svc.Service, err)
	}
	return fmt.Errorf("%w; your data was put back unchanged", cause)
}

// rollback moves the original directories back after a failed restore.
// A partial restore is moved out of the way rather than deleted: its files
// may belong to container users that can't be removed from here. What
// couldn't be put back stays in res.MovedAside, and the error says where.
func (r *Restorer) rollback(res *RestoreResult, at time.Time) error {
	var errs []error
	for dir, aside := range res.MovedAside {
		if _, err := os.Stat(dir); err == nil {
			if err := os.Rename(dir, dir+kept.Suffix(kept.FailedRestore, "", at)); err != nil {
				errs = append(errs, fmt.Errorf("the previous data is still at %s (moving the partial restore out of the way: %w)", aside, err))
				continue
			}
		}
		if err := os.Rename(aside, dir); err != nil {
			errs = append(errs, fmt.Errorf("the previous data is still at %s (%w)", aside, err))
			continue
		}
		delete(res.MovedAside, dir)
	}
	return errors.Join(errs...)
}

// LoadDumps puts each database back from its dump in the service's data
// (after a restore, or a move through a backup). The service's containers
// must be stopped; the database's own is started for it.
func (r *Restorer) LoadDumps(ctx context.Context, svc *generate.Output) error {
	return r.loadDumps(ctx, svc, nil)
}

// loadDumps is LoadDumps, leaving out the databases and files keep says
// are already what they should be (PutBack).
func (r *Restorer) loadDumps(ctx context.Context, svc *generate.Output, keep func(generate.PrepareStep) bool) error {
	for _, step := range svc.Backup.Prepare {
		if keep != nil && keep(step) {
			continue
		}
		switch step.Type {
		case pkgspec.DBSQLite:
			// The live file was excluded from the backup; the consistent copy
			// becomes the live database.
			if err := r.run(ctx, nil, "podman", "unshare", r.RoostackBin, "backup-helper", "sqlite-restore", step.Target, step.Source); err != nil {
				return err
			}
		case pkgspec.DBPostgres:
			if err := r.Units.Start(ctx, step.Container+".service"); err != nil {
				return err
			}
			// Over TCP: while it makes a new database, a Postgres container
			// answers only on its socket, then restarts.
			if err := r.waitForDB(ctx, step, "pg_isready", "-h", "127.0.0.1", "-U", step.User, "-d", step.Database); err != nil {
				return err
			}
			dump, err := os.Open(step.Target)
			if err != nil {
				return err
			}
			err = r.run(ctx, dump, "podman", "exec", "-i", step.Container,
				"pg_restore", "--clean", "--if-exists", "--no-owner",
				"--username="+step.User, "--dbname="+step.Database)
			dump.Close()
			if err != nil {
				return err
			}
		case pkgspec.DBMySQL, pkgspec.DBMariaDB:
			if err := r.Units.Start(ctx, step.Container+".service"); err != nil {
				return err
			}
			// Over TCP, as the same goes for MySQL and MariaDB. The app's
			// user exists only once a new database is ready.
			if err := r.waitForDB(ctx, step, "sh", "-c", generate.MySQLCommand(step.Type, "client")+` -h 127.0.0.1 -u "$1" -e "DO 1"`,
				"ready", step.User); err != nil {
				return err
			}
			// The app's user may drop and recreate its own database and
			// schemas: what the app made since the backup goes, then the
			// dump goes in. A dump with its schemas makes them itself, as
			// they were (--databases); one from before has only the
			// database's tables. Then schemas the package has but the
			// backup didn't are made empty, as its database container's
			// start would. Names are letters, digits and _
			// (generate.mysqlIdent, pkgspec's schemaRe), so they need no
			// quoting.
			dump, err := os.Open(step.Target)
			if err != nil {
				return err
			}
			before := "DROP DATABASE IF EXISTS " + step.Database + "; CREATE DATABASE " + step.Database
			after := "DO 1"
			for _, s := range step.Schemas {
				before += "; DROP DATABASE IF EXISTS " + s
				after += "; CREATE DATABASE IF NOT EXISTS " + s
			}
			client := "(" + generate.MySQLCommand(step.Type, "client") + ` -h 127.0.0.1 -u "$1"`
			err = r.run(ctx, dump, "podman", "exec", "-i", step.Container, "sh", "-c",
				client+` -e "$2") && `+client+` "$3") && `+client+` -e "$4")`,
				"restore", step.User, before, step.Database, after)
			dump.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// waitForDB runs a command in a database's container until it succeeds,
// or says what it last said once the wait is over.
func (r *Restorer) waitForDB(ctx context.Context, step generate.PrepareStep, check ...string) error {
	wait := r.DBWait
	if wait == 0 {
		wait = dbStartWait
	}
	deadline := time.Now().Add(wait)
	for {
		err := r.run(ctx, nil, "podman", append([]string{"exec", "-i", step.Container}, check...)...)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s didn't answer within %s: %w", step.Container, wait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (r *Restorer) run(ctx context.Context, stdin *os.File, name string, args ...string) error {
	cmd := r.Launch(ctx, nil, name, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PutBack puts a copy a restore on this server kept (kept.Copy, not
// Whole) back in place of a service's data, with the copies of its other
// folders from the same moment, and its databases from the dumps among
// them (SPEC.md §8.4: every kept copy can be put back). A database kept
// whole goes back as it was: its dump, from the backup before, is older.
// The data it replaces is kept in turn (kept.BeforePutBack), so changing
// one's mind again is a put back too. The service is stopped meanwhile and
// started again.
func (r *Restorer) PutBack(ctx context.Context, svc *generate.Output, path string) error {
	f, err := keptInside(svc, path)
	if err != nil {
		return err
	}
	suffix := strings.TrimPrefix(f.Path, f.Of)
	now := kept.Suffix(kept.BeforePutBack, "", r.Now())
	r.Log.Step("Stopping %s", svc.Service)
	if err := r.Units.Stop(ctx, svc.Units...); err != nil {
		return err
	}
	r.Log.Step("Putting the kept copy back (what's there now will be kept)")
	whole := map[string]bool{}
	for _, dir := range svc.Backup.KeptDirs() {
		if _, err := os.Stat(dir + suffix); err != nil {
			continue
		}
		whole[dir] = true
		if _, err := os.Stat(dir); err == nil {
			if err := os.Rename(dir, dir+now); err != nil {
				return err
			}
			r.Log.Printf("%s → %s", dir, dir+now)
		}
		if err := os.Rename(dir+suffix, dir); err != nil {
			return err
		}
		r.Log.Printf("%s → %s", dir+suffix, dir)
	}
	if len(svc.Backup.Prepare) > 0 {
		r.Log.Step("Putting the database back")
	}
	err = r.loadDumps(ctx, svc, func(step generate.PrepareStep) bool {
		if step.Type == pkgspec.DBSQLite {
			// Live in the data that went back, unless that's a backup's,
			// which leaves it out.
			_, err := os.Stat(step.Source)
			return err == nil
		}
		return whole[step.DataDir]
	})
	if err != nil {
		return err
	}
	r.Log.Step("Starting %s", svc.Service)
	return r.Units.Start(ctx, svc.Units...)
}

// keptInside is the kept folder at path, if it's one of a service's
// backed-up folders that can go back (kept.Find).
func keptInside(svc *generate.Output, path string) (kept.Folder, error) {
	copies, err := kept.Find(nil, map[string][]string{svc.Service: svc.Backup.KeptDirs()})
	if err != nil {
		return kept.Folder{}, err
	}
	f, ok := kept.Parse(path)
	if !ok || !slices.ContainsFunc(copies, func(c kept.Copy) bool { return c.Has(path) && c.Why.CanGoBack() }) {
		return kept.Folder{}, fmt.Errorf("%s is not a copy of %s's data that can be put back", path, svc.Service)
	}
	return f, nil
}

// SQLiteRestore replaces a live SQLite database with a backed-up copy. The
// app must be stopped. Its stale -wal/-shm files are removed first, since
// they belong to the old database. The new file gets the owner of its
// directory (restored with its original owner), so apps running as a
// non-root container user can still write to it. Runs inside podman
// unshare via `roostack backup-helper sqlite-restore`.
func SQLiteRestore(ctx context.Context, src, dst string) error {
	for _, f := range []string{dst + "-wal", dst + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := SQLiteCopy(ctx, src, dst); err != nil {
		return err
	}
	dir, err := os.Stat(filepath.Dir(dst))
	if err != nil {
		return err
	}
	if st, ok := dir.Sys().(*syscall.Stat_t); ok {
		if err := os.Chown(dst, int(st.Uid), int(st.Gid)); err != nil && !errors.Is(err, os.ErrPermission) {
			return err
		}
	}
	return nil
}
