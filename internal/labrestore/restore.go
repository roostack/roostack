package labrestore

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/instance"
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
	return fsutil.WritePrivate(markerFile(paths), data)
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
	if fd.Folder != fd.ServerID {
		// Find refuses these; a caller holding one built elsewhere too.
		return &forgedError{name: cmp.Or(fd.ServerName, fd.ServerID), id: fd.ServerID, folder: fd.Folder}
	}
	// Only what Roostack backs up of a server: its state and the secrets
	// store (backup.LabPlan). A backup listing anything else wasn't made
	// by Roostack, and isn't put back (security review of 2026-10-16, M26).
	if err := allowed(fd.Snapshot, "the server's backup", []string{paths.StateDir, secretsStore(paths)}, nil); err != nil {
		return err
	}
	log.Step("Restoring %s's settings from the backup of %s", fd.ServerName, fd.Snapshot.Time.Local().Format("2 January 2006 15:04"))
	if err := restorePaths(ctx, paths, repo, fd.Snapshot, log); err != nil {
		return err
	}
	// Then each of the server's services, from its own latest backup
	// (SPEC.md §10.10). Services on the lab's other servers are restored
	// later, from their servers' backups, once Roostack runs.
	services, err := hostedServices(paths, fd.ServerID)
	if err != nil {
		return err
	}
	for _, name := range services {
		snap, err := repo.ServiceSnapshot(ctx, name, "")
		if err != nil {
			log.Printf("%s: %v", name, err)
			continue
		}
		// A service's backup holds only its own folders (generate's
		// storage: under the services' folder, or the media folder).
		var own []string
		for _, dir := range []string{paths.ServicesDir, paths.MediaDir} {
			if dir != "" {
				own = append(own, filepath.Join(dir, name))
			}
		}
		if err := allowed(*snap, name+"'s backup", nil, own); err != nil {
			return err
		}
		log.Step("Restoring %s from its backup of %s", name, snap.Time.Local().Format("2 January 2006 15:04"))
		if err := restorePaths(ctx, paths, repo, *snap, log); err != nil {
			return err
		}
	}
	if _, err := os.Stat(backup.StateDumpFile(paths)); err != nil {
		return fmt.Errorf("the backup has no copy of Roostack's database: %w", err)
	}
	// This machine is the lab now: a newer generation, so the old one
	// stands down if it comes back (backup/fence.go).
	if err := backup.WriteGeneration(paths, backup.ReadGeneration(paths)+1); err != nil {
		return err
	}
	log.Step("Keeping the recovery key for the next backups")
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	if err := fsutil.WritePrivate(backup.KeyFile(paths), key); err != nil {
		return err
	}
	// The restored lab's hints are from its last backup, and its
	// repositories may have moved since (a drive mounted elsewhere): add
	// those this restore learned, for the restored lab's first commands
	// (SPEC.md §10.10 "Key hints").
	if repo.Hints != nil {
		if err := backup.Hints(paths).Add(repo.Hints); err != nil {
			log.Printf("Couldn't keep which key opens each backup (%v): Roostack will ask restic again", err)
		}
	}
	log.Step("Restarting Roostack as the restored lab")
	return marker{Repository: fd.Repository, Snapshot: fd.Snapshot.ID, Restored: time.Now(), Step: "database"}.save(paths)
}

// secretsStore is the Podman secrets store, which a server's backup
// holds beside the state folder (as backup.LabPlan names it).
func secretsStore(paths config.Paths) string {
	return filepath.Join(paths.HomeDir, ".local", "share", "containers", "storage", "secrets")
}

// allowed checks that each of a snapshot's paths is one of exact, or a
// folder inside one of under, before any is put back: what restores
// trust of a backup is where its files go.
func allowed(snap backup.Snapshot, what string, exact, under []string) error {
	for _, p := range snap.Paths {
		ok := filepath.IsAbs(p) && filepath.Clean(p) == p && slices.Contains(exact, p)
		for _, dir := range under {
			ok = ok || filepath.IsAbs(p) && filepath.Clean(p) == p && fsutil.Within(p, dir)
		}
		if !ok {
			return fmt.Errorf("%s holds %s, which isn't one of the places Roostack restores (Roostack's state folder, the secrets store and "+
				"each service's folders, as this server's paths in lab.yaml set them): it wasn't made by Roostack, or the old server "+
				"kept its files elsewhere (set the paths in lab.yaml to match first)", what, p)
		}
	}
	return nil
}

// restorePaths puts every path of a snapshot back where it was, as it was
// (owners included: restic runs in the roostack user's namespace, as for
// backups). Each on its own, so folders above them (/var, /srv) are left
// alone. A service's saved secrets aren't needed: the server's own
// snapshot has the whole secrets store.
func restorePaths(ctx context.Context, paths config.Paths, repo *backup.Repo, snap backup.Snapshot, log *activity.Log) error {
	for _, p := range snap.Paths {
		if strings.HasPrefix(p, backup.SecretsDir(paths)+"/") {
			continue
		}
		log.Printf("%s", p)
		if err := os.MkdirAll(p, 0o750); err != nil {
			return err
		}
		if err := repo.RestoreDir(ctx, snap.ID, p); err != nil {
			return fmt.Errorf("restoring %s: %w", p, err)
		}
	}
	return nil
}

// hostedServices lists the services the restored state places on a server.
func hostedServices(paths config.Paths, serverID string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(paths.InstancesDir(), "*.yaml"))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		in, err := instance.Load(f)
		if err != nil {
			return nil, err
		}
		if in.Host == "" || in.Host == serverID {
			out = append(out, strings.TrimSuffix(filepath.Base(f), ".yaml"))
		}
	}
	return out, nil
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
	if err := fsutil.CopyFile(backup.StateDumpFile(paths), db, 0o600); err != nil {
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
