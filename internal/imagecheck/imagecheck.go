// Package imagecheck asks container registries which processors an image
// has builds for: the multi-architecture check run before a pin changes
// (make check-images, ARM64-PLAN.md 1.3) and by the registry's CI for its
// index (SPEC.md §8.3.3). Anonymous pulls only; no Podman needed.
package imagecheck

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// pinnedRef splits an image pinned by digest: registry/repository, an
// optional tag, then the digest.
var pinnedRef = regexp.MustCompile(`^([a-z0-9.-]+\.[a-z]+(?::[0-9]+)?)/([a-z0-9._/-]+?)(?::[\w.-]+)?@(sha256:[0-9a-f]{64})$`)

// Platforms returns the os/architecture pairs of a pinned image
// (registry/repository[:tag]@sha256:…), from its image index.
func Platforms(ref string) ([]string, error) {
	m := pinnedRef.FindStringSubmatch(ref)
	if m == nil {
		return nil, fmt.Errorf("%s isn't an image pinned by digest (registry/name:tag@sha256:…)", ref)
	}
	return IndexPlatforms(m[1], m[2], m[3])
}

// ErrGone: the registry no longer has the image (it answered 404 for its
// digest), as when a project rebuilds a tag and the registry deletes the
// image the tag pointed to before.
var ErrGone = errors.New("the image is gone from its registry")

// IndexPlatforms fetches an image index (a multi-arch manifest) and returns
// its platforms as os/architecture. A digest that isn't an index is an error:
// pins are of the index, so one pin works on every processor.
func IndexPlatforms(registry, repo, digest string) ([]string, error) {
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
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("registry answered %s: %w", resp.Status, ErrGone)
	}
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
