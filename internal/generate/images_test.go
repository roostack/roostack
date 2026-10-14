package generate

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/imagecheck"
	"roostack.cloud/roostack/internal/pkgtest"
)

// pinnedImage matches an image pinned by digest (SPEC.md §8.1) anywhere in
// a file: registry/repository, an optional tag, then the digest.
var pinnedImage = regexp.MustCompile(`\b([a-z0-9.-]+\.[a-z]+)/([a-z0-9._/-]+?)(?::[\w.-]+)?@(sha256:[0-9a-f]{64})`)

// imageFiles are every file that pins an image: Roostack's own images (the
// Go constants and the Caddy Containerfile) and the catalog's packages.
func imageFiles(t *testing.T) []string {
	t.Helper()
	files := []string{filepath.Join("..", "..", "build", "caddy", "Containerfile")}
	for _, pattern := range []string{"*.go", filepath.Join(pkgtest.Dir(), "*", "*.yaml")} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			if !strings.HasSuffix(m, "_test.go") {
				files = append(files, m)
			}
		}
	}
	return files
}

// TestImagesMultiArch checks every pinned image has a build for each
// processor Roostack supports, so a pin update can't quietly drop ARM
// (ARM64-PLAN.md 1.3). It asks the registries, so it runs only with
// ROOSTACK_IMAGE_CHECK=1 (make check-images): run it after changing a pin.
func TestImagesMultiArch(t *testing.T) {
	if os.Getenv("ROOSTACK_IMAGE_CHECK") == "" {
		t.Skip("asks the registries: run with ROOSTACK_IMAGE_CHECK=1 (make check-images)")
	}
	seen := map[string]bool{}
	for _, f := range imageFiles(t) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pinnedImage.FindAllStringSubmatch(string(data), -1) {
			registry, repo, digest := m[1], m[2], m[3]
			ref := registry + "/" + repo + "@" + digest
			if seen[ref] {
				continue
			}
			seen[ref] = true
			platforms, err := imagecheck.IndexPlatforms(registry, repo, digest)
			if err != nil {
				t.Errorf("%s (%s): %v", ref, f, err)
				continue
			}
			for _, want := range []string{"linux/amd64", "linux/arm64"} {
				if !slices.Contains(platforms, want) {
					t.Errorf("%s (%s) has no %s build (it has %s)", ref, f, want, strings.Join(platforms, ", "))
				}
			}
		}
	}
	if len(seen) < 10 {
		t.Errorf("found only %d pinned images; is the pattern still right?", len(seen))
	}
	t.Logf("checked %d images", len(seen))
}
