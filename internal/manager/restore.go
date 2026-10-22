package manager

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
)

// Restoring a service from any of the lab's backups, onto any server
// (SPEC.md §10.10, "Restoring across the lab"). Each backup has a snapshot
// per service, so the controller finds a service's backups in every
// server's repositories (opening them with the recovery key, as for
// pruning) and streams the chosen one to the server it goes onto.

// ServiceBackup is one backup of a service, somewhere in the lab.
type ServiceBackup struct {
	// Ref is the repository: "backup:<server>" (a server's main backup)
	// or "copy:<server>:<destination>" (an extra copy).
	Ref string
	// ServerID and ServerName: the server that made it.
	ServerID, ServerName string
	// Where is its destination, for people (e.g. "USB drive BACKUP").
	Where string
	// Main: the server's main backup, which restores read first.
	Main     bool
	Snapshot backup.Snapshot
}

// ID identifies the backup in a form.
func (b ServiceBackup) ID() string { return b.Ref + "@" + b.Snapshot.ID }

// listTimeout bounds reading one repository's list of backups: one that
// can't be reached (a server that's off) mustn't hold the page up.
var listTimeout = 30 * time.Second

// BackupMoment is one backup run of a service: the same moment, in each
// place that holds it (the server's main backup and its copies).
type BackupMoment struct {
	ServerID, ServerName string
	// Time is when the run started (its earliest snapshot).
	Time time.Time
	// Places hold it, the one to read first first: the main backup
	// (usually at home, so quickest), then copies.
	Places []ServiceBackup
}

// ID identifies the moment in a form: its first place's backup.
func (b BackupMoment) ID() string { return b.Places[0].ID() }

// sameRunWithin groups snapshots made before runs were tagged: a copy is
// made minutes after the main backup.
const sameRunWithin = time.Hour

// ServiceBackups lists a service's backup moments in every repository the
// controller can open, newest first (the newest is suggested). It also
// says which repositories couldn't be read, for people.
func (m *Manager) ServiceBackups(ctx context.Context, name string) ([]BackupMoment, []string, error) {
	if _, _, err := m.installedService(name); err != nil {
		return nil, nil, err
	}
	repos, err := m.keyRepos(ctx)
	if err != nil {
		return nil, nil, err
	}
	keyFile := backup.KeyFile(m.Lab.Paths)
	found := make([][]ServiceBackup, len(repos))
	problems := make([]string, len(repos))
	var wg sync.WaitGroup
	for i, kr := range repos {
		wg.Go(func() {
			where, main := m.repoTitle(ctx, kr)
			repo, err := kr.open(keyFile)
			if err == nil {
				lctx, cancel := context.WithTimeout(ctx, listTimeout)
				var snaps []backup.Snapshot
				snaps, err = repo.Snapshots(lctx, backup.TagRoostack, backup.ServiceTag(name))
				cancel()
				for _, s := range snaps {
					found[i] = append(found[i], ServiceBackup{Ref: kr.Ref, ServerID: kr.ServerID,
						ServerName: m.nameOf(kr.ServerID), Where: where, Main: main, Snapshot: s})
				}
			}
			if err != nil {
				problems[i] = fmt.Sprintf("Couldn't read %s backups at %s: %s", m.backupsOf(kr.ServerID), where, firstLine(err.Error()))
			}
		})
	}
	wg.Wait()
	var out []ServiceBackup
	for _, f := range found {
		out = append(out, f...)
	}
	var notes []string
	for _, p := range problems {
		if p != "" {
			notes = append(notes, p)
		}
	}
	return moments(out), notes, nil
}

// moments groups backups into runs (by their run tag, or for untagged
// ones, by server and time), newest first, each with its main backup
// first.
func moments(backups []ServiceBackup) []BackupMoment {
	// Oldest first, so each run starts at its earliest snapshot.
	slices.SortFunc(backups, func(a, b ServiceBackup) int { return a.Snapshot.Time.Compare(b.Snapshot.Time) })
	var out []BackupMoment
	byRun := map[string]int{}
	for _, b := range backups {
		run := b.Snapshot.Run()
		i, ok := byRun[b.ServerID+" "+run]
		if run == "" {
			ok = false
			for j := range out {
				if out[j].ServerID == b.ServerID && out[j].Places[0].Snapshot.Run() == "" &&
					b.Snapshot.Time.Sub(out[j].Time) < sameRunWithin && !slices.ContainsFunc(out[j].Places,
					func(p ServiceBackup) bool { return p.Ref == b.Ref }) {
					i, ok = j, true
				}
			}
		}
		if !ok {
			out = append(out, BackupMoment{ServerID: b.ServerID, ServerName: b.ServerName, Time: b.Snapshot.Time})
			i = len(out) - 1
			if run != "" {
				byRun[b.ServerID+" "+run] = i
			}
		}
		out[i].Places = append(out[i].Places, b)
	}
	for _, mo := range out {
		slices.SortStableFunc(mo.Places, func(a, b ServiceBackup) int {
			switch {
			case a.Main == b.Main:
				return 0
			case a.Main:
				return -1
			}
			return 1
		})
	}
	slices.SortFunc(out, func(a, b BackupMoment) int { return b.Time.Compare(a.Time) })
	return out
}

