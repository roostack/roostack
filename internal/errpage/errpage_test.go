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
	if Scene("nope", "1") != "" || len(Names()) != 11 {
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
