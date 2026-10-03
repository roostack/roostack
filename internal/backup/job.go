package backup

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/units"
)

// JobName is the coordinator job name for backups (used to resume them
// after a reboot).
const JobName = "backup"

// Plan is everything one lab backup covers (SPEC.md §6.9).
type Plan struct {
	Paths    []string
	Excludes []string
	Prepare  []generate.PrepareStep
}

// LabPlan combines the lab's own state with each service's backup plan.
//
// Included: /var/lib/roostack (desired state, Caddy config and certificates)
// and the Podman secrets store, since generated passwords are needed to use
// restored data. Excluded: the rest of the roostack home directory (container
// images, which can be downloaded again), the live SQLite database (a
// consistent copy is taken first), and the backup key itself (with the
// new and old keys of a change).
func LabPlan(paths config.Paths, services []*generate.Output) Plan {
	secrets := filepath.Join(paths.HomeDir, ".local", "share", "containers", "storage", "secrets")
	p := Plan{
		Paths: []string{paths.StateDir, secrets},
		Excludes: append(excludeAllBut(paths.HomeDir, secrets),
			filepath.Join(paths.StateDir, "roostack.db"),
			filepath.Join(paths.StateDir, "roostack.db-wal"),
			filepath.Join(paths.StateDir, "roostack.db-shm"),
			KeyFile(paths),
			// A change's new and old keys and its progress (internal/manager),
			// and a half-written key: no backup holds a key that opens it,
			// and a restored lab isn't left half-way through a change.
			KeyFile(paths)+".*",
		),
	}
	for _, s := range services {
		p.Paths = append(p.Paths, s.Backup.Include...)
		p.Excludes = append(p.Excludes, s.Backup.Exclude...)
		p.Prepare = append(p.Prepare, s.Backup.Prepare...)
	}
	slices.Sort(p.Paths)
	p.Paths = slices.Compact(p.Paths)
	return p
}

// excludeAllBut returns restic exclude patterns that skip everything under
// root except keep (a descendant). restic excludes a directory's contents
// even when a path inside it is given explicitly, so each level is excluded
// with a wildcard and the next directory towards keep is re-included with a
// "!" pattern.
func excludeAllBut(root, keep string) []string {
	rel, err := filepath.Rel(root, keep)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return []string{root}
	}
	var out []string
	dir := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		out = append(out, filepath.Join(dir, "*"))
		dir = filepath.Join(dir, part)
		out = append(out, "!"+dir)
	}
	return out
}

// KeyFile holds the repository password: the recovery key.
func KeyFile(paths config.Paths) string { return filepath.Join(paths.StateDir, "backup-key") }

// WireGuardKeysFile is where Roostack keeps a copy of its WireGuard keys for
// backups (readable by Roostack only, like the lab CA's key).
func WireGuardKeysFile(paths config.Paths) string {
	return filepath.Join(paths.StateDir, "wireguard-keys.json")
}

// StateDumpFile is where the consistent copy of Roostack's own database goes.
func StateDumpFile(paths config.Paths) string {
	return filepath.Join(paths.StateDir, "db-dump", "roostack.db")
}

// JobDeps are what a backup job needs.
type JobDeps struct {
	Repo     *Repo
	Preparer *Preparer
	// DumpState writes a consistent copy of Roostack's SQLite database.
	DumpState func(ctx context.Context, path string) error
	Paths     config.Paths
	Host      string
	// Plan returns the current plan (services may change between runs).
	Plan func() (Plan, error)
	// OnDone is called with the result (e.g. to record it for the UI).
	OnDone func(*Summary, error)
	// Precheck runs first, e.g. that the backup drive is connected
	// (CheckDrive). Optional.
	Precheck func() error
	// Copies are extra copies of the backup, somewhere else (SPEC.md §6.9,
	// 3-2-1): each gets its own backup of the same files after the main
	// one, so each can be read on its own.
	Copies []Copy
	// OnCopy is called with each copy's result.
	OnCopy func(c Copy, started time.Time, sum *Summary, err error)
	// Log shows the run's steps live in the UI (optional, per run).
	Log *activity.Log
}

// Copy is an extra copy of a server's backups.
type Copy struct {
	// Name identifies it (the destination's reference); Title is for people.
	Name, Title string
	Repo        *Repo
	// Precheck runs first, e.g. that its drive is connected (optional).
	Precheck func() error
}

// Reporter receives progress while a backup runs. *maintenance.Handle
// implements it.
type Reporter interface {
	SetInterruptible(bool)
	SetProgress(int)
}

// NewJob returns the coordinator job for a full backup (SPEC.md §3.2).
func NewJob(d JobDeps) maintenance.Job {
	return maintenance.Job{
		Name:      JobName,
		Resumable: true,
		Run: func(ctx context.Context, h *maintenance.Handle) error {
			_, err := Run(ctx, d, h)
			return err
		},
	}
}

