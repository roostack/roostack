package manager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/s3api"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// Rolling a server's backups back (SPEC.md §10.10). A server that's taken
// over can hide its backups at online storage that keeps old versions
// (Backblaze B2, or a versioned S3 bucket), and overwrite their files
// with new versions, but not erase what was there (docs/ASSUMPTIONS.md
// N4). With the account's key, the controller puts every file in the
// server's folder back as it was at a time the Owner chooses: a file
// hidden or overwritten since gets its version from then copied back on
// top, and a file added since is hidden. Nothing is erased, so a rollback
// can itself be rolled back, and it works with a key that can't erase
// old versions. restic's lock files are left alone; stale ones are
// cleared afterwards (restic unlock). The Owner first checks what it
// would change (a dry run), then confirms; it runs as a maintenance job.

// Kinds of change in a rollback.
const (
	rollUnhide  = "unhide"   // hidden since: its version from then comes back
	rollPutBack = "put back" // overwritten since: its version from then comes back
	rollHide    = "hide"     // added since: hidden
)

// rollbackChange is one file's change.
type rollbackChange struct {
	Key, Kind string
	// Version is the version to copy back (not for rollHide).
	Version string
	// Folder is restic's folder it's in ("data", "snapshots", …, or
	// "config").
	Folder string
}

// RollbackPlan is what rolling a server's backups back to a time
// changes, as checked.
type RollbackPlan struct {
	ServerID, Ref string
	Before        time.Time
	Checked       time.Time
	// Files is how many files the folder has, or had; Changed how many
	// change.
	Files, Changed int
	// Summary says what changes, for people.
	Summary []string
	changes []rollbackChange
}

// SummaryText is Summary as one paragraph.
func (p *RollbackPlan) SummaryText() string { return strings.Join(p.Summary, " ") }

// Empty reports whether nothing changed since then.
func (p *RollbackPlan) Empty() bool { return p.Changed == 0 }

// lines say what rolling back changes, for people.
func (p *RollbackPlan) lines() []string {
	if len(p.changes) == 0 {
		return []string{"Nothing in this backup has changed since then."}
	}
	var out []string
	for _, k := range []struct{ kind, since, then string }{
		{rollUnhide, "hidden since then", "come back"},
		{rollPutBack, "overwritten since then", "are put back as they were"},
		{rollHide, "added since then", "are hidden"},
	} {
		if what, n := p.describe(k.kind); n > 0 {
			then := k.then
			if n == 1 {
				then = map[string]string{"come back": "comes back", "are put back as they were": "is put back as it was",
					"are hidden": "is hidden"}[then]
			}
			out = append(out, fmt.Sprintf("%s %s %s.", words.Capitalise(what), k.since, then))
		}
	}
	return out
}

// describe names the files of one kind of change, backups (restic's
// snapshots) first, as people know them, and counts them.
func (p *RollbackPlan) describe(kind string) (string, int) {
	snaps, config, other := 0, false, 0
	for _, c := range p.changes {
		switch {
		case c.Kind != kind:
		case c.Folder == "snapshots":
			snaps++
		case c.Folder == "config":
			config = true
		default:
			other++
		}
	}
	var parts []string
	switch snaps {
	case 0:
	case 1:
		parts = append(parts, "1 backup (a snapshot)")
	default:
		parts = append(parts, fmt.Sprintf("%d backups (snapshots)", snaps))
	}
	if config {
		parts = append(parts, "the repository's config file")
	}
	switch {
	case other == 1 && len(parts) == 0:
		parts = append(parts, "1 file")
	case other == 1:
		parts = append(parts, "1 other file")
	case other > 1 && len(parts) == 0:
		parts = append(parts, fmt.Sprintf("%d files", other))
	case other > 1:
		parts = append(parts, fmt.Sprintf("%d other files", other))
	}
	n := snaps + other
	if config {
		n++
	}
	return words.And(parts), n
}

// rollbackChecks are the latest checks, by activity.BackupRollbackFor: a
// rollback runs only after one for the same time (the Owner saw what it
// changes).
type rollbackChecks struct {
	sync.Mutex
	plans map[string]*RollbackPlan
}

// rollbackCheckAge is how long a check lets a rollback run.
const rollbackCheckAge = time.Hour

// rollbackTarget finds what a server's backups at ref need for a
// rollback: the account (it must keep versions) and the server's folder.
func (m *Manager) rollbackTarget(ctx context.Context, serverID, ref string, before time.Time) (S3Provider, string, error) {
	if m.BackupStore == nil {
		return S3Provider{}, "", ErrNotFound
	}
	t, ok := m.targetAt(ctx, serverID, ref)
	if !ok {
		return S3Provider{}, "", ErrNotFound
	}
	d, err := m.destinationOf(ctx, ref)
	if err != nil || d.kind != destS3 {
		return S3Provider{}, "", ErrNotFound
	}
	if !m.access(ctx, serverID, d, t).Versioned {
		return S3Provider{}, "", usererr.New("%s doesn't keep old versions, so there's nothing to roll back to.", d.provider.Name)
	}
	switch {
	case before.IsZero():
		return S3Provider{}, "", usererr.New("Choose the date and time to go back to.")
	case before.After(time.Now()):
		return S3Provider{}, "", usererr.New("Choose a time that has passed.")
	case !t.Created.IsZero() && before.Before(t.Created):
		return S3Provider{}, "", usererr.New("Backups here started on %s: choose a time after that.", t.Created.Local().Format("2 January 2006 15:04"))
	}
	labID, err := m.labID(ctx)
	if err != nil {
		return S3Provider{}, "", err
	}
	return d.s3, backup.ServerFolder(d.s3.Prefix, labID, serverID) + "/", nil
}

