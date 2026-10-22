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

// useFstab points the agent at a test fstab with these contents, and
// counts systemd reloads.
func useFstab(t *testing.T, contents string) (file string, reloads *int) {
	t.Helper()
	file = filepath.Join(t.TempDir(), "fstab")
	if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	oldFile, oldReload := fstab, reloadSystemd
	n := 0
	fstab, reloadSystemd = file, func() { n++ }
	t.Cleanup(func() { fstab, reloadSystemd = oldFile, oldReload })
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

// Nothing is written, nor systemd reloaded, when nothing changed or the
// edit fails.
func TestEditFstabUnchanged(t *testing.T) {
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
	if err := editFstab(func(tab string) (string, error) { return removeBlock(tab, "UUID=1 / ext4 defaults 0 1"), nil }); err != nil {
		t.Fatal(err)
	}
	if got, _ := readFstab(); got != "" || *reloads != 1 {
		t.Errorf("fstab %q, %d reloads; want it emptied and one reload", got, *reloads)
	}
}