// Run performs a full backup. The prepare steps are not interruptible; the
// upload is, and resumes after a reboot.
func Run(ctx context.Context, d JobDeps, r Reporter) (summary *Summary, err error) {
	defer func() {
		if d.OnDone != nil {
			d.OnDone(summary, err)
		}
	}()
	if d.Precheck != nil {
		if err := d.Precheck(); err != nil {
			return nil, err
		}
	}
	plan, err := d.Plan()
	if err != nil {
		return nil, err
	}
	r.SetInterruptible(false)
	d.Log.Step("Saving a copy of Roostack's own database")
	if err := d.DumpState(ctx, StateDumpFile(d.Paths)); err != nil {
		return nil, fmt.Errorf("copying Roostack database: %w", err)
	}
	for _, step := range plan.Prepare {
		d.Log.Step("Saving a consistent copy of %s's %s database", prepareService(step), step.Type)
		d.Log.Printf("%s → %s", cmp.Or(step.Container, step.Source), step.Target)
		if err := d.Preparer.Run(ctx, step); err != nil {
			return nil, fmt.Errorf("preparing %s: %w", step.Target, err)
		}
	}
	// Skip paths that don't exist yet (e.g. no Podman secrets on a fresh
	// install); restic treats a missing source as an error.
	var paths []string
	for _, p := range plan.Paths {
		if _, err := os.Stat(p); err == nil {
			paths = append(paths, p)
		}
	}
	r.SetInterruptible(true)
	summary, err = upload(ctx, d, d.Repo, "", paths, plan.Excludes, r)
	// The copies go ahead even if the main destination failed: that's
	// what they're for.
	for _, c := range d.Copies {
		started := time.Now()
		var csum *Summary
		cerr := error(nil)
		if c.Precheck != nil {
			cerr = c.Precheck()
		}
		if cerr == nil {
			csum, cerr = upload(ctx, d, c.Repo, c.Title, paths, plan.Excludes, r)
		}
		if cerr != nil {
			d.Log.Printf("The copy at %s didn't finish: %v", c.Title, cerr)
		}
		if d.OnCopy != nil {
			d.OnCopy(c, started, csum, cerr)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return summary, err
}

// reachTimeout limits opening a repository: restic keeps retrying one it
// can't reach (a server that's off), which would hold up every backup
// after it.
var reachTimeout = 2 * time.Minute

// upload backs the paths up to one repository (to is "" for the main one).
func upload(ctx context.Context, d JobDeps, repo *Repo, to string, paths, excludes []string, r Reporter) (*Summary, error) {
	r.SetProgress(-1) // until this upload reports its own
	if to == "" {
		d.Log.Step("Opening the backup storage")
	} else {
		d.Log.Step("Copying to %s", to)
	}
	d.Log.Printf("Repository: %s", repo.URL)
	ictx, cancel := context.WithTimeout(ctx, reachTimeout)
	err := repo.Init(ictx)
	cancel()
	if ictx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
		return nil, fmt.Errorf("couldn't reach %s in %s: is it switched on and connected?", cmp.Or(to, "the backup storage"), reachTimeout)
	}
	if err != nil {
		return nil, err
	}
	if to == "" {
		d.Log.Step("Uploading changes")
		for _, p := range paths {
			d.Log.Printf("Including %s", p)
		}
	}
	lastTen := -1
	sum, err := repo.Backup(ctx, BackupOptions{
		Paths: paths, Excludes: excludes, Tags: []string{"roostack"}, Host: d.Host,
		OnProgress: func(p Progress) {
			r.SetProgress(int(p.PercentDone))
			if ten := int(p.PercentDone) / 10; ten > lastTen {
				lastTen = ten
				d.Log.Printf("%3d%%  %s of %s, about %s left", int(p.PercentDone), Bytes(p.BytesDone),
					Bytes(p.TotalBytes), (time.Duration(p.SecondsRemaining) * time.Second).String())
			}
		},
	})
	if sum != nil {
		d.Log.Step("Backed up: %d new files, %s added (snapshot %s)", sum.FilesNew, Bytes(sum.DataAdded), sum.SnapshotID)
		// The space it all takes there, for the plan (SPEC.md §6.9).
		if stored, serr := repo.Stored(ctx); serr == nil {
			sum.Stored = stored
			d.Log.Printf("The backups there take %s", Bytes(stored))
		} else {
			d.Log.Printf("Couldn't tell how much space the backups there take: %v", serr)
		}
	}
	return sum, err
}

// prepareService names the service a prepare step belongs to, from its
// dump path (<services dir>/<service>/db-dump/<file>).
func prepareService(step generate.PrepareStep) string {
	return filepath.Base(filepath.Dir(filepath.Dir(step.Target)))
}

// Bytes formats a size for people, e.g. "1.2 GB" (units.Bytes).
func Bytes(n uint64) string { return units.Bytes(int64(n)) }
