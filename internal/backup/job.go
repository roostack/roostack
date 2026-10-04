package backup

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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

// Plan is everything one server's backup covers (SPEC.md §6.9): the
// server's own snapshot (Roostack's state and the Podman secrets store),
// then one snapshot per service, so each service can be found and restored
// on its own, onto any server (SPEC.md §10.10).
type Plan struct {
	Paths    []string
	Excludes []string
	Prepare  []generate.PrepareStep
	Services []ServicePlan
}

// ServicePlan is one service's snapshot.
type ServicePlan struct {
	Name     string
	Paths    []string
	Excludes []string
	// Secrets are the service's Podman secrets, saved with its data so a
	// restore on another server (or after its server is lost) has them.
	Secrets []string
}

// Snapshot tags. Every snapshot Roostack makes is tagged TagRoostack, plus
// TagServer (the server's own) or ServiceTag (one service's).
const (
	TagRoostack = "roostack"
	TagServer   = "server"
)

// ServiceTag tags a service's snapshots.
func ServiceTag(name string) string { return "service=" + name }

// RunTag tags every snapshot of one backup run, main and copies alike, so
// a restore can tell which copies hold the same moment.
func RunTag(started time.Time) string { return "run=" + strconv.FormatInt(started.Unix(), 10) }

// Run identifies the backup run a snapshot is from ("" if untagged).
func (s Snapshot) Run() string {
	for _, t := range s.Tags {
		if run, ok := strings.CutPrefix(t, "run="); ok {
			return run
		}
	}
	return ""
}

// Service names the service a snapshot is of ("" for the server's own).
func (s Snapshot) Service() string {
	for _, t := range s.Tags {
		if name, ok := strings.CutPrefix(t, "service="); ok {
			return name
		}
	}
	return ""
}

// SecretsDir is where a backup saves each service's secrets for its
// snapshot (removed again after the upload).
func SecretsDir(paths config.Paths) string { return filepath.Join(paths.StateDir, "backup-secrets") }

// SecretsFile is a service's saved secrets (JSON: name to value).
func SecretsFile(paths config.Paths, service string) string {
	return filepath.Join(SecretsDir(paths), service+".json")
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
			// Each service's secrets go in its own snapshot.
			SecretsDir(paths),
		),
	}
	for _, s := range services {
		if len(s.Backup.Include) == 0 {
			continue // nothing of its own to keep (e.g. a cache)
		}
		sp := ServicePlan{Name: s.Service, Paths: slices.Clone(s.Backup.Include), Excludes: s.Backup.Exclude}
		for _, sec := range s.Secrets {
			sp.Secrets = append(sp.Secrets, sec.Name)
		}
		slices.Sort(sp.Paths)
		sp.Paths = slices.Compact(sp.Paths)
		p.Services = append(p.Services, sp)
		p.Prepare = append(p.Prepare, s.Backup.Prepare...)
	}
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
	// Only limits a run to some destinations, by name ("" the main one,
	// else a copy's Name): a retry of those that failed. Nil runs all.
	Only []string
	// ReadSecret reads a Podman secret, to save with its service's data.
	// Optional: without it, services' snapshots have no secrets.
	ReadSecret func(ctx context.Context, name string) ([]byte, error)
	// Generation is the lab's (fence.go): every snapshot is tagged with
	// it, and a newer one in a repository means this copy is out of date.
	Generation int
}

// wants reports whether a run backs up to destination name.
func (d JobDeps) wants(name string) bool { return d.Only == nil || slices.Contains(d.Only, name) }

// DriveError is a destination's drive not being ready (its Precheck):
// the backup there runs as soon as it's plugged in.
type DriveError struct{ Err error }

func (e *DriveError) Error() string { return e.Err.Error() }
func (e *DriveError) Unwrap() error { return e.Err }

