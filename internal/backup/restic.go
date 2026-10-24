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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/cloudkeys"
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
	// Credentials are keys that expire, fetched before each command
	// (Roostack Cloud backup's short-lived keys: internal/cloudkeys). A
	// failure stops the command before it starts; a command that fails
	// once its keys have run out runs again at once with new ones
	// (keysRanOut).
	Credentials func(ctx context.Context) (cloudapi.StorageKeys, error)
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
	// Hints, if set, tells restic which key file to try (hints.go), so a
	// slow key someone added doesn't slow every open.
	Hints *KeyHints
	// JunkRecheck is how long FindJunk waits before reading a suspect
	// file again; zero is junkRecheck (quarantine.go).
	JunkRecheck time.Duration
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
	// Services are the services a run backed up (a run's total only).
	Services []string
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
	// Summary is what the backup covered (restic 0.17 and later).
	Summary *struct {
		TotalBytes int64 `json:"total_bytes_processed"`
	} `json:"summary,omitempty"`
}

// Size is how much data the snapshot holds, in bytes (-1 if unknown).
func (s Snapshot) Size() int64 {
	if s.Summary == nil {
		return -1
	}
	return s.Summary.TotalBytes
}

func (r *Repo) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd, _ := r.commandUntil(ctx, args...)
	return cmd
}

// commandUntil is command, and when the keys it runs with expire (zero
// without Credentials).
func (r *Repo) commandUntil(ctx context.Context, args ...string) (*exec.Cmd, time.Time) {
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
	if hint := r.keyHint(ctx); hint != "" {
		env = append(env, "RESTIC_KEY_HINT="+hint)
	}
	var keysErr error
	var until time.Time
	if r.Credentials != nil {
		var k cloudapi.StorageKeys
		if k, keysErr = r.Credentials(ctx); keysErr == nil {
			env, until = append(env, cloudkeys.Env(k)...), k.Expires
		}
	}
	var cmd *exec.Cmd
	if r.Launch != nil {
		cmd = r.Launch(ctx, env, r.Bin, args...)
	} else {
		cmd = DirectLaunch(ctx, env, r.Bin, args...)
	}
	if keysErr != nil {
		// exec.Cmd's Start returns Err: the command never runs.
		cmd.Err = fmt.Errorf("getting this backup's keys: %w", keysErr)
	}
	return cmd, until
}