// repoTitle names a repository for people, and says whether it's a
// server's main backup.
func (m *Manager) repoTitle(ctx context.Context, kr keyRepo) (string, bool) {
	if strings.HasPrefix(kr.Ref, "backup:") {
		_, title, _ := m.BackupTargetOf(ctx, kr.ServerID)
		return cmp.Or(title, "its main backup"), true
	}
	for _, c := range m.BackupCopiesOf(ctx, kr.ServerID) {
		if kr.Ref == "copy:"+kr.ServerID+":"+c.Ref {
			return c.Title, false
		}
	}
	return "an extra copy", false
}

// RestoreService restores a service from one of its backups (an ID from
// ServiceBackups) onto a server ("" for where it runs).
//
// Back onto the server it runs on, from that server's main backup, the
// server restores it itself (node.Restores: its data is kept aside as
// before a restore). Otherwise the controller does it in the background
// (activity.Restore): the service stops where it runs, the backup's data
// and secrets go to the chosen server, which starts it and loads its
// databases; data already there and the old server's copy are kept aside.
func (m *Manager) RestoreService(ctx context.Context, name, id, to string) error {
	p, in, err := m.installedService(name)
	if err != nil {
		return err
	}
	host := cmp.Or(in.Host, m.Lab.NodeID)
	to = cmp.Or(to, host)
	if in.Waiting && to == host {
		to = m.Lab.NodeID // its server is gone: here, unless chosen
	}
	ref, snapID, ok := strings.Cut(id, "@")
	if !ok || snapID == "" {
		return formErrorf("Choose a backup to restore.")
	}
	dst, ok := m.node(to).(node.Mover)
	if !ok {
		return formErrorf("Choose a server in this lab.")
	}
	if r, ok := m.node(to).(RemoteServer); ok && r.Settings() == nil {
		return formErrorf("Roostack can't reach %s right now.", r.Identity().Name)
	}
	if ref == "backup:"+to && to == host && !in.Waiting {
		if x := m.node(to).Restores(); x != nil {
			if err := x.Restore(ctx, name, snapID); err != nil {
				return err
			}
			// Held stopped if its databases don't load (SPEC.md §6.9).
			go m.watchRestore(name, to, id, false)
			return nil
		}
	}
	repos, err := m.keyRepos(ctx)
	if err != nil {
		return err
	}
	kr, ok := findRepo(repos, ref)
	if !ok {
		return formErrorf("That backup isn't there any more. Choose another.")
	}
	repo, err := kr.open(backup.KeyFile(m.Lab.Paths))
	if err != nil {
		return err
	}
	snap, err := repo.ServiceSnapshot(ctx, name, snapID)
	if err != nil {
		return formErrorf("That backup isn't there any more. Choose another.")
	}
	m.mu.Lock()
	if m.restoring == nil {
		m.restoring = map[string]bool{}
	}
	busy := m.restoring[name]
	m.restoring[name] = true
	m.mu.Unlock()
	if busy {
		return backup.ErrRestoreQueued
	}
	where, _ := m.repoTitle(ctx, kr)
	act := m.Activity.Start(activity.Restore, "Restoring "+p.Title)
	go func() {
		ctx := context.Background()
		err := m.restoreFrom(ctx, act, restoreJob{name: name, title: cmp.Or(in.Title, p.Title), host: host, to: to, backup: id,
			repo: repo, snap: *snap, from: fmt.Sprintf("%s backup at %s", m.backupsOf(kr.ServerID), where)}, dst)
		if err != nil {
			m.Log.Error("restoring a service", "service", name, "err", err)
		}
		m.mu.Lock()
		delete(m.restoring, name)
		m.mu.Unlock()
		if err == nil && in.Waiting {
			m.leaveIfDone(ctx, host) // its last waiting service?
		}
		act.Finish(err)
	}()
	return nil
}

// Restoring reports whether the controller is restoring a service.
func (m *Manager) Restoring(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.restoring[name]
}

// restoreJob is a restore the controller does.
type restoreJob struct {
	name, title string
	// host is where the service runs now; to is where it goes.
	host, to string
	repo     *backup.Repo
	snap     backup.Snapshot
	from     string // where the backup is, for the log
	backup   string // the backup's ID, for restoring it again
}

