// Package restictest is what tests need to run the real restic: the binary
// (or a skip without it), and new repositories copied from one made once
// per test binary, since `restic init` takes seconds to derive its key and
// a copy takes milliseconds (quality review T2, T9). It doesn't import
// backup, so backup's own tests can use it.
package restictest

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Bin is the restic binary tests run: ROOSTACK_RESTIC_BIN, or restic on
// the PATH. Without either the test skips, and says so with `go test -v`.
// With ROOSTACK_RESTIC_BIN set to something that won't run, as CI sets it,
// the test fails rather than skips, so a broken setting can't hide the
// tests.
func Bin(t testing.TB) string {
	t.Helper()
	if bin := os.Getenv("ROOSTACK_RESTIC_BIN"); bin != "" {
		if out, err := exec.Command(bin, "version").CombinedOutput(); err != nil {
			t.Fatalf("ROOSTACK_RESTIC_BIN is %s, which doesn't run: %v %s", bin, err, bytes.TrimSpace(out))
		}
		return bin
	}
	bin, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic not found; set ROOSTACK_RESTIC_BIN")
	}
	return bin
}

// templates are the repositories made so far, one per password, in a
// folder that Run removes once the tests have finished.
var templates struct {
	sync.Mutex
	running bool
	dir     string
	repos   map[string]string // password → repository
}

// Run runs a package's tests, from its TestMain, and then removes the
// template repositories Init made. A package that uses Init must call
// it.
func Run(m *testing.M) int {
	templates.Lock()
	templates.running = true
	templates.Unlock()
	code := m.Run()
	templates.Lock()
	defer templates.Unlock()
	if templates.dir != "" {
		os.RemoveAll(templates.dir)
	}
	return code
}

// Init makes an empty repository at dir, which mustn't exist yet, opened
// with password (the whole of the password file restic reads, so
// usually ending in a newline): what `restic init` would make, copied
// from a template made the first time a password is used. Every
// repository made from one template has the same repository ID, which
// Roostack doesn't read. Init doesn't record a key hint, as
// backup.Repo.Init does: a test of that calls Repo.Init on a new folder.
func Init(t testing.TB, dir, password string) {
	t.Helper()
	tmpl, err := template(Bin(t), password)
	if err != nil {
		t.Fatal(err)
	}
	if err := copyDir(tmpl, dir); err != nil {
		t.Fatal(err)
	}
}

func template(bin, password string) (string, error) {
	templates.Lock()
	defer templates.Unlock()
	if !templates.running {
		return "", fmt.Errorf("restictest.Init needs the package's TestMain to call restictest.Run")
	}
	if repo, ok := templates.repos[password]; ok {
		return repo, nil
	}
	if templates.dir == "" {
		dir, err := os.MkdirTemp("", "restictest-")
		if err != nil {
			return "", err
		}
		templates.dir, templates.repos = dir, map[string]string{}
	}
	n := len(templates.repos)
	pw := filepath.Join(templates.dir, fmt.Sprintf("key-%d", n))
	if err := os.WriteFile(pw, []byte(password), 0o600); err != nil {
		return "", err
	}
	repo := filepath.Join(templates.dir, fmt.Sprintf("repo-%d", n))
	cmd := exec.Command(bin, "init", "--quiet", "--repo", repo, "--password-file", pw,
		"--cache-dir", filepath.Join(templates.dir, "cache"))
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("restic init: %v %s", err, bytes.TrimSpace(out))
	}
	templates.repos[password] = repo
	return repo, nil
}

// copyDir copies a repository's folders and files, keeping their
// permissions (restic makes its files read-only).
func copyDir(from, to string) error {
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("restictest: %s already exists", to)
	}
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		dst := filepath.Join(to, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(dst, info.Mode().Perm())
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode().Perm())
	})
}
