package importer

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A copy leaves out what's excluded, keeps links as links, and copied
// again copies only what changed and removes what's gone.
func TestCopy(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "library.incoming")
	write(t, filepath.Join(src, "upload/a.jpg"), "photo")
	write(t, filepath.Join(src, "upload/b.jpg"), "photo two")
	write(t, filepath.Join(src, "thumbs/a.webp"), "thumb")
	os.Symlink("/etc/passwd", filepath.Join(src, "link"))
	pr, err := Copy(t.Context(), src, dst, []string{"thumbs"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Files != 3 || pr.Copied != 14 || pr.Bytes != 14 {
		t.Errorf("first copy %+v", pr)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "upload/b.jpg")); string(b) != "photo two" {
		t.Errorf("copied %q", b)
	}
	if _, err := os.Stat(filepath.Join(dst, "thumbs")); err == nil {
		t.Error("an excluded folder was copied")
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "/etc/passwd" {
		t.Errorf("link %q %v", l, err)
	}

	// Copied again: one file changed, one gone, and a folder where the
	// link was (which mustn't be followed).
	write(t, filepath.Join(src, "upload/a.jpg"), "PHOTO")
	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(src, "upload/a.jpg"), later, later)
	os.Remove(filepath.Join(src, "upload/b.jpg"))
	os.Remove(filepath.Join(src, "link"))
	write(t, filepath.Join(src, "link/inside.txt"), "x")
	pr, err = Copy(t.Context(), src, dst, []string{"thumbs"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Copied != 6 || pr.Removed != 1 {
		t.Errorf("second copy %+v", pr)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "link/inside.txt")); string(b) != "x" {
		t.Error("the link wasn't replaced by its folder")
	}
	if _, err := os.Stat(filepath.Join(dst, "upload/b.jpg")); err == nil {
		t.Error("a file gone from the copy stayed")
	}
	if pr, _ = Copy(t.Context(), src, dst, nil, nil); pr.Copied != 5 {
		t.Errorf("thumbs copied once no longer excluded: %+v", pr)
	}
	if pr, _ = Copy(t.Context(), src, dst, nil, nil); pr.Copied != 0 {
		t.Errorf("nothing changed, yet %+v", pr)
	}
}

// Scan counts files, bytes and links, what Roostack can't read, and finds
// SQLite databases by their header.
func TestScan(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Library/com.plexapp.plugins.library.db"), "SQLite format 3\x00 and more")
	write(t, filepath.Join(dir, "Library/fake.db"), "not one")
	write(t, filepath.Join(dir, "Preferences.xml"), "<x/>")
	write(t, filepath.Join(dir, "Cache/a"), "aaaa")
	os.Symlink("Library", filepath.Join(dir, "link"))
	c, err := Scan(t.Context(), dir, []string{"Cache"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Files != 3 || c.Dirs != 1 || c.Links != 1 || c.Unreadable != 0 || len(c.SQLite) != 1 || c.SQLite[0] != "Library/com.plexapp.plugins.library.db" {
		t.Errorf("scan %+v", c)
	}
	if os.Getuid() == 0 {
		return // root reads everything
	}
	os.Chmod(filepath.Join(dir, "Preferences.xml"), 0o200)
	os.Chmod(filepath.Join(dir, "Library"), 0o300)
	defer os.Chmod(filepath.Join(dir, "Library"), 0o755)
	c, _ = Scan(t.Context(), dir, []string{"Cache"}, nil)
	if c.Unreadable != 2 || c.Owner != os.Getuid() || c.Example == "" {
		t.Errorf("unreadable %+v", c)
	}
}
