package errpage

import (
	"strings"
	"testing"
)

// Each scene gets its own wash ID; an unknown one is nothing.
func TestScene(t *testing.T) {
	a, b := Scene("asleep", "1"), Scene("asleep", "2")
	if !strings.Contains(a, `id="es-wash-1"`) || !strings.Contains(a, `url(#es-wash-1)`) || strings.Contains(a, "@ID@") ||
		!strings.Contains(b, `id="es-wash-2"`) {
		t.Errorf("IDs: %.200s", a)
	}
	if Scene("nope", "1") != "" || len(Names()) != 12 {
		t.Errorf("names: %v", Names())
	}
}

// Caddy's pages: one per status, and a catch-all; the service's title from
// its route, escaped; the lab's name, the link to Roostack and the
// details only on a private service's.
func TestCaddyPages(t *testing.T) {
	pages := CaddyPages(`The Smiths' <lab>`, "https://roost.lab.example.com")
	for _, name := range []string{"403.html", "404.html", "429.html", "502.html", "503.html", "504.html", "error.html"} {
		if pages[name] == "" {
			t.Errorf("no %s", name)
		}
	}
	p := pages["502.html"]
	for _, want := range []string{tplService + " is asleep on the job.", `class="es-scene"`, `{{placeholder "http.error.status_code"}}`,
		"The Smiths&#39; &lt;lab&gt;", `href="https://roost.lab.example.com{{with`, "Details"} {
		if !strings.Contains(p, want) {
			t.Errorf("502 missing %q", want)
		}
	}
	// Everything about the lab is inside the private block.
	i := strings.Index(p, tplPrivate)
	if i < 0 || strings.Index(p, "Smiths") < i || strings.Index(p, "roost.lab.example.com") < i || strings.Index(p, "<details>") < i {
		t.Error("the lab's name, link and details only for a private service")
	}
	if !strings.Contains(pages["503.html"], `http-equiv="refresh"`) || strings.Contains(p, `http-equiv="refresh"`) {
		t.Error("only the starting page reloads itself")
	}
}

// The 503 page is the paused page (SPEC.md §6.16) when the route set
// caddy.BlockedVar, and "Still hatching" otherwise; the paused page doesn't
// reload itself, offer "Try again", say why, or name the lab.
func TestPausedPage(t *testing.T) {
	p := CaddyPages(`The Smiths' lab`, "https://roost.lab.example.com")["503.html"]
	blocked := `{{if eq (placeholder "http.vars.roostack_blocked") "yes"}}`
	if !strings.HasPrefix(p, blocked) {
		t.Fatalf("503.html doesn't start by choosing:\n%.300s", p)
	}
	paused, hatching, ok := strings.Cut(strings.TrimPrefix(p, blocked), "</html>{{else}}<!doctype")
	if !ok || !strings.Contains(paused, "<h1>Resting its wings.</h1>") || !strings.Contains(paused, tplService+" is closed to visitors from the internet") ||
		!strings.Contains(hatching, "Still hatching.") || !strings.HasSuffix(strings.TrimSpace(hatching), "{{end}}") {
		t.Errorf("the two pages:\n%s", p)
	}
	for _, leak := range []string{"Smiths", "roost.lab.example.com", "Try again", "<details>", "refresh", "security", "http.error.message"} {
		if strings.Contains(paused, leak) {
			t.Errorf("the paused page has %q", leak)
		}
	}
}

// The refused page tells a ban from a request AppSec matched by the var
// the route sets before AppSec, says nothing about the request or the lab,
// and offers no "Try again".
func TestRefusedPage(t *testing.T) {
	p := CaddyPages(`The Smiths' lab`, "https://roost.lab.example.com")["403.html"]
	waf := `{{if eq (placeholder "http.vars.roostack_waf") "yes"}}`
	i, j := strings.Index(p, waf+"<h1>"+fill(exploitPage.Headline)), strings.Index(p, "{{else}}<h1>"+fill(bannedPage.Headline))
	if i < 0 || j < i || !strings.Contains(p, "<title>"+waf) || !strings.Contains(p, tplService+", because it looked like an attack") {
		t.Errorf("the two refusals:\n%s", p)
	}
	for _, leak := range []string{"Smiths", "roost.lab.example.com", ".Host", "http.request", "http.error.message", "Try again", "<details>"} {
		if strings.Contains(p, leak) {
			t.Errorf("the refused page has %q", leak)
		}
	}
}
