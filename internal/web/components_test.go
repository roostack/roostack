package web

import (
	"io/fs"
	"os"
	"path/filepath"
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

func TestHelpLinkHref(t *testing.T) {
	for page, want := range map[string]string{
		"":                        `href="/help/"`,
		"backups":                 `href="/help/backups/"`,
		"settings/people#roles":   `href="/help/settings/people/#roles"`,
		"start/first-run/#1-step": `href="/help/start/first-run/#1-step"`,
	} {
		if html, err := helpLink(page, "Title"); err != nil || !strings.Contains(string(html), want) {
			t.Errorf("help %q: %s %v", page, html, err)
		}
	}
	for _, page := range []string{"/help/backups", "../etc"} {
		if _, err := helpLink(page, "Title"); err == nil {
			t.Errorf("help %q: want an error", page)
		}
	}
	if _, err := helpLink("backups", ""); err == nil {
		t.Error("help without a title: want an error")
	}
}

// Every help link, the component's and the few written out, names a page
// and section that the help has (the source in help/docs, so this works
// without building the site).
func TestPageHelpTargets(t *testing.T) {
	links := []*regexp.Regexp{
		regexp.MustCompile(`\{\{help "([^"]*)" "[^"]+"\}\}`),
		regexp.MustCompile(`href="/help/([^"]*)"`),
	}
	heading := regexp.MustCompile(`(?m)^#{1,6} +(.+?) *$`)
	docs := filepath.Join("..", "..", "help", "docs")
	files, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range files {
		page, _ := fs.ReadFile(templateFS, f)
		for _, link := range links {
			for _, m := range link.FindAllStringSubmatch(string(page), -1) {
				n++
				path, section, _ := strings.Cut(m[1], "#")
				path = strings.Trim(path, "/")
				src, err := os.ReadFile(filepath.Join(docs, path+".md"))
				if err != nil {
					src, err = os.ReadFile(filepath.Join(docs, path, "index.md"))
				}
				if err != nil {
					t.Errorf("%s: no help page %q", f, path)
					continue
				}
				found := section == ""
				for _, h := range heading.FindAllStringSubmatch(string(src), -1) {
					found = found || helpSlug(h[1]) == section
				}
				if !found {
					t.Errorf("%s: help page %q has no section #%s", f, path, section)
				}
			}
		}
	}
	if n < 20 {
		t.Errorf("found only %d help links; is the pattern still right?", n)
	}
}

// helpSlug is the id the help gives a heading: Python-Markdown's toc
// slugify, which Zensical uses (punctuation dropped, lower case, runs of
// spaces and hyphens to one hyphen).
func helpSlug(s string) string {
	s = regexp.MustCompile("[`*]").ReplaceAllString(s, "")
	s = strings.ToLower(strings.TrimSpace(regexp.MustCompile(`[^\w\s-]`).ReplaceAllString(s, "")))
	return regexp.MustCompile(`[-\s]+`).ReplaceAllString(s, "-")
}
