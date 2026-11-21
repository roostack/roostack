package web

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Each page gets only the shared parts it calls, so every part that Go
// renders from a page by name must be among them: the page calls it, or
// it's in partsFor.
func TestPagesHaveTheirParts(t *testing.T) {
	shared, err := sharedParts()
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	rendered := regexp.MustCompile(`render(?:Part|Live)\(w, (?:r, )?"([^"]+)", "([^"]+)"`)
	found := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range rendered.FindAllStringSubmatch(string(src), -1) {
			found++
			if p, err := parsePage(m[1], shared); err != nil || p.Lookup(m[2]) == nil {
				t.Errorf("%s: the page %q hasn't got the part %q (add it to partsFor), %v", f, m[1], m[2], err)
			}
		}
	}
	for page, parts := range partsFor {
		p, err := parsePage(page, shared)
		for _, part := range parts {
			if err != nil || p.Lookup(part) == nil {
				t.Errorf("partsFor: %q has no part %q, %v", page, part, err)
			}
		}
	}
	if found < 10 {
		t.Errorf("found only %d parts rendered by name: has renderPart changed?", found)
	}
	// A shared part a page calls is there, like the backups page's
	// backup-card; one it doesn't call isn't.
	if p, _ := parsePage("backups", shared); p.Lookup("backup-card") == nil {
		t.Error("the backups page hasn't got its backup-card")
	}
	if p, _ := parsePage("login", shared); p.Lookup("relay-stats") != nil {
		t.Error("the login page has every part, not only those it calls")
	}
}
