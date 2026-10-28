package imagecheck

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// registryServer is a registry asking for an anonymous token first, with
// one repository's tags in two pages and a manifest per tag.
func registryServer(t *testing.T) string {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			fmt.Fprint(w, `{"token":"t0k"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer t0k" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="`+srv.URL+`/token",service="test",scope="repository:org/app:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v2/org/app/tags/list" && r.URL.Query().Get("last") == "":
			w.Header().Set("Link", `</v2/org/app/tags/list?n=2&last=1.1>; rel="next"`)
			fmt.Fprint(w, `{"name":"org/app","tags":["1.0","1.1"]}`)
		case r.URL.Path == "/v2/org/app/tags/list":
			fmt.Fprint(w, `{"name":"org/app","tags":["1.2"]}`)
		case r.URL.Path == "/v2/org/app/manifests/1.2" && r.Method == http.MethodHead:
			w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("ab", 32))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	old := registryClient
	registryClient = srv.Client()
	t.Cleanup(func() { registryClient = old })
	return strings.TrimPrefix(srv.URL, "https://")
}

// Tags follows the pages, after logging in as the registry asks; TagDigest
// reads the digest from a HEAD, and a missing tag is ErrGone.
func TestTagsAndDigest(t *testing.T) {
	host := registryServer(t)
	tags, err := Tags(host, "org/app")
	if err != nil || !slices.Equal(tags, []string{"1.0", "1.1", "1.2"}) {
		t.Fatalf("tags %v, %v", tags, err)
	}
	d, err := TagDigest(host, "org/app", "1.2")
	if err != nil || d != "sha256:"+strings.Repeat("ab", 32) {
		t.Errorf("digest %q, %v", d, err)
	}
	if _, err := TagDigest(host, "org/app", "9.9"); err == nil || !strings.Contains(err.Error(), ErrGone.Error()) {
		t.Errorf("a missing tag: %v", err)
	}
}

// ParseRef splits a pinned image and puts it back together.
func TestParseRef(t *testing.T) {
	s := "ghcr.io/immich-app/immich-server:v2.1.0@sha256:" + strings.Repeat("0", 64)
	r, err := ParseRef(s)
	if err != nil || r.Registry != "ghcr.io" || r.Repo != "immich-app/immich-server" || r.Tag != "v2.1.0" || r.String() != s {
		t.Errorf("%+v %v", r, err)
	}
	if _, err := ParseRef("vaultwarden/server:1.0"); err == nil {
		t.Error("a reference without its registry was taken")
	}
}