// precheck runs a destination's check, as a DriveError.
func precheck(check func() error) error {
	if check == nil {
		return nil
	}
	if err := check(); err != nil {
		return &DriveError{err}
	}
	return nil
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
//
// Each destination is checked first (its drive); one that isn't ready is
// skipped, and the others go ahead.
func Run(ctx context.Context, d JobDeps, r Reporter) (summary *Summary, err error) {
	defer func() {
		if d.OnDone != nil {
			d.OnDone(summary, err)
		}
	}()
	mainErr := error(nil)
	if d.wants("") {
		mainErr = precheck(d.Precheck)
	}
	copies := 0
	for _, c := range d.Copies {
		if d.wants(c.Name) {
			copies++
		}
	}
	if mainErr != nil && copies == 0 {
		return nil, mainErr
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
	snaps, err := d.snapshots(ctx, plan, RunTag(time.Now()), GenTag(d.Generation))
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(SecretsDir(d.Paths))
	r.SetInterruptible(true)
	switch {
	case mainErr != nil:
		err = mainErr
		d.Log.Printf("Not backing up to the main destination: %v", err)
	case d.wants(""):
		summary, err = upload(ctx, d, d.Repo, "", snaps, r)
	}
	var fenced *FenceError
	if errors.As(err, &fenced) {
		d.Log.Printf("%v", err)
		return nil, err // nowhere else either
	}
	// The copies go ahead even if the main destination failed: that's
	// what they're for.
	for _, c := range d.Copies {
		if !d.wants(c.Name) {
			continue
		}
		started := time.Now()
		var csum *Summary
		cerr := precheck(c.Precheck)
		if cerr == nil {
			csum, cerr = upload(ctx, d, c.Repo, c.Title, snaps, r)
		}
		if errors.As(cerr, &fenced) {
			d.Log.Printf("%v", cerr)
			return summary, cerr
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

// snapshot is one snapshot a run makes, in every repository.
type snapshot struct {
	title    string // "" for the server's own
	paths    []string
	excludes []string
	tags     []string
}

// snapshots lists what a run backs up: the server's own snapshot, then each
// service's (with its secrets saved next to it). Paths that don't exist yet
// are skipped (e.g. no Podman secrets on a fresh install, or a service
// without media); restic treats a missing source as an error.
func (d JobDeps) snapshots(ctx context.Context, plan Plan, run, gen string) ([]snapshot, error) {
	existing := func(paths []string) []string {
		var out []string
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
			}
		}
		return out
	}
	var out []snapshot
	if paths := existing(plan.Paths); len(paths) > 0 {
		out = append(out, snapshot{paths: paths, excludes: plan.Excludes, tags: []string{TagRoostack, TagServer, run, gen}})
	}
	if err := os.RemoveAll(SecretsDir(d.Paths)); err != nil {
		return nil, err
	}
	for _, sp := range plan.Services {
		paths := existing(sp.Paths)
		if len(paths) == 0 {
			continue
		}
		if len(sp.Secrets) > 0 && d.ReadSecret != nil {
			secrets := map[string][]byte{}
			for _, name := range sp.Secrets {
				if v, err := d.ReadSecret(ctx, name); err == nil {
					secrets[name] = v
				} // else e.g. an install-only secret, used and deleted
			}
			file := SecretsFile(d.Paths, sp.Name)
			if err := writeJSON(file, secrets); err != nil {
				return nil, fmt.Errorf("saving %s's secrets for its backup: %w", sp.Name, err)
			}
			paths = append(paths, file)
		}
		out = append(out, snapshot{title: sp.Name, paths: paths, excludes: sp.Excludes, tags: []string{TagRoostack, ServiceTag(sp.Name), run, gen}})
	}
	return out, nil
}

// writeJSON writes v to a file only Roostack can read.
func writeJSON(file string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o600)
}

// upload backs the snapshots up to one repository (to is "" for the main
// one). The summary adds them up; its snapshot is the server's own.
func upload(ctx context.Context, d JobDeps, repo *Repo, to string, snaps []snapshot, r Reporter) (*Summary, error) {
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
	// Out of date (another copy of this lab is newer, or the lab removed
	// this server): nothing is uploaded, here or anywhere.
	if err := checkFence(ctx, repo, d.Generation); err != nil {
		return nil, err
	}
	total := &Summary{}
	for i, snap := range snaps {
		what := cmp.Or(snap.title, "Roostack's own settings")
		if to == "" {
			d.Log.Step("Backing up %s", what)
			for _, p := range snap.paths {
				d.Log.Printf("Including %s", p)
			}
		} else {
			d.Log.Step("Copying %s to %s", what, to)
		}
		lastTen := -1
		sum, err := repo.Backup(ctx, BackupOptions{
			Paths: snap.paths, Excludes: snap.excludes, Tags: snap.tags, Host: d.Host,
			OnProgress: func(p Progress) {
				// Overall progress: each snapshot is an equal share.
				r.SetProgress((i*100 + int(p.PercentDone)) / len(snaps))
				if ten := int(p.PercentDone) / 10; ten > lastTen && p.TotalBytes > progressLines {
					lastTen = ten
					d.Log.Printf("%3d%%  %s of %s, about %s left", int(p.PercentDone), Bytes(p.BytesDone),
						Bytes(p.TotalBytes), (time.Duration(p.SecondsRemaining) * time.Second).String())
				}
			},
		})
		if err != nil {
			return nil, fmt.Errorf("backing up %s: %w", what, err)
		}
		d.Log.Printf("%d new files, %s added (snapshot %s)", sum.FilesNew, Bytes(sum.DataAdded), short(sum.SnapshotID))
		if snap.title == "" || total.SnapshotID == "" {
			total.SnapshotID = sum.SnapshotID
		}
		total.FilesNew += sum.FilesNew
		total.DataAdded += sum.DataAdded
		total.Duration += sum.Duration
	}
	d.Log.Step("Backed up: %d new files, %s added", total.FilesNew, Bytes(total.DataAdded))
	// The space it all takes there, for the plan (SPEC.md §6.9).
	if stored, serr := repo.Stored(ctx); serr == nil {
		total.Stored = stored
		d.Log.Printf("The backups there take %s", Bytes(stored))
	} else {
		d.Log.Printf("Couldn't tell how much space the backups there take: %v", serr)
	}
	return total, nil
}

// progressLines is the size above which an upload logs its progress every
// 10% (small ones finish too quickly for it to help).
const progressLines = uint64(100 * units.MB)

// short shortens a snapshot ID as restic shows it.
func short(id string) string { return id[:min(8, len(id))] }

// prepareService names the service a prepare step belongs to, from its
// dump path (<services dir>/<service>/db-dump/<file>).
func prepareService(step generate.PrepareStep) string {
	return filepath.Base(filepath.Dir(filepath.Dir(step.Target)))
}

// Bytes formats a size for people, e.g. "1.2 GB" (units.Bytes).
func Bytes(n uint64) string { return units.Bytes(int64(n)) }
