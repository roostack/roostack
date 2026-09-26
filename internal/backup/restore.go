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

	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/pkgspec"
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
	Launch   func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd
	MylabBin string
	Now      func() time.Time
}

// RestoreResult says what happened, for the person running the restore.
type RestoreResult struct {
	Snapshot Snapshot
	// MovedAside maps each data directory to where its previous contents
	// were moved. Nothing is deleted; remove them once happy.
	MovedAside map[string]string
}

// RestoreService restores a service from a snapshot ("" = the latest).
//
// The service is stopped, its current data directories are moved aside,
// the snapshot is restored, databases are put back from their dumps, and
// the service is started again. If the snapshot doesn't contain the
// service's data, the original directories are put back.
func (r *Restorer) RestoreService(ctx context.Context, svc *generate.Output, snapshotID string) (*RestoreResult, error) {
	snap, err := r.pickSnapshot(ctx, snapshotID)
	if err != nil {
		return nil, err
	}
	dirs := svc.Backup.Include
	if len(dirs) == 0 {
		return nil, fmt.Errorf("%s has no backed-up data", svc.Service)
	}
	if err := r.Units.Stop(ctx, svc.Units...); err != nil {
		return nil, fmt.Errorf("stopping %s: %w", svc.Service, err)
	}
	res := &RestoreResult{Snapshot: *snap, MovedAside: map[string]string{}}
	suffix := ".before-restore-" + r.Now().Format("20060102-150405")
	for _, dir := range dirs {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			continue
		}
		aside := dir + suffix
		if err := os.Rename(dir, aside); err != nil {
			r.rollback(res, suffix)
			return nil, fmt.Errorf("moving %s aside: %w", dir, err)
		}
		res.MovedAside[dir] = aside
	}

	for _, dir := range dirs {
		err := r.Repo.RestoreDir(ctx, snap.ID, dir)
		if errors.Is(err, ErrNotInSnapshot) {
			r.rollback(res, suffix)
			return nil, fmt.Errorf("snapshot %s doesn't contain %s; your data was put back unchanged", snap.Short, dir)
		}
		if err != nil {
			r.rollback(res, suffix)
			return nil, fmt.Errorf("%w; your data was put back unchanged", err)
		}
	}

	if err := r.restoreDatabases(ctx, svc); err != nil {
		return res, fmt.Errorf("restoring databases: %w (the previous data is in the .before-restore directories)", err)
	}
	if err := r.Units.Start(ctx, svc.Units...); err != nil {
		return res, fmt.Errorf("starting %s: %w", svc.Service, err)
	}
	return res, nil
}

// pickSnapshot finds the requested MyLab snapshot, or the latest.
func (r *Repo) pickSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	snaps, err := r.Snapshots(ctx, "mylab")
	if err != nil {
		return nil, err
	}
	if len(snaps) == 0 {
		return nil, errors.New("there are no backups yet")
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

func (r *Restorer) pickSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	return r.Repo.pickSnapshot(ctx, id)
}

// rollback moves the original directories back after a failed restore.
// A partial restore is moved out of the way rather than deleted: its files
// may belong to container users that can't be removed from here.
func (r *Restorer) rollback(res *RestoreResult, suffix string) {
	for dir, aside := range res.MovedAside {
		if _, err := os.Stat(dir); err == nil {
			os.Rename(dir, strings.Replace(dir+suffix, ".before-restore-", ".failed-restore-", 1))
		}
		os.Rename(aside, dir)
	}
	res.MovedAside = map[string]string{}
}

// restoreDatabases puts each database back from its dump.
func (r *Restorer) restoreDatabases(ctx context.Context, svc *generate.Output) error {
	for _, step := range svc.Backup.Prepare {
		switch step.Type {
		case pkgspec.DBSQLite:
			// The live file was excluded from the backup; the consistent copy
			// becomes the live database.
			if err := r.run(ctx, nil, "podman", "unshare", r.MylabBin, "backup-helper", "sqlite-restore", step.Target, step.Source); err != nil {
				return err
			}
		case pkgspec.DBPostgres:
			// UNTESTED against a real Postgres service (TASKS 1.13).
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

// SQLiteRestore replaces a live SQLite database with a backed-up copy. The
// app must be stopped. Its stale -wal/-shm files are removed first, since
// they belong to the old database. The new file gets the owner of its
// directory (restored with its original owner), so apps running as a
// non-root container user can still write to it. Runs inside podman
// unshare via `mylab backup-helper sqlite-restore`.
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
