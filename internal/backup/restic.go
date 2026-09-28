// Package backup runs Roostack's backups with restic (SPEC.md §6.9).
//
// restic is called as a CLI with --json output, behind the small Repo
// type, so the tool could be swapped later. Users can always recover with
// the plain restic command and their recovery key.
package backup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ErrNoRepository means the repository hasn't been initialised.
var ErrNoRepository = errors.New("backup repository does not exist")

// ErrLocked means another restic process holds the repository lock.
var ErrLocked = errors.New("backup repository is locked")

// ErrWrongKey means the key doesn't open the repository.
var ErrWrongKey = errors.New("the key doesn't open this backup")

// Repo is one restic repository (a backup destination).
type Repo struct {
	// Bin is the restic binary.
	Bin string
	// URL is the repository location, e.g. /mnt/backup/roostack or
	// s3:https://s3.example.com/bucket/roostack.
	URL string
	// PasswordFile holds the recovery key (the repository password).
	PasswordFile string
	// Env adds backend credentials, e.g. AWS_ACCESS_KEY_ID=...
	Env []string
	// LimitUploadKiB caps upload speed (0 = unlimited), so a first
	// off-site backup doesn't swamp a home connection.
	LimitUploadKiB int
	// CacheDir is restic's local cache.
	CacheDir string
	// Launch builds the command to run. Nil runs restic directly. In
	// production it runs restic via systemd-run --user inside podman
	// unshare, so it can read files owned by container users
	// (docs/ASSUMPTIONS.md B2). Launch must handle cancellation.
	Launch func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd
}

// Retention is how many snapshots to keep (SPEC.md §6.9 defaults).
type Retention struct {
	Daily, Weekly, Monthly int
}

// DefaultRetention keeps 7 daily, 4 weekly and 6 monthly snapshots.
var DefaultRetention = Retention{Daily: 7, Weekly: 4, Monthly: 6}

// Progress is reported while a backup runs.
type Progress struct {
	PercentDone      float64
	BytesDone        uint64
	TotalBytes       uint64
	SecondsRemaining int
}

// Summary is the result of a finished backup.
type Summary struct {
	SnapshotID string
	FilesNew   int
	DataAdded  uint64
	Duration   time.Duration
	// Stored is the space the whole repository takes, after the backup
	// (0 if unknown).
	Stored uint64
}

