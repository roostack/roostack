package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

// The mode asked for applies even over a file that had a looser one, and
// nothing is left behind (the quality review of 2026-10-21, P7).
func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secret")
	if err := os.WriteFile(file, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(file, []byte("new")); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(file)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	if data, _ := os.ReadFile(file); string(data) != "new" {
		t.Errorf("content = %q", data)
	}
	if err := WriteFile(file, []byte("shared"), 0o640); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
}

// A write that fails leaves the old file as it was, and no temporary file.
func TestWriteFileFails(t *testing.T) {
	dir := t.TempDir()
	if err := WritePrivate(filepath.Join(dir, "missing", "secret"), []byte("x")); err == nil {
		t.Error("a missing folder: want an error")
	}
	// The target is a folder: the rename fails.
	target := filepath.Join(dir, "folder")
	os.Mkdir(target, 0o755)
	os.WriteFile(filepath.Join(target, "inside"), nil, 0o644)
	if err := WritePrivate(target, []byte("x")); err == nil {
		t.Error("over a folder: want an error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
}
