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

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/maintenance"
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
// Included: /var/lib/mylab (desired state, Caddy config and certificates)
// and the Podman secrets store, since generated passwords are needed to use
// restored data. Excluded: the rest of the mylab home directory (container
// images, which can be downloaded again), the live SQLite database (a
// consistent copy is taken first), and the backup key itself.
func LabPlan(paths config.Paths, services []*generate.Output) Plan {
	secrets := filepath.Join(paths.HomeDir, ".local", "share", "containers", "storage", "secrets")
	p := Plan{
		Paths: []string{paths.StateDir, secrets},
		Excludes: append(excludeAllBut(paths.HomeDir, secrets),
			filepath.Join(paths.StateDir, "mylab.db"),
			filepath.Join(paths.StateDir, "mylab.db-wal"),
			filepath.Join(paths.StateDir, "mylab.db-shm"),
			KeyFile(paths),
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

// StateDumpFile is where the consistent copy of MyLab's own database goes.
func StateDumpFile(paths config.Paths) string {
	return filepath.Join(paths.StateDir, "db-dump", "mylab.db")
}

// JobDeps are what a backup job needs.
type JobDeps struct {
	Repo     *Repo
	Preparer *Preparer
	// DumpState writes a consistent copy of MyLab's SQLite database.
	DumpState func(ctx context.Context, path string) error
	Paths     config.Paths
	Host      string
	// Plan returns the current plan (services may change between runs).
	Plan func() (Plan, error)
	// OnDone is called with the result (e.g. to record it for the UI).
	OnDone func(*Summary, error)
	// Log shows the run's steps live in the UI (optional, per run).
	Log *activity.Log
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
	plan, err := d.Plan()
	if err != nil {
		return nil, err
	}
	r.SetInterruptible(false)
	d.Log.Step("Saving a copy of MyLab's own database")
	if err := d.DumpState(ctx, StateDumpFile(d.Paths)); err != nil {
		return nil, fmt.Errorf("copying MyLab database: %w", err)
	}
	for _, step := range plan.Prepare {
		d.Log.Step("Saving a consistent copy of %s's %s database", prepareService(step), step.Type)
		d.Log.Printf("%s → %s", cmp.Or(step.Container, step.Source), step.Target)
		if err := d.Preparer.Run(ctx, step); err != nil {
			return nil, fmt.Errorf("preparing %s: %w", step.Target, err)
		}
	}
	d.Log.Step("Opening the backup storage")
	d.Log.Printf("Repository: %s", d.Repo.URL)
	if err := d.Repo.Init(ctx); err != nil {
		return nil, err
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
	d.Log.Step("Uploading changes")
	for _, p := range paths {
		d.Log.Printf("Including %s", p)
	}
	lastTen := -1
	sum, err := d.Repo.Backup(ctx, BackupOptions{
		Paths: paths, Excludes: plan.Excludes, Tags: []string{"mylab"}, Host: d.Host,
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
	}
	return sum, err
}

// prepareService names the service a prepare step belongs to, from its
// dump path (<services dir>/<service>/db-dump/<file>).
func prepareService(step generate.PrepareStep) string {
	return filepath.Base(filepath.Dir(filepath.Dir(step.Target)))
}

// Bytes formats a size for people, e.g. "1.2 GB".
func Bytes(n uint64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}