// CheckRollback works out, in the background, what rolling a server's
// backups at ref back to before changes (a log under
// activity.BackupRollbackFor), for RollBack to do once the Owner has seen
// it.
func (m *Manager) CheckRollback(ctx context.Context, serverID, ref string, before time.Time) error {
	s3, folder, err := m.rollbackTarget(ctx, serverID, ref, before)
	if err != nil {
		return err
	}
	kind := activity.BackupRollbackFor(serverID, ref)
	act := m.Activity.Start(kind, "Checking what going back to "+before.Local().Format("2 January 15:04")+" changes")
	go func() {
		ctx := context.Background()
		act.Step("Listing every version of the files in %s's folder", m.serverName(serverID))
		plan, err := m.rollbackPlan(ctx, m.s3Client(s3), folder, before)
		if err == nil {
			plan.ServerID, plan.Ref, plan.Checked = serverID, ref, time.Now()
			for _, l := range plan.Summary {
				act.Printf("%s", l)
			}
			m.rollbacks.Lock()
			if m.rollbacks.plans == nil {
				m.rollbacks.plans = map[string]*RollbackPlan{}
			}
			m.rollbacks.plans[kind] = plan
			m.rollbacks.Unlock()
			act.Step("Checked %d files", plan.Files)
		}
		act.Finish(err)
	}()
	return nil
}

// RollbackChecked is the latest check of rolling a server's backups at
// ref back, while it can still be acted on (nil if none).
func (m *Manager) RollbackChecked(serverID, ref string) *RollbackPlan {
	m.rollbacks.Lock()
	defer m.rollbacks.Unlock()
	p := m.rollbacks.plans[activity.BackupRollbackFor(serverID, ref)]
	if p == nil || time.Since(p.Checked) > rollbackCheckAge {
		return nil
	}
	return p
}

// RollBack rolls a server's backups at ref back to before, as checked
// (CheckRollback, for the same time, within the hour), as a maintenance
// job with its log under activity.BackupRollbackFor.
func (m *Manager) RollBack(ctx context.Context, serverID, ref string, before time.Time) error {
	if _, _, err := m.rollbackTarget(ctx, serverID, ref, before); err != nil {
		return err
	}
	checked := m.RollbackChecked(serverID, ref)
	if checked == nil || !checked.Before.Equal(before) {
		return usererr.New("Check what going back to that time changes first.")
	}
	if m.Submit == nil {
		return errors.New("no maintenance coordinator")
	}
	m.rollbacks.Lock()
	delete(m.rollbacks.plans, activity.BackupRollbackFor(serverID, ref))
	m.rollbacks.Unlock()
	m.Submit(m.rollbackJob(serverID, ref, before))
	return nil
}

