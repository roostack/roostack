// Package fsutil writes files the one safe way: atomically, with the
// permissions asked for, whatever was there before. It also reads files
// an app controls without letting the app hang or flood the reader.
package fsutil

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// Within reports whether path is dir or inside it. It compares the paths
// as written, cleaned first (so a trailing slash or "a/../b" can't fool
// it), without looking at the disk: a caller that cares about symlinks
// resolves them first. "/" holds every absolute path, "." every relative
// one that doesn't climb out with "..", and an absolute path is never
// within a relative folder, nor the other way round.
func Within(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	switch {
	case path == dir:
		return true
	case filepath.IsAbs(path) != filepath.IsAbs(dir):
		return false
	case dir == "/":
		return true
	case dir == ".":
		return path != ".." && !strings.HasPrefix(path, "../")
	}
	return strings.HasPrefix(path, dir+"/")
}

// OpenRegularInRoot opens path inside root for reading, for a file an app
// controls (its config file, a file its page shows): through the os.Root,
// so no link leads out, and without blocking, refused unless it's a
// regular file, so a named pipe can't hang the reader.
func OpenRegularInRoot(root *os.Root, path string) (*os.File, fs.FileInfo, error) {
	f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%s isn't a regular file", path)
	}
	return f, info, nil
}

// ReadRegularInRoot reads a file as OpenRegularInRoot opens it, refused
// when over max bytes so that a huge file isn't read into memory. It
// returns the file's information too, from the same handle.
func ReadRegularInRoot(root *os.Root, path string, max int64) ([]byte, fs.FileInfo, error) {
	f, info, err := OpenRegularInRoot(root, path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(b)) > max {
		return nil, nil, fmt.Errorf("%s is larger than %d bytes", path, max)
	}
	return b, info, nil
}
