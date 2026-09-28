package labrestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
)

// A lab restore runs in three steps, across a restart of Roostack:
//
//  1. Restore (before any account exists): the backup's files go back to
//     where they were (Roostack's state, secrets and every service's data),
//     the recovery key becomes the backup key, and a marker is left.
//  2. StartFromBackup (when Roostack starts, before it opens its database):
//     the backed-up copy of the database becomes the live one.
//  3. Databases (once the services have been set up again): each
//     service's databases are put back from their dumps.

// marker records a restore in progress, in the state folder.
type marker struct {
	Repository string    `json:"repository"`
	Snapshot   string    `json:"snapshot"`
	Restored   time.Time `json:"restored"`
	// Step is "database" (step 2 next) or "services" (step 3 next).
	Step string `json:"step"`
}

func markerFile(paths config.Paths) string {
	return filepath.Join(paths.StateDir, "restore-pending.json")
}

func readMarker(paths config.Paths) (marker, bool) {
	var m marker
	data, err := os.ReadFile(markerFile(paths))
	if err != nil || json.Unmarshal(data, &m) != nil {
		return m, false
	}
	return m, true
}

func (m marker) save(paths config.Paths) error {
	data, _ := json.Marshal(m)
	tmp := markerFile(paths) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, markerFile(paths))
}

// Restore puts a server's backup back onto this one (step 1). repo is
// opened with the recovery key, which is in keyFile. It must run on a
// server with nothing on it yet; the caller then restarts Roostack.
func Restore(ctx context.Context, paths config.Paths, repo *backup.Repo, fd Found, keyFile string, log *activity.Log) error {
	if fd.StateDir != paths.StateDir {
		return fmt.Errorf("that server kept Roostack's state in %s, but this one uses %s; set paths.state_dir in lab.yaml to match first",
			fd.StateDir, paths.StateDir)
	}
	if !fd.Controller {
		return errors.New("that's a backup of a server that the controller manages: restore the controller's backup first")
	}
	log.Step("Restoring %s's files from the backup of %s", fd.ServerName, fd.Snapshot.Time.Local().Format("2 January 2006 15:04"))
	for _, p := range fd.Snapshot.Paths {
		log.Printf("%s", p)
	}
	// Every path goes back where it was, as it was (owners included:
	// restic runs in the roostack user's namespace, as for backups). Each on
	// its own, so folders above them (/var, /srv) are left alone.
	for _, p := range fd.Snapshot.Paths {
		if err := os.MkdirAll(p, 0o750); err != nil {
			return err
		}
		if err := repo.RestoreDir(ctx, fd.Snapshot.ID, p); err != nil {
			return fmt.Errorf("restoring %s: %w", p, err)
		}
	}
	if _, err := os.Stat(backup.StateDumpFile(paths)); err != nil {
		return fmt.Errorf("the backup has no copy of Roostack's database: %w", err)
	}
	log.Step("Keeping the recovery key for the next backups")
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	if err := writeFile(backup.KeyFile(paths), key); err != nil {
		return err
	}
	log.Step("Restarting Roostack as the restored lab")
	return marker{Repository: fd.Repository, Snapshot: fd.Snapshot.ID, Restored: time.Now(), Step: "database"}.save(paths)
}

// StartFromBackup finishes step 2 if a restore is in progress: the
// backed-up copy of Roostack's database replaces the new, empty one. Call it
// before opening the database.
func StartFromBackup(paths config.Paths) (bool, error) {
	m, ok := readMarker(paths)
	if !ok || m.Step != "database" {
		return false, nil
	}
	db := filepath.Join(paths.StateDir, "roostack.db")
	for _, f := range []string{db + "-wal", db + "-shm"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	if err := copyFile(backup.StateDumpFile(paths), db); err != nil {
		return false, fmt.Errorf("putting Roostack's database back: %w", err)
	}
	m.Step = "services"
	return true, m.save(paths)
}

// Pending reports whether the services' databases still need putting
// back (step 3), and since when.
func Pending(paths config.Paths) (time.Time, bool) {
	m, ok := readMarker(paths)
	return m.Restored, ok && m.Step == "services"
}

// Done ends a restore.
func Done(paths config.Paths) error {
	err := os.Remove(markerFile(paths))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".restore"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func writeFile(file string, data []byte) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