// rollbackJob rolls the backups back. What changed is worked out again:
// the server may have backed up since the check.
func (m *Manager) rollbackJob(serverID, ref string, before time.Time) maintenance.Job {
	name := m.nameOf(serverID)
	return maintenance.Job{Name: "rolling back " + name + "'s backups", Run: func(ctx context.Context, _ *maintenance.Handle) (err error) {
		act := m.Activity.Start(activity.BackupRollbackFor(serverID, ref),
			"Going back to "+before.Local().Format("2 January 15:04")+" in "+name+"'s backups")
		defer func() { act.Finish(err) }()
		s3, folder, err := m.rollbackTarget(ctx, serverID, ref, before)
		if err != nil {
			return err
		}
		cl := m.s3Client(s3)
		act.Step("Listing every version of the files in %s's folder", name)
		plan, err := m.rollbackPlan(ctx, cl, folder, before)
		if err != nil {
			return err
		}
		for _, l := range plan.Summary {
			act.Printf("%s", l)
		}
		failed := 0
		for i, c := range plan.changes {
			if i%200 == 0 {
				act.Step("Putting files back as they were: %d of %d", i, len(plan.changes))
			}
			var err error
			if c.Kind == rollHide {
				err = cl.Delete(ctx, c.Key, "")
			} else {
				err = cl.CopyBack(ctx, c.Key, c.Version)
			}
			if err != nil {
				failed++
				if failed <= 10 {
					act.Printf("%s: %v", strings.TrimPrefix(c.Key, folder), err)
				}
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d files couldn't be put back as they were; going back to the same time again tries them again", failed, len(plan.changes))
		}
		// Old lock files don't come back, but a backup that stopped when
		// the server was taken over may have left one (N1).
		t, _ := m.targetAt(ctx, serverID, ref)
		repo, err := m.adminRepo(ctx, t)
		if err != nil {
			return err
		}
		act.Step("Checking the backups there")
		if n, err := repo.Unlock(ctx); err != nil {
			act.Printf("Couldn't clear old locks: %s", words.FirstLine(strings.TrimSpace(err.Error())))
		} else if n > 0 {
			act.Printf("Cleared %d old %s", n, map[bool]string{true: "lock", false: "locks"}[n == 1])
		}
		switch err := repo.Check(ctx, ""); {
		case errors.Is(err, backup.ErrLocked):
			act.Printf("A backup is running there, so they weren't checked now; the weekly check will.")
		case err != nil:
			return fmt.Errorf("the backups there don't check out: %w", err)
		}
		snaps, err := repo.Snapshots(ctx)
		if err != nil {
			return err
		}
		switch {
		case len(snaps) == 1:
			act.Printf("1 backup there now, from %s.", snaps[0].Time.Local().Format("2 January 15:04"))
		case len(snaps) > 1:
			newest := slices.MaxFunc(snaps, func(a, b backup.Snapshot) int { return a.Time.Compare(b.Time) })
			act.Printf("%d backups there now, the newest from %s.", len(snaps), newest.Time.Local().Format("2 January 15:04"))
		}
		act.Step("%s's backups are back as they were at %s", name, before.Local().Format("2 January 15:04"))
		return nil
	}}
}

// rollbackPlan lists every version under folder and works out each
// file's change: those changed since before go back to how they were
// then. The order is safe for anyone reading the backups meanwhile:
// what's brought back goes config, keys, data, index, then snapshots;
// what's hidden goes the other way.
func (m *Manager) rollbackPlan(ctx context.Context, cl *s3api.Client, folder string, before time.Time) (*RollbackPlan, error) {
	plan := &RollbackPlan{Before: before}
	// Each file's versions, gathered: Backblaze lists a page's delete
	// markers apart from its versions, not each file's together
	// (docs/ASSUMPTIONS.md N9).
	byKey := map[string][]s3api.Version{}
	err := cl.Versions(ctx, folder, func(v s3api.Version) error {
		byKey[v.Key] = append(byKey[v.Key], v)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing the files' versions: %w", err)
	}
	for key, versions := range byKey {
		// Newest first; the latest is newest even at the same time.
		slices.SortStableFunc(versions, func(a, b s3api.Version) int {
			if c := b.Modified.Compare(a.Modified); c != 0 {
				return c
			}
			switch {
			case a.Latest && !b.Latest:
				return -1
			case b.Latest && !a.Latest:
				return 1
			}
			return 0
		})
		plan.Files++
		if c, ok := rollbackChangeOf(folder, key, versions, before); ok {
			plan.changes = append(plan.changes, c)
		}
	}
	slices.SortFunc(plan.changes, func(a, b rollbackChange) int { return strings.Compare(a.Key, b.Key) })
	rank := map[string]int{"config": 0, "keys": 1, "data": 2, "index": 3, "snapshots": 4}
	slices.SortStableFunc(plan.changes, func(a, b rollbackChange) int {
		ah, bh := a.Kind == rollHide, b.Kind == rollHide
		switch {
		case ah != bh: // bring back first, then hide
			if ah {
				return 1
			}
			return -1
		case ah:
			return cmp.Compare(rank[b.Folder], rank[a.Folder])
		}
		return cmp.Compare(rank[a.Folder], rank[b.Folder])
	})
	plan.Changed, plan.Summary = len(plan.changes), plan.lines()
	return plan, nil
}

// rollbackChangeOf is one file's change, from its versions (newest
// first), if it changed since before.
func rollbackChangeOf(folder, key string, versions []s3api.Version, before time.Time) (rollbackChange, bool) {
	rel := strings.TrimPrefix(key, folder)
	dir, _, _ := strings.Cut(rel, "/")
	c := rollbackChange{Key: key, Folder: dir}
	if dir == "locks" || len(versions) == 0 {
		return c, false
	}
	now := versions[0]
	if !now.Modified.After(before) {
		return c, false
	}
	var then *s3api.Version
	for i := range versions {
		if !versions[i].Modified.After(before) {
			then = &versions[i]
			break
		}
	}
	switch {
	case then == nil || then.DeleteMarker:
		// It wasn't there then.
		if now.DeleteMarker {
			return c, false
		}
		c.Kind = rollHide
	case now.DeleteMarker:
		c.Kind, c.Version = rollUnhide, then.VersionID
	case now.ETag != "" && now.ETag == then.ETag && now.Size == then.Size:
		return c, false // as it was already (rolled back before)
	default:
		c.Kind, c.Version = rollPutBack, then.VersionID
	}
	return c, true
}
