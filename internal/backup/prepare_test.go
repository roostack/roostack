package backup

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/pkgspec"
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
		Type: pkgspec.DBPostgres, Container: "mylab-immich-db-main", User: "immich", Database: "immich", Target: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "podman exec mylab-immich-db-main pg_dump --username=immich --dbname=immich --format=custom"
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
	p := &Preparer{Launch: fakeLaunch(&calls, "true"), MylabBin: "/usr/local/bin/mylab"}
	dir := t.TempDir()
	err := p.Run(context.Background(), generate.PrepareStep{
		Type: pkgspec.DBSQLite, Source: "/srv/x/data/db.sqlite3", Target: filepath.Join(dir, "main.sqlite3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("podman unshare /usr/local/bin/mylab backup-helper sqlite-copy /srv/x/data/db.sqlite3 %s/main.sqlite3", dir)
	if calls[0] != want {
		t.Errorf("got %q, want %q", calls[0], want)
	}
}