// keysRanOut reports whether a command that failed at now ran with keys
// that expire at until and had run out (or were about to): it failed
// because of them, whatever the storage said (S3 answers 400 or 403), so
// new keys put it right at once, without waiting for the next try. A
// refusal while the keys are still good (a suspended account, a full
// quota) isn't this.
func keysRanOut(until, now time.Time) bool {
	return !until.IsZero() && !now.Before(until.Add(-time.Minute))
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

// run runs a restic command and returns stdout. One that failed because
// its keys ran out runs again at once, with new ones.
func (r *Repo) run(ctx context.Context, args ...string) ([]byte, error) {
	for try := 0; ; try++ {
		cmd, until := r.commandUntil(ctx, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil && try == 0 && ctx.Err() == nil && keysRanOut(until, time.Now()) {
			continue
		}
		return out, r.wrap(args[0], err, stderr.String())
	}
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
	if _, err = r.run(ctx, "init"); err != nil {
		return err
	}
	// Its only key is this one: remember it while nobody can have added a
	// slow one (hints.go).
	if r.Hints != nil {
		r.recordHint(ctx)
	}
	return nil
}

// Dump writes a folder from a snapshot as a tar stream (paths in it are
// the full paths, without the leading slash). ErrNotInSnapshot if the
// snapshot doesn't have it.
func (r *Repo) Dump(ctx context.Context, snapshotID, dir string, w io.Writer) error {
	cmd := r.command(ctx, "dump", "--archive", "tar", "--", snapshotID, dir)
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
	cmd := r.command(ctx, "dump", "--", snapshotID, file)
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
// `restic key list`. It returns the new key's ID (for a key hint, which it
// records for this Repo's URL), or "" if restic didn't say.
//
// The new key goes in a file next to the repository's key file, not in
// /tmp: restic may run outside Roostack's sandbox (Launch, for a drive),
// where Roostack's private /tmp doesn't exist, but the key file's folder
// is visible to both. CreateTemp makes it 0600.
func (r *Repo) AddKey(ctx context.Context, key, user string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(r.PasswordFile), ".new-key-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(key + "\n"); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	out, err := r.run(ctx, "key", "add", "--new-password-file", f.Name(), "--user", user, "--host", "roostack")
	if err != nil {
		return "", err
	}
	m := addedKey.FindSubmatch(out)
	if m == nil {
		return "", nil
	}
	id := string(m[1])
	if r.Hints != nil {
		r.Hints.Set(r.URL, []byte(key), id)
	}
	return id, nil
}

// addedKey is what `restic key add` says (0.18: "saved new key with ID …").
var addedKey = regexp.MustCompile(`saved new key with ID ([0-9a-f]{8,64})`)

// Key is one of a repository's keys (`restic key list`).
type Key struct {
	ID       string `json:"id"`
	Current  bool   `json:"current"` // the key this Repo opened it with
	UserName string `json:"userName"`
}

// Keys lists the repository's keys.
func (r *Repo) Keys(ctx context.Context) ([]Key, error) {
	out, err := r.run(ctx, "key", "list", "--json")
	if err != nil {
		return nil, err
	}
	var keys []Key
	if err := json.Unmarshal(out, &keys); err != nil {
		return nil, fmt.Errorf("restic key list: %w", err)
	}
	return keys, nil
}

// CurrentKey is the ID of the key this Repo opens the repository with.
func (r *Repo) CurrentKey(ctx context.Context) (string, error) {
	keys, err := r.Keys(ctx)
	if err != nil {
		return "", err
	}
	for _, k := range keys {
		if k.Current {
			return k.ID, nil
		}
	}
	return "", errors.New("restic key list: no current key")
}

// RemoveKey removes another key from the repository (restic refuses to
// remove the one in use).
func (r *Repo) RemoveKey(ctx context.Context, id string) error {
	_, err := r.run(ctx, "key", "remove", id)
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
	// OnNewKeys is called when the backup's keys ran out while it ran and
	// it's carrying on with new ones (may be nil).
	OnNewKeys func()
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

	for try := 0; ; try++ {
		summary, until, err := r.backupOnce(ctx, args, o.OnProgress)
		if err != nil && try == 0 && ctx.Err() == nil && keysRanOut(until, time.Now()) {
			// A long upload outlasted its keys: what's uploaded is kept
			// (restic saves its index as it goes), so carry on now.
			if o.OnNewKeys != nil {
				o.OnNewKeys()
			}
			continue
		}
		return summary, err
	}
}

// backupOnce runs restic backup once, and says when its keys expire.
func (r *Repo) backupOnce(ctx context.Context, args []string, onProgress func(Progress)) (*Summary, time.Time, error) {
	cmd, until := r.commandUntil(ctx, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, until, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, until, err
	}
	summary, parseErr := parseBackupOutput(stdout, onProgress)
	err = cmd.Wait()
	if err := r.wrap("backup", err, stderr.String()); err != nil {
		if ctx.Err() != nil {
			return nil, until, ctx.Err()
		}
		return nil, until, err
	}
	if parseErr != nil {
		return nil, until, parseErr
	}
	if summary == nil {
		return nil, until, errors.New("restic backup: no summary in output")
	}
	return summary, until, nil
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

// Forget applies the retention policy and removes unreferenced data. Each
// service's snapshots and the server's own are kept by the policy on their
// own (grouped by tags), so a service that's no longer backed up (its
// server lost, waiting to be restored) keeps its last backups rather than
// losing them to other services' newer ones (SPEC.md §10.10).
func (r *Repo) Forget(ctx context.Context, keep Retention) error {
	_, err := r.run(ctx, "forget", "--prune", "--group-by", "host,tags",
		"--keep-daily", strconv.Itoa(keep.Daily),
		"--keep-weekly", strconv.Itoa(keep.Weekly),
		"--keep-monthly", strconv.Itoa(keep.Monthly))
	return err
}

// Unlock removes stale locks: those of a restic that crashed or was
// killed (on this host and no longer running), or not refreshed for 30
// minutes. Locks of a restic that's still running stay, so it's safe
// while a server backs up. restic doesn't skip stale locks when it locks a
// repository for itself, so without this a crashed backup blocks every
// prune and check there for good (docs/ASSUMPTIONS.md N1). It returns how
// many it removed.
func (r *Repo) Unlock(ctx context.Context) (int, error) {
	out, err := r.run(ctx, "unlock")
	if err != nil {
		return 0, err
	}
	n := 0
	if m := unlockedRE.FindSubmatch(out); m != nil {
		n, _ = strconv.Atoi(string(m[1]))
	}
	return n, nil
}

// unlockedRE is what restic unlock prints when it removed locks.
var unlockedRE = regexp.MustCompile(`removed (\d+) locks?`)

// ForgetSnapshots removes snapshots by ID, and the data only they used.
func (r *Repo) ForgetSnapshots(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.run(ctx, append([]string{"forget", "--prune", "--"}, ids...)...)
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