// restoreFrom streams a service's backup to the server it goes onto
// (RestoreService).
func (m *Manager) restoreFrom(ctx context.Context, act *activity.Log, j restoreJob, dst node.Mover) (err error) {
	act.Printf("The backup from %s, in %s (%s)", j.snap.Time.Local().Format("Mon 2 Jan 15:04"), j.from, j.snap.Short)
	src, _ := m.node(j.host).(node.Mover)
	reachable := src != nil
	if r, ok := m.node(j.host).(RemoteServer); ok && r.Settings() == nil {
		reachable = false
		act.Printf("Roostack can't reach %s, where %s ran: it's restored without stopping it there", m.nameOf(j.host), j.title)
	}
	stopped := false
	defer func() {
		if err != nil && stopped {
			act.Step("Starting %s again%s", j.title, m.onServer(j.host))
			if serr := src.StartService(context.WithoutCancel(ctx), j.name); serr != nil {
				act.Printf("%v", serr)
			}
		}
	}()
	if reachable {
		act.Step("Stopping %s%s", j.title, m.onServer(j.host))
		if err := src.StopService(ctx, j.name); err != nil {
			return err
		}
		stopped = true
	}

	if to := m.onServer(j.to); to == "" {
		act.Step("Copying %s's data from the backup", j.title)
	} else {
		act.Step("Copying %s's data from the backup to %s", j.title, m.nameOf(j.to))
	}
	for dir, path := range []string{filepath.Join(m.Lab.Paths.ServicesDir, j.name), filepath.Join(m.Lab.Paths.MediaDir, j.name)} {
		if !slices.ContainsFunc(j.snap.Paths, func(p string) bool { return p == path || strings.HasPrefix(p, path+"/") }) {
			continue // e.g. no media folder
		}
		// Anything left from an earlier attempt goes first.
		if err := dst.PruneData(ctx, j.name, dir, nil); err != nil {
			return err
		}
		strip := len(strings.Split(strings.Trim(path, "/"), "/"))
		err := pipe(ctx, func(w io.Writer) error { return j.repo.Dump(ctx, j.snap.ID, path, w) },
			func(r io.Reader) error { return dst.ImportData(ctx, j.name, dir, r, strip) })
		if err != nil && !errors.Is(err, backup.ErrNotInSnapshot) {
			return err
		}
	}

	act.Step("Putting its secrets in place (e.g. database passwords)")
	secrets, err := m.backedUpSecrets(ctx, j)
	if err != nil {
		if !reachable {
			return err
		}
		act.Printf("%v; using the ones %s has", err, m.serverName(j.host))
		if secrets, err = src.ExportSecrets(ctx, j.name); err != nil {
			return err
		}
	}
	if secrets, err = m.ownSecrets(act, j.name, secrets); err != nil {
		return err
	}
	if err := dst.ImportSecrets(ctx, secrets); err != nil {
		return err
	}

	act.Step("Putting the data in place%s", m.onServer(j.to))
	kept, err := dst.CommitData(ctx, j.name)
	for _, k := range kept {
		act.Printf("The data that was there is kept at %s until you delete it", k)
	}
	if err != nil {
		return err
	}
	if j.to != j.host {
		act.Step("Setting %s up on %s", j.title, m.nameOf(j.to))
		switched := time.Now()
		if err := m.switchHost(j.name, j.to); err != nil {
			return err
		}
		stopped = false // it runs on the new server now
		if err := waitApplied(ctx, m.node(j.to), switched); err != nil {
			return err
		}
	}
	act.Step("Loading its databases from the backup, and starting it")
	if err := dst.LoadDumps(ctx, j.name); err != nil {
		// Its files are the backup's now: started again on them, without
		// its databases, it would run half-restored (security review of
		// 2026-10-16). It's held stopped until the Owner chooses (SPEC.md
		// §6.9, "A restore that didn't finish").
		stopped = false
		act.Printf("%v", err)
		h := instance.Hold{Backup: j.backup, Taken: j.snap.Time, Error: err.Error()}
		if len(kept) > 0 {
			h.Kept = kept[0]
		}
		if herr := m.holdService(j.name, h); herr != nil {
			act.Printf("Couldn't keep it stopped: %v", herr)
		}
		return errors.New(backup.NotLoadedMsg(j.title, j.snap.Time))
	}
	stopped = false
	if released, err := m.releaseHold(j.name, "Starting "+j.title+": it's restored"); err != nil {
		act.Printf("%v", err)
	} else if released {
		m.audit(ctx, AuditHoldEnded, "Restored "+j.title+", so it's no longer stopped")
	}
	if j.to != j.host && reachable {
		aside, err := src.SetAsideData(ctx, j.name, "to-"+m.nameOf(j.to))
		if err != nil {
			act.Printf("Couldn't set the old copy aside on %s: %v", m.nameOf(j.host), err)
		}
		for _, a := range aside {
			act.Printf("The old copy stays on %s at %s until you delete it", m.nameOf(j.host), a)
		}
	}
	act.Step("%s is back to the backup from %s%s", j.title, j.snap.Time.Local().Format("Mon 2 Jan 15:04"), strings.Replace(m.onServer(j.to), " on", ", on", 1))
	return nil
}

// backedUpSecrets reads the secrets saved with a service's backup.
func (m *Manager) backedUpSecrets(ctx context.Context, j restoreJob) (map[string][]byte, error) {
	data, err := j.repo.DumpFile(ctx, j.snap.ID, backup.SecretsFile(m.Lab.Paths, j.name))
	if err != nil {
		return nil, fmt.Errorf("the backup has no secrets for %s", j.title)
	}
	var secrets map[string][]byte
	if err := json.Unmarshal(data, &secrets); err != nil {
		return nil, fmt.Errorf("reading the backup's secrets for %s: %w", j.title, err)
	}
	return secrets, nil
}
