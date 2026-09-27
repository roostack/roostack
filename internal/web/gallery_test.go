package web

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mylab/mylab/internal/auth"
)

var updateGallery = flag.Bool("update-gallery", false, "rewrite docs/design/gallery.html")

// galleryFile is the standalone gallery, opened straight from the repo. Its
// CSS paths are relative to it, so it always shows the current styles.
var galleryFile = filepath.Join("..", "..", "docs", "design", "gallery.html")

func TestGalleryFile(t *testing.T) {
	var buf bytes.Buffer
	if err := renderGallery(&buf, "../../internal/web/static/", true); err != nil {
		t.Fatal(err)
	}
	if *updateGallery {
		os.MkdirAll(filepath.Dir(galleryFile), 0o755)
		if err := os.WriteFile(galleryFile, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(galleryFile)
	if err != nil || !bytes.Equal(got, buf.Bytes()) {
		t.Fatalf("%s is out of date; run: go test ./internal/web -run TestGalleryFile -update-gallery", galleryFile)
	}
	// The relative paths must point at real files.
	if _, err := os.Stat(filepath.Join("static", "gallery.js")); err != nil {
		t.Errorf("gallery links gallery.js: %v", err)
	}
	files := []string{"tokens.css", "app.css", "gallery.css"}
	for _, th := range Themes {
		files = append(files, "themes/"+th.ID+".css")
	}
	for _, css := range files {
		if _, err := os.Stat(filepath.Join("static", "css", css)); err != nil {
			t.Errorf("gallery links %s: %v", css, err)
		}
	}
}

func TestGalleryPage(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	code, body := h.get("/design")
	if code != 200 {
		t.Fatalf("GET /design: %d", code)
	}
	for _, want := range []string{`href="/static/css/tokens.css"`, `class="panel" data-mode="dark"`,
		`notice notice-warn`, `status status-running`, `swatch swatch-highlight`, "Remove dad"} {
		if !strings.Contains(body, want) {
			t.Errorf("gallery missing %q", want)
		}
	}
	// Examples never poll (they're not real operations).
	if strings.Contains(body, `hx-get="/activity/gallery`) {
		t.Error("activity examples must not poll")
	}
	if _, home := h.get("/"); !strings.Contains(home, `href="/design"`) {
		t.Error("the UI should link to the design system")
	}
}
