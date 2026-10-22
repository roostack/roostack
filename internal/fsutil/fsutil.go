// Package fsutil writes files the one safe way: atomically, with the
// permissions asked for, whatever was there before.
package fsutil

import (
	"os"
	"path/filepath"
)

// WriteFile writes data to path atomically: into a new temporary file in
// the same folder, with perm set on it, synced to disk, then renamed over
// path. Readers see the old file or the new one, never part of one, and a
// crash or power cut mid-write leaves the old file whole. The temporary
// file is removed if anything fails.
//
// Unlike os.WriteFile, perm always applies: os.WriteFile sets the mode only
// when it creates the file, so a leftover temporary file (or the file
// itself) that was 0644 would stay 0644.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WritePrivate writes a file only its owner can read (0600), atomically:
// how Roostack keeps secrets on disk (CLAUDE.md, "Security").
func WritePrivate(path string, data []byte) error {
	return WriteFile(path, data, 0o600)
}
