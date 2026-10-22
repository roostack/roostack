package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// useFstab points the agent at a test fstab with these contents, in a
// folder that stands for /etc and one for /etc/roostack. Commits rename
// in-process, as the systemd job would, and reloads are counted.
func useFstab(t *testing.T, contents string) (file string, reloads *int) {
	t.Helper()
	etc := t.TempDir()
	own := filepath.Join(etc, "roostack")
	os.Mkdir(own, 0o755)
	file = filepath.Join(etc, "fstab")
	if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	old := []string{fstab, fstabNew, fstabPrevious}
	oldCommit, oldReload := commitFstab, reloadSystemd
	n := 0
	fstab, fstabNew, fstabPrevious = file, filepath.Join(own, "fstab.new"), filepath.Join(own, "fstab.previous")
	commitFstab, reloadSystemd = CommitFstab, func() { n++ }
	t.Cleanup(func() {
		fstab, fstabNew, fstabPrevious = old[0], old[1], old[2]
		commitFstab, reloadSystemd = oldCommit, oldReload
	})
	return file, &n
}

// Edits at the same time each start from the other's result, so none is
// lost (the quality review of 2026-10-21, P5).
func TestEditFstabConcurrent(t *testing.T) {
	file, _ := useFstab(t, "UUID=1 / ext4 defaults 0 1\n")
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := editFstab(func(tab string) (string, error) {
				return withBlock(tab, fmt.Sprintf("# Added by Roostack: media library l%d", i), fmt.Sprintf("/a /b%d none bind 0 0", i)), nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, _ := os.ReadFile(file)
	if n := strings.Count(string(data), "# Added by Roostack"); n != 20 {
		t.Errorf("%d of 20 edits kept:\n%s", n, data)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, want 0644", fi.Mode().Perm())
	}
}

// A change replaces fstab whole and keeps the file before it; nothing is
// written, nor systemd reloaded, when nothing changed or the edit fails.
func TestEditFstab(t *testing.T) {
	const tab = "UUID=1 / ext4 defaults 0 1\n"
	file, reloads := useFstab(t, tab)
	if err := editFstab(func(tab string) (string, error) { return tab, nil }); err != nil {
		t.Fatal(err)
	}
	if err := editFstab(func(string) (string, error) { return "", errors.New("no") }); err == nil {
		t.Error("a failed edit: want its error")
	}
	if data, _ := os.ReadFile(file); string(data) != tab || *reloads != 0 {
		t.Errorf("fstab %q, %d reloads; want it untouched", data, *reloads)
	}
	if _, err := os.Stat(fstabPrevious); !errors.Is(err, os.ErrNotExist) {
		t.Error("nothing changed, so nothing is kept")
	}
	if err := editFstab(func(tab string) (string, error) {
		return withBlock(tab, "# Added by Roostack: x", "/a /b none bind 0 0"), nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := readFstab(); !strings.HasSuffix(got, "/a /b none bind 0 0\n") || *reloads != 1 {
		t.Errorf("fstab %q, %d reloads; want the line and one reload", got, *reloads)
	}
	if prev, _ := os.ReadFile(fstabPrevious); string(prev) != tab {
		t.Errorf("previous %q, want the file before the change", prev)
	}
	if _, err := os.Stat(fstabNew); !errors.Is(err, os.ErrNotExist) {
		t.Error("the new file is renamed into place, not left behind")
	}
}

// If the new file can't be put in place (the systemd job fails), fstab
// stays as it was and the new file goes.
func TestEditFstabCommitFails(t *testing.T) {
	const tab = "UUID=1 / ext4 defaults 0 1\n"
	file, reloads := useFstab(t, tab)
	commitFstab = func() error { return errors.New("the job failed") }
	err := editFstab(func(tab string) (string, error) { return tab + "/a /b none bind 0 0\n", nil })
	if err == nil || !strings.Contains(err.Error(), "replacing /etc/fstab: the job failed") {
		t.Errorf("err = %v", err)
	}
	if data, _ := os.ReadFile(file); string(data) != tab || *reloads != 0 {
		t.Errorf("fstab %q, %d reloads; want it as it was", data, *reloads)
	}
	if _, err := os.Stat(fstabNew); !errors.Is(err, os.ErrNotExist) {
		t.Error("the new file should go")
	}
	// Nowhere to write the new file: fstab is untouched too.
	commitFstab = CommitFstab
	fstabNew = filepath.Join(t.TempDir(), "missing", "fstab.new")
	if err := editFstab(func(tab string) (string, error) { return tab + "/c /d none bind 0 0\n", nil }); err == nil {
		t.Error("want an error")
	}
	if data, _ := os.ReadFile(file); string(data) != tab {
		t.Errorf("fstab %q, want it as it was", data)
	}
}

// The commit renames the new file into place, and fails when there's
// none.
func TestRenameSynced(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "new"), filepath.Join(dir, "fstab")
	os.WriteFile(from, []byte("new"), 0o644)
	os.WriteFile(to, []byte("old"), 0o644)
	if err := renameSynced(from, to); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(to); string(data) != "new" {
		t.Errorf("fstab %q", data)
	}
	if err := renameSynced(from, to); err == nil {
		t.Error("nothing to rename: want an error")
	}
}
