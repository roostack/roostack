package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/kept"
	"roostack.cloud/roostack/internal/node"
)

// One service's data going to a server (SPEC.md §10.10): a move, copying
// its data directly or through a fresh backup (move.go), and a restore
// from any of the lab's backups, onto its own server or another
// (restore.go), which is also how a gone server's services come back
// (lost.go). Each stops the service where it runs, brings its data and
// secrets, puts them in place, switches where it runs and sets the old
// copy aside. A failure after it stopped starts it again where it ran;
// what happens when its databases don't load differs (hold.go).

// transferJob is one move or restore of a service, run by the controller.
type transferJob struct {
	m   *Manager
	act *activity.Log
	// title is the service's, for the log.
	name, title string
	// from is the server the service runs on now, to the one it goes to
	// (the same for a restore in place).
	from, to string
	// src is from's side (nil if from can't be reached: then a restore
	// goes ahead without stopping it there), dst to's.
	src, dst node.Mover
	// restore: from a backup the Owner chose, with the secrets saved in
	// it (else a move, with the secrets from's side has).
	restore bool
	// From a backup (a restore, or a move through a backup): its
	// repository and the service's snapshot, whose databases' dumps are
	// loaded once its files are in place. where says where it is, for the
	// log; backupID is a restore's backup, for restoring it again.
	repo            *backup.Repo
	snap            backup.Snapshot
	where, backupID string

	// stopped: the service is stopped on from, so a failure starts it
	// there again.
	stopped bool
	// kept is where the data to had before went (CommitData).
	kept []string
}

// startAgainIfFailed starts the service again where it ran if the job
// failed (err) after stopping it there. Deferred by each flow.
func (j *transferJob) startAgainIfFailed(ctx context.Context, err error) {
	if err == nil || !j.stopped {
		return
	}
	j.act.Step("Starting %s again%s", j.title, j.m.onServer(j.from))
	if serr := j.src.StartService(context.WithoutCancel(ctx), j.name); serr != nil {
		j.act.Printf("%v", serr)
	}
}

// stop stops the service where it runs.
func (j *transferJob) stop(ctx context.Context) error {
	j.act.Step("Stopping %s%s", j.title, j.m.onServer(j.from))
	if err := j.src.StopService(ctx, j.name); err != nil {
		return err
	}
	j.stopped = true
	return nil
}

// copyFromBackup streams the service's folders from the snapshot into
// to's staging folders, each emptied first of anything an earlier attempt
// left. A folder the snapshot doesn't have (e.g. no media folder) is
// skipped.
func (j *transferJob) copyFromBackup(ctx context.Context) error {
	for dir, path := range j.m.Lab.Paths.ServiceDataDirs(j.name) {
		if !slices.ContainsFunc(j.snap.Paths, func(p string) bool { return fsutil.Within(p, path) }) {
			continue
		}
		if err := j.dst.PruneData(ctx, j.name, dir, nil); err != nil {
			return err
		}
		strip := len(strings.Split(strings.Trim(path, "/"), "/"))
		err := pipe(ctx, func(w io.Writer) error { return j.repo.Dump(ctx, j.snap.ID, path, w) },
			func(r io.Reader) error { return j.dst.ImportData(ctx, j.name, dir, r, strip) })
		if err != nil && !errors.Is(err, backup.ErrNotInSnapshot) {
			return err
		}
	}
	return nil
}

// bringSecrets puts the service's own secrets on to: a restore's from the
// backup (or, if it has none, those from has), a move's from from.
func (j *transferJob) bringSecrets(ctx context.Context) error {
	var secrets map[string][]byte
	var err error
	if j.restore {
		j.act.Step("Putting its secrets in place (e.g. database passwords)")
		secrets, err = j.backedUpSecrets(ctx)
		if err != nil && j.src != nil {
			j.act.Printf("%v; using the ones %s has", err, j.m.serverName(j.from))
			secrets, err = j.src.ExportSecrets(ctx, j.name)
		}
	} else {
		j.act.Step("Copying its secrets (e.g. database passwords)")
		secrets, err = j.src.ExportSecrets(ctx, j.name)
	}
	if err != nil {
		return err
	}
	if secrets, err = j.m.ownSecrets(j.act, j.name, secrets); err != nil {
		return err
	}
	return j.dst.ImportSecrets(ctx, secrets)
}

// backedUpSecrets reads the secrets saved with the service's backup.
func (j *transferJob) backedUpSecrets(ctx context.Context) (map[string][]byte, error) {
	data, err := j.repo.DumpFile(ctx, j.snap.ID, backup.SecretsFile(j.m.Lab.Paths, j.name))
	if err != nil {
		return nil, fmt.Errorf("the backup has no secrets for %s", j.title)
	}
	var secrets map[string][]byte
	if err := json.Unmarshal(data, &secrets); err != nil {
		return nil, fmt.Errorf("reading the backup's secrets for %s: %w", j.title, err)
	}
	return secrets, nil
}

// commit puts the copied data in place on to; what was there is kept,
// from before a restore or a move.
func (j *transferJob) commit(ctx context.Context) error {
	j.act.Step("Putting the data in place%s", j.m.onServer(j.to))
	why := kept.BeforeMove
	if j.restore {
		why = kept.BeforeRestore
	}
	aside, err := j.dst.CommitData(ctx, j.name, why)
	j.kept = aside
	for _, k := range aside {
		j.act.Printf("The data that was there is kept at %s until you delete it", k)
	}
	return err
}

// switchTo places the service on to, and returns when, for waitApplied.
// It no longer runs on from, so a failure from here on doesn't start it
// there again.
func (j *transferJob) switchTo() (time.Time, error) {
	switched := time.Now()
	if err := j.m.switchHost(j.name, j.to); err != nil {
		return switched, err
	}
	j.stopped = false
	return switched, nil
}

// setAsideOld renames the old copy on from, which it moved away from.
func (j *transferJob) setAsideOld(ctx context.Context) {
	aside, err := j.src.SetAsideData(ctx, j.name, "to-"+j.m.nameOf(j.to))
	if err != nil {
		j.act.Printf("Couldn't set the old copy aside on %s: %v", j.m.nameOf(j.from), err)
	}
	for _, a := range aside {
		j.act.Printf("The old copy will stay on %s at %s until you delete it", j.m.nameOf(j.from), a)
	}
}
