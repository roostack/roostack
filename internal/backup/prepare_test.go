package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
)

// A copy taken while another connection keeps writing must be a valid
// database containing a consistent prefix of the writes.
func TestSQLiteCopyWhileWriting(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "db.sqlite3")
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE items (n INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() { // the "app", writing continuously
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			db.Exec(`INSERT INTO items (n) VALUES (?)`, i)
		}
	}()

	dst := filepath.Join(dir, "copy.sqlite3")
	for i := 0; i < 3; i++ { // repeated copies overwrite the previous one
		if err := SQLiteCopy(context.Background(), src, dst); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()

	cp, err := sql.Open("sqlite", "file:"+dst+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	var check string
	if err := cp.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity: %q %v", check, err)
	}
	var count, max int
	cp.QueryRow(`SELECT count(*), coalesce(max(n), -1) FROM items`).Scan(&count, &max)
	if count != max+1 {
		t.Errorf("copy has %d rows but max n=%d: not a consistent prefix", count, max)
	}
	if _, err := os.Stat(dst + ".tmp"); !os.IsNotExist(err) {
		t.Error("temporary file left behind")
	}
}

// fakeLaunch records the command and runs `sh -c script` instead.
func fakeLaunch(calls *[]string, script string) func(context.Context, []string, string, ...string) *exec.Cmd {
	return func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
		*calls = append(*calls, name+" "+strings.Join(args, " "))
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
}

func TestPostgresDump(t *testing.T) {
	var calls []string
	p := &Preparer{Launch: fakeLaunch(&calls, "printf 'PGDMP fake dump'")}
	target := filepath.Join(t.TempDir(), "db-dump", "main.sql")
	err := p.Run(context.Background(), generate.PrepareStep{
		Type: pkgspec.DBPostgres, Container: "roostack-immich-db-main", User: "immich", Database: "immich", Target: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "podman exec roostack-immich-db-main pg_dump --username=immich --dbname=immich --format=custom"
	if len(calls) != 1 || calls[0] != want {
		t.Errorf("calls = %q, want %q", calls, want)
	}
	if got, _ := os.ReadFile(target); string(got) != "PGDMP fake dump" {
		t.Errorf("dump = %q", got)
	}
}

func TestFailedDumpKeepsPrevious(t *testing.T) {
	var calls []string
	target := filepath.Join(t.TempDir(), "main.sql")
	os.WriteFile(target, []byte("yesterday"), 0o600)
	p := &Preparer{Launch: fakeLaunch(&calls, "printf partial; echo 'connection refused' >&2; exit 1")}
	err := p.Run(context.Background(), generate.PrepareStep{Type: pkgspec.DBPostgres, Container: "c", User: "u", Database: "d", Target: target})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "yesterday" {
		t.Errorf("a failed dump must not replace the previous one; got %q", got)
	}
}

func TestSQLiteStepUsesUnshare(t *testing.T) {
	var calls []string
	p := &Preparer{Launch: fakeLaunch(&calls, "true"), RoostackBin: "/usr/local/bin/roostack"}
	dir := t.TempDir()
	err := p.Run(context.Background(), generate.PrepareStep{
		Type: pkgspec.DBSQLite, Source: "/srv/x/data/db.sqlite3", Target: filepath.Join(dir, "main.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("podman unshare /usr/local/bin/roostack backup-helper sqlite-copy /srv/x/data/db.sqlite3 %s/main.sqlite3", dir)
	if calls[0] != want {
		t.Errorf("got %q, want %q", calls[0], want)
	}
}

// A database Roostack has stopped (no Quadlet file) keeps its earlier
// dump; one that should be running, or has no dump yet, is dumped
// (SPEC.md §8.3.4).
func TestStoppedDatabaseKeepsDump(t *testing.T) {
	quadlets := t.TempDir()
	target := filepath.Join(t.TempDir(), "main.sql")
	step := generate.PrepareStep{Type: pkgspec.DBPostgres, Container: "roostack-x-db-main", User: "u", Database: "d", Target: target}
	var calls []string
	p := &Preparer{Launch: fakeLaunch(&calls, "printf today"), QuadletDir: quadlets}

	// No dump yet: tried (and here it works).
	if err := p.Run(context.Background(), step); err != nil || len(calls) != 1 {
		t.Fatalf("no earlier dump: %v, calls %q", err, calls)
	}
	os.WriteFile(target, []byte("yesterday"), 0o600)
	if err := p.Run(context.Background(), step); !errors.Is(err, ErrDumpKept) || len(calls) != 1 {
		t.Errorf("stopped: %v, calls %q", err, calls)
	}
	if got, _ := os.ReadFile(target); string(got) != "yesterday" {
		t.Errorf("dump = %q", got)
	}
	// Its Quadlet file is there: it should be running, so it's dumped.
	os.WriteFile(filepath.Join(quadlets, "roostack-x-db-main.container"), nil, 0o644)
	if err := p.Run(context.Background(), step); err != nil || len(calls) != 2 {
		t.Errorf("running: %v, calls %q", err, calls)
	}
}

// MySQL and MariaDB are dumped in their containers as the app's user, the
// password read there from the secret file, never on a command line, with
// the database's schemas in the same dump.
func TestMySQLDump(t *testing.T) {
	for kind, tool := range map[string]string{pkgspec.DBMySQL: "mysqldump", pkgspec.DBMariaDB: "mariadb-dump"} {
		var calls []string
		p := &Preparer{Launch: fakeLaunch(&calls, "printf 'fake dump'")}
		target := filepath.Join(t.TempDir(), "main.sql")
		err := p.Run(context.Background(), generate.PrepareStep{
			Type: kind, Container: "roostack-seafile-db-main", User: "seafile", Database: "seafile",
			Schemas: []string{"seafile_ccnet", "seafile_seahub"}, Target: target,
		})
		if err != nil {
			t.Fatal(err)
		}
		want := `podman exec roostack-seafile-db-main sh -c u="$1"; shift; MYSQL_PWD="$(cat /run/secrets/db-password)" exec ` + tool +
			` --single-transaction --no-tablespaces --routines --triggers --events -h 127.0.0.1 -u "$u" --databases "$@"` +
			` dump seafile seafile seafile_ccnet seafile_seahub`
		if len(calls) != 1 || calls[0] != want {
			t.Errorf("%s: calls = %q, want %q", kind, calls, want)
		}
		if got, _ := os.ReadFile(target); string(got) != "fake dump" {
			t.Errorf("%s: dump = %q", kind, got)
		}
	}
}
