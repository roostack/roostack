package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestComponents(t *testing.T) {
	html, err := notice("warn", `Disk "full" <soon>`, "link=/backups", "linkText=Improve backups")
	if err != nil || !strings.Contains(string(html), `class="notice notice-warn"`) ||
		!strings.Contains(string(html), `Disk &#34;full&#34; &lt;soon&gt;`) || !strings.Contains(string(html), `<a href="/backups">Improve backups</a>`) {
		t.Errorf("notice: %s %v", html, err)
	}
	if html, _ := notice("info", "Checking…"); !strings.Contains(string(html), `class="notice"`) {
		t.Errorf("info notice: %s", html)
	}
	for name, err := range map[string]error{
		"unknown kind":   second(notice("wran", "x")),
		"unknown option": second(notice("ok", "x", "href=/")),
		"badge variant":  second(badge("New", "loud")),
		"confirm kind":   second(confirmButton("/x", "Sure?", "Go", "primary")),
		"confirm field":  second(confirmButton("/x", "Sure?", "Go", "", "path")),
	} {
		if err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	html, err = confirmButton("/users/3/remove", `Remove "dad"?`, "Remove", "quiet danger", "path=/srv/a b", "x=1")
	if err != nil || !strings.Contains(string(html), `Remove &#34;dad&#34;?</p>`) || !strings.Contains(string(html), "<dialog") ||
		!strings.Contains(string(html), `x-on:click="$refs.dialog.showModal()"`) || strings.Contains(string(html), "confirm(") ||
		!strings.Contains(string(html), `<input type="hidden" name="path" value="/srv/a b">`) ||
		!strings.Contains(string(html), `<input type="hidden" name="x" value="1">`) || !strings.Contains(string(html), `class="quiet danger"`) {
		t.Errorf("confirm: %s %v", html, err)
	}
	if html, err := activityBlock(nil); html != "" || err != nil {
		t.Errorf("no activity should render nothing: %q %v", html, err)
	}
}

func second[T any](_ T, err error) error { return err }

// Every link that opens a new window says so (the new-window component)
// and doesn't hand the new page a way back into Roostack (rel="noopener").
func TestNewWindowLinks(t *testing.T) {
	link := regexp.MustCompile(`(?s)<a\s[^>]*target="_blank"[^>]*>.*?</a>`)
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		page, _ := fs.ReadFile(templateFS, f)
		for _, a := range link.FindAllString(string(page), -1) {
			n++
			if !strings.Contains(a, `{{template "new-window"}}`) || !strings.Contains(a, `rel="noopener"`) {
				t.Errorf("%s: a new-window link without the new-window component or rel=\"noopener\": %.120s", f, a)
			}
		}
	}
	if n < 8 {
		t.Errorf("found only %d new-window links; is the pattern still right?", n)
	}
}
