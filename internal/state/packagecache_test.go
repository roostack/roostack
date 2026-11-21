package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/pkgtest"
)

// The lab's packages are parsed once while their files stay the same: each
// load is a copy the caller may change, and a changed or removed file is
// read again.
func TestPackageCache(t *testing.T) {
	c := packageCache{parsed: map[string]cachedPackage{}}
	path := filepath.Join(t.TempDir(), "vaultwarden.yaml")
	data := pkgtest.Read(t, "vaultwarden")
	writeFile(t, path, string(data))
	a, err := c.load(path)
	if err != nil {
		t.Fatal(err)
	}
	a.Title = "changed by its caller"
	b, err := c.load(path)
	if err != nil || b.Title != "Vaultwarden" || b == a || len(c.parsed) != 1 {
		t.Fatalf("second load: %q %v", b.Title, err)
	}
	writeFile(t, path, strings.Replace(string(data), "title: Vaultwarden", "title: Vaultwarden again", 1))
	if b, err = c.load(path); err != nil || b.Title != "Vaultwarden again" {
		t.Fatalf("after the file changed: %q %v", b.Title, err)
	}
	os.Remove(path)
	if _, err := c.load(path); err == nil || len(c.parsed) != 0 {
		t.Errorf("a removed file: %v, %d kept", err, len(c.parsed))
	}
	writeFile(t, path, "package: [")
	if _, err := c.load(path); err == nil || !strings.Contains(err.Error(), path) || len(c.parsed) != 0 {
		t.Errorf("a broken file: %v, %d kept", err, len(c.parsed))
	}
}
