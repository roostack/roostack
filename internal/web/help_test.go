package web

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

// The help's CSP allows exactly its pages' inline scripts, and inline
// styles (see helpCSP).
func TestHelpCSP(t *testing.T) {
	site := fstest.MapFS{
		"helpsite/site/index.html": {Data: []byte(`<html><script>var a=1</script>` +
			`<script id="__config" type="application/json">{"base":"."}</script>` +
			`<script src="assets/bundle.js"></script><td style="text-align: center;">x</td></html>`)},
		"helpsite/site/start/index.html": {Data: []byte(`<script>var a=1</script><script>
var b=2</script>`)},
		"helpsite/site/assets/bundle.js": {Data: []byte(`<script>not a page</script>`)},
	}
	h, err := newHelpSite(site)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		cspHash([]byte("var a=1")), cspHash([]byte("\nvar b=2")),
		"style-src 'self' 'unsafe-inline';",
		"frame-ancestors 'none'",
	} {
		if !strings.Contains(h.csp, want) {
			t.Errorf("CSP lacks %s:\n%s", want, h.csp)
		}
	}
	for _, notWant := range []string{cspHash([]byte(`{"base":"."}`)), cspHash([]byte("not a page")), "unsafe-eval"} {
		if strings.Contains(h.csp, notWant) {
			t.Errorf("CSP has %s:\n%s", notWant, h.csp)
		}
	}
	// Each script once; no style hashes (they'd switch 'unsafe-inline' off).
	if strings.Count(h.csp, "'sha256-") != 2 {
		t.Errorf("each script once, and nothing else:\n%s", h.csp)
	}

	// Without a built site, /help/ says so.
	h, err = newHelpSite(fstest.MapFS{"helpsite/README.md": {}})
	if err != nil || h.files != nil {
		t.Fatalf("no site: %v %v", h.files, err)
	}
}

// /help/ is served to anyone, with a CSP that lets its own inline scripts
// run. It's embedded only after `make build`, so either way is fine here.
func TestHelpServed(t *testing.T) {
	h := newHarness(t, "")
	resp, err := h.client.Get(h.srv.URL + "/help")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if h.server.help.files == nil {
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "built without its help") {
			t.Errorf("no help built: %d %q", resp.StatusCode, body)
		}
		return
	}
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/help/" {
		t.Fatalf("help: %d at %s", resp.StatusCode, resp.Request.URL.Path)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	scripts := 0
	for _, m := range inlineScript.FindAllStringSubmatch(string(body), -1) {
		if strings.Contains(m[1], "src=") || strings.Contains(m[1], "type=") {
			continue
		}
		scripts++
		if !strings.Contains(csp, cspHash([]byte(m[2]))) {
			t.Errorf("CSP doesn't allow the page's script %.60q", m[2])
		}
	}
	if scripts == 0 {
		t.Error("expected Zensical's inline scripts on the page")
	}
	// The rest of the UI keeps its own policy.
	resp, err = h.client.Get(h.srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if strings.Contains(resp.Header.Get("Content-Security-Policy"), "sha256") {
		t.Error("the help's CSP leaked to the rest of the UI")
	}
}

// Every logged-in page links to the help, and the login page to getting
// back in.
func TestHelpLinks(t *testing.T) {
	h, _ := loggedIn(t, "member")
	if _, body := h.get("/"); !strings.Contains(body, `href="/help/"`) {
		t.Error("no Help link in the navigation")
	}
	h = newHarness(t, "")
	if _, body := h.get("/login"); !strings.Contains(body, `href="/help/trouble/locked-out/"`) {
		t.Error("no help link on the login page")
	}
}
