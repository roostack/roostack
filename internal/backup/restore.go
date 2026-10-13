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
}

// RestoreResult says what happened, for the person running the restore.
type RestoreResult struct {
	Snapshot Snapshot
	// MovedAside maps each data directory to where its previous contents
	// were moved. Nothing is deleted; remove them once happy.
	MovedAside map[string]string
}

// RestoreService restores a service from one of its snapshots ("" = the
// latest).
//
// The service is stopped, its current data directories are moved aside,
// the snapshot is restored, databases are put back from their dumps, and
// the service is started again. If getting the files back fails, the
// original directories are put back.
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
	suffix := keptBefore + r.Now().Format(keptTimeLayout)
	r.Log.Step("Moving the current data aside (nothing is deleted)")
	for _, dir := range dirs {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		aside := dir + suffix
		if err := os.Rename(dir, aside); err != nil {
			return nil, r.undo(ctx, svc, res, suffix, fmt.Errorf("moving %s aside: %w", dir, err))
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
			return nil, r.undo(ctx, svc, res, suffix, err)
		}
	}

	if len(svc.Backup.Prepare) > 0 {
		r.Log.Step("Putting the database back")
	}
	if err := r.LoadDumps(ctx, svc); err != nil {
		return res, fmt.Errorf("restoring databases: %w (the previous data is in the .before-restore directories)", err)
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

// undo puts the original data back after a failed restore and starts the
// service again, as it was before. The error adds to cause whether that
// worked, so nobody is told their data is back when it isn't.
func (r *Restorer) undo(ctx context.Context, svc *generate.Output, res *RestoreResult, suffix string, cause error) error {
	r.Log.Step("Putting the current data back")
	if err := r.rollback(res, suffix); err != nil {
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
func (r *Restorer) rollback(res *RestoreResult, suffix string) error {
	var errs []error
	for dir, aside := range res.MovedAside {
		if _, err := os.Stat(dir); err == nil {
			if err := os.Rename(dir, strings.Replace(dir+suffix, keptBefore, keptFailed, 1)); err != nil {
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
	for _, step := range svc.Backup.Prepare {
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
		}
	}
	return nil
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

// Suffixes of directories a restore keeps: the data it replaced, and a
// partial restore that was rolled back.
const (
	keptBefore     = ".before-restore-"
	keptFailed     = ".failed-restore-"
	keptTimeLayout = "20060102-150405"
)

// KeptCopy is a directory a restore left behind.
type KeptCopy struct {
	Path   string
	Of     string // the data directory it is a copy of
	Failed bool   // a rolled-back partial restore, not the previous data
	Time   time.Time
}

// KeptCopies lists the directories restores left behind for a service.
func KeptCopies(svc *generate.Output) ([]KeptCopy, error) {
	var kept []KeptCopy
	for _, dir := range svc.Backup.Include {
		for _, suffix := range []string{keptBefore, keptFailed} {
			matches, err := filepath.Glob(dir + suffix + "*")
			if err != nil {
				return nil, err
			}
			for _, m := range matches {
				t, err := time.ParseInLocation(keptTimeLayout, strings.TrimPrefix(m, dir+suffix), time.Local)
				if err != nil {
					continue // not one of ours
				}
				kept = append(kept, KeptCopy{Path: m, Of: dir, Failed: suffix == keptFailed, Time: t})
			}
		}
	}
	slices.SortFunc(kept, func(a, b KeptCopy) int { return b.Time.Compare(a.Time) })
	return kept, nil
}

// RemoveKept deletes a kept copy. Only paths KeptCopies returns for the
// service are accepted. Files may belong to container users, so it runs
// inside podman unshare.
func (r *Restorer) RemoveKept(ctx context.Context, svc *generate.Output, path string) error {
	kept, err := KeptCopies(svc)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(kept, func(k KeptCopy) bool { return k.Path == path }) {
		return fmt.Errorf("%s is not a copy kept by a restore", path)
	}
	return r.run(ctx, nil, "podman", "unshare", "rm", "-rf", "--", path)
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
