package generate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pinnedImage matches an image pinned by digest (SPEC.md §8.1) anywhere in
// a file: registry/repository, an optional tag, then the digest.
var pinnedImage = regexp.MustCompile(`\b([a-z0-9.-]+\.[a-z]+)/([a-z0-9._/-]+?)(?::[\w.-]+)?@(sha256:[0-9a-f]{64})`)

// imageFiles are every file that pins an image: Roostack's own images (the
// Go constants and the Caddy Containerfile) and the catalog's packages.
func imageFiles(t *testing.T) []string {
	t.Helper()
	files := []string{filepath.Join("..", "..", "build", "caddy", "Containerfile")}
	for _, pattern := range []string{"*.go", filepath.Join("..", "..", "docs", "examples", "*.yaml")} {
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
			platforms, err := indexPlatforms(registry, repo, digest)
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

// indexPlatforms fetches an image index (a multi-arch manifest) and returns
// its platforms as os/architecture. A digest that isn't an index is an error:
// pins are of the index, so one pin works on every processor.
func indexPlatforms(registry, repo, digest string) ([]string, error) {
	host := registry
	if registry == "docker.io" {
		host = "registry-1.docker.io"
		if !strings.Contains(repo, "/") {
			repo = "library/" + repo
		}
	}
	u := "https://" + host + "/v2/" + repo + "/manifests/" + digest
	resp, err := registryGet(u, "")
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		token, err := registryToken(resp.Header.Get("WWW-Authenticate"))
		if err != nil {
			return nil, err
		}
		if resp, err = registryGet(u, token); err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry answered %s", resp.Status)
	}
	var index struct {
		MediaType string
		Manifests []struct {
			Platform struct{ OS, Architecture string }
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&index); err != nil {
		return nil, err
	}
	if len(index.Manifests) == 0 {
		return nil, fmt.Errorf("not a multi-arch index (%s)", resp.Header.Get("Content-Type"))
	}
	var platforms []string
	for _, m := range index.Manifests {
		if p := m.Platform.OS + "/" + m.Platform.Architecture; p != "unknown/unknown" {
			platforms = append(platforms, p)
		}
	}
	return platforms, nil
}

var registryClient = &http.Client{Timeout: 30 * time.Second}

func registryGet(u, token string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// Registries rate-limit anonymous requests (ghcr.io after a couple of
	// runs in a row): wait and try again a few times.
	for attempt := 1; ; attempt++ {
		resp, err := registryClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusTooManyRequests || attempt == 4 {
			return resp, err
		}
		resp.Body.Close()
		wait := time.Duration(attempt*5) * time.Second
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs < 60 {
			wait = time.Duration(secs) * time.Second
		}
		time.Sleep(wait)
	}
}

// registryToken gets an anonymous pull token, as the registry's
// `WWW-Authenticate: Bearer realm="…",service="…",scope="…"` asks.
func registryToken(challenge string) (string, error) {
	params, ok := strings.CutPrefix(challenge, "Bearer ")
	if !ok {
		return "", fmt.Errorf("unexpected auth challenge %q", challenge)
	}
	q := url.Values{}
	var realm string
	for _, kv := range regexp.MustCompile(`(\w+)="([^"]*)"`).FindAllStringSubmatch(params, -1) {
		if kv[1] == "realm" {
			realm = kv[2]
		} else {
			q.Set(kv[1], kv[2])
		}
	}
	resp, err := registryGet(realm+"?"+q.Encode(), "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tok struct {
		Token       string
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	return tok.Token, nil
}