// Snapshot is one backup in the repository.
type Snapshot struct {
	ID    string    `json:"id"`
	Short string    `json:"short_id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
	Tags  []string  `json:"tags"`
	// Hostname is the computer that made it.
	Hostname string `json:"hostname"`
}

func (r *Repo) command(ctx context.Context, args ...string) *exec.Cmd {
	env := []string{
		"RESTIC_REPOSITORY=" + r.URL,
		"RESTIC_PASSWORD_FILE=" + r.PasswordFile,
		// Lower peak memory at a small CPU cost (SPEC.md §6.9).
		"GOGC=20",
	}
	if r.CacheDir != "" {
		env = append(env, "RESTIC_CACHE_DIR="+r.CacheDir)
	}
	env = append(env, r.Env...)
	if r.Launch != nil {
		return r.Launch(ctx, env, r.Bin, args...)
	}
	return DirectLaunch(ctx, env, r.Bin, args...)
}

// DirectLaunch runs a command as the current user. On cancel it sends
// SIGINT so restic stops cleanly (removing its lock; the next run continues
// where it left off), then kills it if it hangs.
func DirectLaunch(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	return cmd
}

// run runs a restic command and returns stdout.
func (r *Repo) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := r.command(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return out, r.wrap(args[0], err, stderr.String())
}

func (r *Repo) wrap(verb string, err error, stderr string) error {
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 10:
			return ErrNoRepository
		case 11:
			return ErrLocked
		case 12:
			return ErrWrongKey
		}
	}
	return fmt.Errorf("restic %s: %w: %s", verb, err, strings.TrimSpace(stderr))
}

// Init creates the repository if it doesn't exist yet.
func (r *Repo) Init(ctx context.Context) error {
	_, err := r.run(ctx, "cat", "config")
	if !errors.Is(err, ErrNoRepository) {
		return err // exists (nil) or a real error
	}
	_, err = r.run(ctx, "init")
	return err
}

// Dump writes a folder from a snapshot as a tar stream (paths in it are
// the full paths, without the leading slash). ErrNotInSnapshot if the
// snapshot doesn't have it.
func (r *Repo) Dump(ctx context.Context, snapshotID, dir string, w io.Writer) error {
	cmd := r.command(ctx, "dump", "--archive", "tar", snapshotID, dir)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && strings.Contains(stderr.String(), "not found") {
		return ErrNotInSnapshot
	}
	return r.wrap("dump", err, stderr.String())
}

// DumpFile reads one file from a snapshot. ErrNotInSnapshot if the
// snapshot doesn't have it.
func (r *Repo) DumpFile(ctx context.Context, snapshotID, file string) ([]byte, error) {
	cmd := r.command(ctx, "dump", snapshotID, file)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil && strings.Contains(stderr.String(), "not found") {
		return nil, ErrNotInSnapshot
	}
	return stdout.Bytes(), r.wrap("dump", err, stderr.String())
}

// AddKey adds another key (password) to the repository, e.g. a server's
// own key next to the recovery key (SPEC.md §10.10). user names it in
// `restic key list`.
func (r *Repo) AddKey(ctx context.Context, key, user string) error {
	f, err := os.CreateTemp("", "roostack-key-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	_, err = r.run(ctx, "key", "add", "--new-password-file", f.Name(), "--user", user, "--host", "roostack")
	return err
}

// BackupOptions says what to back up.
type BackupOptions struct {
	Paths    []string
	Excludes []string
	Tags     []string
	// Host is recorded in the snapshot, so restores can pick this lab's
	// snapshots when a repository is shared.
	Host string
	// OnProgress is called with progress updates (may be nil).
	OnProgress func(Progress)
}

// Backup takes a snapshot. Cancelling ctx stops it cleanly; running it
// again continues without re-uploading data already stored.
func (r *Repo) Backup(ctx context.Context, o BackupOptions) (*Summary, error) {
	if len(o.Paths) == 0 {
		return nil, errors.New("backup: no paths")
	}
	args := []string{"backup", "--json"}
	if o.Host != "" {
		args = append(args, "--host", o.Host)
	}
	for _, t := range o.Tags {
		args = append(args, "--tag", t)
	}
	for _, e := range o.Excludes {
		args = append(args, "--exclude", e)
	}
	if r.LimitUploadKiB > 0 {
		args = append(args, "--limit-upload", strconv.Itoa(r.LimitUploadKiB))
	}
	args = append(args, "--")
	args = append(args, o.Paths...)

	cmd := r.command(ctx, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	summary, parseErr := parseBackupOutput(stdout, o.OnProgress)
	err = cmd.Wait()
	if err := r.wrap("backup", err, stderr.String()); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if parseErr != nil {
		return nil, parseErr
	}
	if summary == nil {
		return nil, errors.New("restic backup: no summary in output")
	}
	return summary, nil
}

// parseBackupOutput reads restic's JSON lines ("status" and "summary").
func parseBackupOutput(rd io.Reader, onProgress func(Progress)) (*Summary, error) {
	var summary *Summary
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var msg struct {
			Type             string  `json:"message_type"`
			PercentDone      float64 `json:"percent_done"`
			BytesDone        uint64  `json:"bytes_done"`
			TotalBytes       uint64  `json:"total_bytes"`
			SecondsRemaining int     `json:"seconds_remaining"`
			SnapshotID       string  `json:"snapshot_id"`
			FilesNew         int     `json:"files_new"`
			DataAdded        uint64  `json:"data_added"`
			TotalDuration    float64 `json:"total_duration"`
		}
		if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
			continue // restic may print non-JSON warnings
		}
		switch msg.Type {
		case "status":
			if onProgress != nil {
				onProgress(Progress{msg.PercentDone * 100, msg.BytesDone, msg.TotalBytes, msg.SecondsRemaining})
			}
		case "summary":
			summary = &Summary{
				SnapshotID: msg.SnapshotID, FilesNew: msg.FilesNew, DataAdded: msg.DataAdded,
				Duration: time.Duration(msg.TotalDuration * float64(time.Second)),
			}
		}
	}
	return summary, sc.Err()
}

// Stored returns the space the repository's data takes (restic stats
// --mode raw-data: deduplicated and compressed, as stored). It reads only
// the index, so it's quick.
func (r *Repo) Stored(ctx context.Context) (uint64, error) {
	out, err := r.run(ctx, "stats", "--mode", "raw-data", "--json")
	if err != nil {
		return 0, err
	}
	var st struct {
		TotalSize uint64 `json:"total_size"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return 0, fmt.Errorf("restic stats: %w", err)
	}
	return st.TotalSize, nil
}

// Snapshots lists snapshots, optionally only those with all the given tags.
func (r *Repo) Snapshots(ctx context.Context, tags ...string) ([]Snapshot, error) {
	args := []string{"snapshots", "--json"}
	if len(tags) > 0 {
		args = append(args, "--tag", strings.Join(tags, ","))
	}
	out, err := r.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	return snaps, nil
}

// Forget applies the retention policy and removes unreferenced data.
func (r *Repo) Forget(ctx context.Context, keep Retention) error {
	_, err := r.run(ctx, "forget", "--prune",
		"--keep-daily", strconv.Itoa(keep.Daily),
		"--keep-weekly", strconv.Itoa(keep.Weekly),
		"--keep-monthly", strconv.Itoa(keep.Monthly))
	return err
}

// Check verifies the repository; subset (e.g. "5%") also reads back that
// share of the stored data. Empty checks structure only.
func (r *Repo) Check(ctx context.Context, subset string) error {
	args := []string{"check"}
	if subset != "" {
		args = append(args, "--read-data-subset="+subset)
	}
	_, err := r.run(ctx, args...)
	return err
}

// RestoreDir restores one directory from a snapshot into the same place.
// Only the directory's own contents are written: restoring a whole path
// would also restore its parents' metadata (e.g. /srv), which fails inside
// podman unshare. Returns ErrNotInSnapshot if the snapshot doesn't have it.
func (r *Repo) RestoreDir(ctx context.Context, snapshotID, dir string) error {
	_, err := r.run(ctx, "restore", snapshotID+":"+dir, "--target", dir)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return ErrNotInSnapshot
	}
	return err
}

// ErrNotInSnapshot means a restored path isn't in the snapshot.
var ErrNotInSnapshot = errors.New("not in this backup")

// Restore extracts a snapshot into target. Paths are kept as they were at
// backup time under target (e.g. target/srv/roostack/services/...). Include
// limits the restore to some paths.
func (r *Repo) Restore(ctx context.Context, snapshotID, target string, include ...string) error {
	args := []string{"restore", snapshotID, "--target", target}
	for _, p := range include {
		args = append(args, "--include", p)
	}
	_, err := r.run(ctx, args...)
	return err
}
