package backup

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
)

// Preparer makes service data consistent before a snapshot (SPEC.md §6.9):
// SQLite databases are copied with SQLite's own mechanism and Postgres is
// dumped, so the snapshot never contains a half-written database.
type Preparer struct {
	// Launch runs podman commands (via systemd-run --user in production,
	// SPEC.md §6.1).
	Launch func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd
	// RoostackBin is the roostack binary, used for `backup-helper sqlite-copy`
	// inside podman unshare (so files owned by any container user can be
	// read).
	RoostackBin string
}

// Run carries out one prepare step.
func (p *Preparer) Run(ctx context.Context, step generate.PrepareStep) error {
	if err := os.MkdirAll(filepath.Dir(step.Target), 0o750); err != nil {
		return err
	}
	switch step.Type {
	case pkgspec.DBSQLite:
		return p.run(ctx, nil, "podman", "unshare", p.RoostackBin, "backup-helper", "sqlite-copy", step.Source, step.Target)
	case pkgspec.DBPostgres:
		// Custom format: compressed, and restorable table by table.
		return p.run(ctx, &step.Target, "podman", "exec", step.Container,
			"pg_dump", "--username="+step.User, "--dbname="+step.Database, "--format=custom")
	}
	return fmt.Errorf("backup: unknown prepare step %q", step.Type)
}

// run runs a command; if stdoutTo is set, stdout is written to that file
// atomically (via a temporary file and rename).
func (p *Preparer) run(ctx context.Context, stdoutTo *string, name string, args ...string) error {
	cmd := p.Launch(ctx, nil, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var tmp *os.File
	if stdoutTo != nil {
		var err error
		if tmp, err = os.CreateTemp(filepath.Dir(*stdoutTo), ".dump-*"); err != nil {
			return err
		}
		defer os.Remove(tmp.Name()) // no-op after a successful rename
		cmd.Stdout = tmp
	}
	err := cmd.Run()
	if tmp != nil {
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	if tmp != nil {
		return os.Rename(tmp.Name(), *stdoutTo)
	}
	return nil
}

// SQLiteCopy writes a consistent copy of a live SQLite database, using
// VACUUM INTO. It is safe while the app is writing: SQLite's locking
// gives the copy a single point-in-time view. It runs inside podman
// unshare via `roostack backup-helper sqlite-copy`.
func SQLiteCopy(ctx context.Context, src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	os.Remove(tmp)
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("sqlite copy %s: %w", src, err)
	}
	return os.Rename(tmp, dst)
}
