package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Reading and copying the person's files (SPEC.md §8.4): Scan says what a
// folder holds and what Roostack can't read, before anything is copied;
// Copy copies it into a service's folder, again and again, each time only
// what changed. Owners are never copied: everything belongs to the user
// copying (Roostack's, the containers' root), as in folders Roostack
// makes. Links are copied as links and never followed, and nothing is
// written outside the folder copied into (os.Root).

// Count is what a folder holds.
type Count struct {
	Files int64 `json:"files"`
	Dirs  int64 `json:"dirs"`
	Bytes int64 `json:"bytes"`
	Links int64 `json:"links,omitempty"`
	// Special are devices, sockets and pipes, which aren't copied.
	Special int64 `json:"special,omitempty"`
	// Unreadable are files and folders Roostack can't read (a folder
	// counts once, for all inside it); Owner owns most of them, Example
	// is the first found.
	Unreadable int64  `json:"unreadable,omitempty"`
	Owner      int    `json:"owner,omitempty"`
	Example    string `json:"example,omitempty"`
	// SQLite are SQLite databases found by their header (for the old
	// paths search), relative to the folder.
	SQLite []string `json:"sqlite,omitempty"`
}

// sqliteHeader starts every SQLite database file.
var sqliteHeader = []byte("SQLite format 3\x00")

// Scan counts what's in dir, leaving out exclude (folders relative to
// it). seen, if set, is told how far it got now and then.
func Scan(ctx context.Context, dir string, exclude []string, seen func(Count)) (Count, error) {
	var c Count
	uid, gids := os.Getuid(), groups()
	owners := map[int]int64{}
	last := time.Now()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if err != nil {
			if p == dir {
				return err
			}
			c.unreadable(rel, -1, owners)
			return nil
		}
		if rel != "." && slices.Contains(exclude, rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if seen != nil && time.Since(last) > time.Second {
			seen(c)
			last = time.Now()
		}
		info, err := d.Info()
		if err != nil {
			c.unreadable(rel, -1, owners)
			return nil
		}
		st, _ := info.Sys().(*syscall.Stat_t)
		owner := -1
		if st != nil {
			owner = int(st.Uid)
		}
		switch t := d.Type(); {
		case t&fs.ModeSymlink != 0:
			c.Links++
		case d.IsDir():
			if rel != "." {
				c.Dirs++
			}
			if st != nil && !canRead(info.Mode(), int(st.Uid), int(st.Gid), uid, gids, true) {
				c.unreadable(rel, owner, owners)
				return fs.SkipDir
			}
		case t.IsRegular():
			c.Files++
			c.Bytes += info.Size()
			if st != nil && !canRead(info.Mode(), int(st.Uid), int(st.Gid), uid, gids, false) {
				c.unreadable(rel, owner, owners)
			} else if sqliteName(d.Name()) && hasHeader(p, sqliteHeader) {
				c.SQLite = append(c.SQLite, rel)
			}
		default:
			c.Special++
		}
		return nil
	})
	for o, n := range owners {
		if n > owners[c.Owner] || (n == owners[c.Owner] && o < c.Owner) {
			c.Owner = o
		}
	}
	return c, err
}

// unreadable counts a file or folder Roostack can't read.
func (c *Count) unreadable(rel string, owner int, owners map[int]int64) {
	c.Unreadable++
	if c.Example == "" {
		c.Example = rel
	}
	if owner >= 0 {
		owners[owner]++
	}
}

// sqliteName is a name SQLite databases usually have.
func sqliteName(name string) bool {
	for _, e := range []string{".db", ".sqlite", ".sqlite3", ".db3"} {
		if strings.HasSuffix(name, e) {
			return true
		}
	}
	return false
}

// hasHeader says whether a file starts with header.
func hasHeader(p string, header []byte) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, len(header))
	_, err = io.ReadFull(f, b)
	return err == nil && bytes.Equal(b, header)
}

// canRead says whether a user (uid, in gids) can read a file, or list and
// enter a folder, with these permissions.
func canRead(mode fs.FileMode, owner, group, uid int, gids []int, dir bool) bool {
	if uid == 0 {
		return true
	}
	need := fs.FileMode(0o4)
	if dir {
		need = 0o5
	}
	perm := mode.Perm()
	switch {
	case owner == uid:
		return perm>>6&need == need
	case slices.Contains(gids, group):
		return perm>>3&need == need
	}
	return perm&need == need
}

// groups are this process's groups.
func groups() []int {
	g, _ := os.Getgroups()
	return append(g, os.Getgid())
}

