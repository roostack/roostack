package generate

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/imagecheck"
)

// containerfileSHA256 is the SHA-256 of build/caddy/Containerfile, as
// scripts/build-caddy-image.sh labels the image with it.
func containerfileSHA256(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "build", "caddy", "Containerfile"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// The image Roostack names is the one built from the Containerfile in the
// source: changing the Containerfile needs a new image (SPEC.md §6.5).
func TestCaddyImageMatchesContainerfile(t *testing.T) {
	if got := containerfileSHA256(t); got != CaddyContainerfileSHA256 {
		t.Errorf("build/caddy/Containerfile has changed (its SHA-256 is %s, not %s): run the Caddy image workflow "+
			"(docs/RELEASING.md) and put the image it prints, and this SHA-256, in internal/generate/caddyimage.go", got, CaddyContainerfileSHA256)
	}
	if !strings.HasPrefix(PublishedCaddyImage, "ghcr.io/roostack/roostack-caddy@sha256:") || len(PublishedCaddyImage) != len("ghcr.io/roostack/roostack-caddy@sha256:")+64 {
		t.Errorf("PublishedCaddyImage %q isn't Roostack's Caddy image named by digest", PublishedCaddyImage)
	}
	// A development build's image is never in a test run, nor in a release.
	if devCaddyImage != "" || CaddyImage != PublishedCaddyImage {
		t.Errorf("CaddyImage is %q, not the published image", CaddyImage)
	}
}

// TestCaddyImagePublished checks the image Roostack names is on GHCR, for
// both processors, built from this Containerfile, and that anyone can
// download it, as servers do. It asks the registry, so it runs only with
// ROOSTACK_IMAGE_CHECK=1: the release workflow runs it before building
// (and make check-images with the other pins).
func TestCaddyImagePublished(t *testing.T) {
	if os.Getenv("ROOSTACK_IMAGE_CHECK") == "" {
		t.Skip("asks the registry: run with ROOSTACK_IMAGE_CHECK=1 (make check-images)")
	}
	repo, digest, _ := strings.Cut(strings.TrimPrefix(PublishedCaddyImage, "ghcr.io/"), "@")
	labels, err := imagecheck.Labels("ghcr.io", repo, digest)
	if err != nil {
		t.Fatalf("%s isn't published where servers can download it: %v. Run the Caddy image workflow (docs/RELEASING.md), "+
			"put the image it prints in internal/generate/caddyimage.go, and check the package is public", PublishedCaddyImage, err)
	}
	want := containerfileSHA256(t)
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		l, ok := labels[platform]
		switch {
		case !ok:
			t.Errorf("%s has no %s build (it has %s)", PublishedCaddyImage, platform, strings.Join(slices.Sorted(maps.Keys(labels)), ", "))
		case l[CaddyContainerfileLabel] != want:
			t.Errorf("%s's %s build is from a Containerfile with SHA-256 %q, not this one's (%s)", PublishedCaddyImage, platform, l[CaddyContainerfileLabel], want)
		}
	}
}
