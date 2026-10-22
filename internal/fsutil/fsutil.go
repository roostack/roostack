// Package fsutil writes files the one safe way: atomically, with the
// permissions asked for, whatever was there before.
package fsutil

import (
	"bytes"
	"io"
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
	return write(path, bytes.NewReader(data), perm)
}

// WritePrivate writes a file only its owner can read (0600), atomically:
// how Roostack keeps secrets on disk (CLAUDE.md, "Security").
func WritePrivate(path string, data []byte) error {
	return WriteFile(path, data, 0o600)
}

// CopyFile copies src to dst atomically, as WriteFile does, streaming it
// rather than reading it all into memory first (a database dump can be
// large). dst gets perm, not src's mode.
func CopyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return write(dst, in, perm)
}

func write(path string, r io.Reader, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, r); err != nil {
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