// Progress is how far a copy got.
type Progress struct {
	// Files and Bytes are those looked at so far (copied or already
	// there); Copied is what was written this time.
	Files  int64 `json:"files"`
	Bytes  int64 `json:"bytes"`
	Copied int64 `json:"copied"`
	// Removed are files gone from the person's copy since, removed from
	// Roostack's.
	Removed int64 `json:"removed,omitempty"`
	// Special were left out (devices, sockets, pipes).
	Special int64 `json:"special,omitempty"`
	// Now is the folder it's in, for people.
	Now string `json:"now,omitempty"`
}

// Copy copies src into dst (made if missing), leaving out exclude
// (folders relative to src), and removes from dst what isn't in src any
// more: copied again, only what changed is (a file with the same size and
// time is left). The progress function is told how far it got now and
// then (and at the end).
func Copy(ctx context.Context, src, dst string, exclude []string, progress func(Progress)) (Progress, error) {
	var pr Progress
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return pr, err
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return pr, err
	}
	defer root.Close()
	keep := map[string]bool{".": true}
	last := time.Now()
	tell := func(force bool) {
		if progress != nil && (force || time.Since(last) > time.Second) {
			progress(pr)
			last = time.Now()
		}
	}
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, unwrapPath(err))
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if slices.Contains(exclude, rel) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		keep[rel] = true
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("reading %s: %w", p, unwrapPath(err))
		}
		have, herr := root.Lstat(rel)
		switch t := d.Type(); {
		case d.IsDir():
			pr.Now = rel
			tell(false)
			if herr == nil && !have.IsDir() {
				if err := root.RemoveAll(rel); err != nil {
					return err
				}
				herr = fs.ErrNotExist
			}
			if herr != nil {
				if err := root.Mkdir(rel, info.Mode().Perm()|0o700); err != nil {
					return err
				}
			}
		case t&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if herr == nil && have.Mode()&fs.ModeSymlink != 0 {
				if now, err := root.Readlink(rel); err == nil && now == target {
					return nil
				}
			}
			if herr == nil {
				if err := root.RemoveAll(rel); err != nil {
					return err
				}
			}
			pr.Files++
			return root.Symlink(target, rel)
		case t.IsRegular():
			pr.Files++
			if herr == nil && have.Mode().IsRegular() && have.Size() == info.Size() && have.ModTime().Equal(info.ModTime()) {
				pr.Bytes += info.Size()
				tell(false)
				return nil // unchanged
			}
			if herr == nil && !have.Mode().IsRegular() {
				if err := root.RemoveAll(rel); err != nil {
					return err
				}
			}
			if err := copyFile(ctx, root, p, rel, info, &pr, tell); err != nil {
				return err
			}
		default:
			pr.Special++
		}
		return nil
	})
	if err != nil {
		tell(true)
		return pr, err
	}
	// What's gone from the person's copy goes from Roostack's.
	var gone []string
	fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !keep[p] {
			gone = append(gone, p)
			if d.IsDir() {
				return fs.SkipDir
			}
		}
		return nil
	})
	for _, p := range gone {
		if err := root.RemoveAll(p); err != nil {
			return pr, err
		}
		pr.Removed++
	}
	pr.Now = ""
	tell(true)
	return pr, nil
}

// copyFile copies one file into root, with its permissions (and the
// owner's read and write) and time, telling how far it got as it goes.
func copyFile(ctx context.Context, root *os.Root, src, rel string, info fs.FileInfo, pr *Progress, tell func(bool)) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, unwrapPath(err))
	}
	defer in.Close()
	out, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return fmt.Errorf("writing %s: %w", rel, unwrapPath(err))
			}
			pr.Bytes += int64(n)
			pr.Copied += int64(n)
			tell(false)
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			out.Close()
			return fmt.Errorf("reading %s: %w", src, unwrapPath(rerr))
		}
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", rel, unwrapPath(err))
	}
	// Its mode again (the umask may have taken some), then its time, so
	// copying again leaves it.
	if err := root.Chmod(rel, info.Mode().Perm()|0o600); err != nil {
		return err
	}
	return root.Chtimes(rel, info.ModTime(), info.ModTime())
}

// unwrapPath keeps an error's reason, not its path (said already).
func unwrapPath(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// Rel joins a folder of the person's files ("." for the top) to where
// they're mounted.
func Rel(base, rel string) string { return filepath.Join(base, filepath.FromSlash(path.Clean(rel))) }
