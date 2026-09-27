package web

import (
	"regexp"
	"strings"
)

// formatHTML indents rendered HTML for the design gallery's "renders as"
// view: block elements go on their own lines, inline ones stay in the
// line, and <pre> content is left exactly as it is. It's for reading,
// not a general-purpose formatter.
func formatHTML(s string) string {
	var b strings.Builder
	depth := 0
	pre := 0
	lineOpen := false
	newline := func() {
		if lineOpen {
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat("  ", max(depth, 0)))
		lineOpen = true
	}
	for _, tok := range htmlToken.FindAllString(s, -1) {
		if pre > 0 {
			b.WriteString(tok)
			if strings.HasPrefix(tok, "</pre") {
				pre--
			}
			continue
		}
		if !strings.HasPrefix(tok, "<") {
			text := strings.Join(strings.Fields(tok), " ")
			if text == "" {
				continue
			}
			if !lineOpen {
				newline()
			}
			b.WriteString(text)
			continue
		}
		name := tagName(tok)
		closing := strings.HasPrefix(tok, "</")
		switch {
		case !blockTags[name]:
			if !lineOpen {
				newline()
			}
			b.WriteString(tok)
		case closing:
			depth--
			if !lastWasOpen(b.String(), name) {
				newline()
			}
			b.WriteString(tok)
		default:
			newline()
			b.WriteString(tok)
			if name == "pre" {
				pre++
				continue
			}
			if !voidTags[name] {
				depth++
			}
		}
	}
	return strings.TrimSpace(b.String())
}

var htmlToken = regexp.MustCompile(`<[^>]+>|[^<]+`)

func tagName(tok string) string {
	t := strings.TrimLeft(tok, "</")
	if i := strings.IndexAny(t, " \t\n/>"); i >= 0 {
		t = t[:i]
	}
	return strings.ToLower(t)
}

// lastWasOpen reports whether the output so far ends with the line that
// opened this element, so short elements like <li>…</li> stay on one line.
func lastWasOpen(out, name string) bool {
	line := out[strings.LastIndex(out, "\n")+1:]
	return strings.HasPrefix(strings.TrimSpace(line), "<"+name) && !strings.Contains(line[strings.Index(line, "<"+name)+1:], "<"+name)
}

var blockTags = map[string]bool{
	"div": true, "section": true, "ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
	"p": true, "form": true, "fieldset": true, "legend": true, "label": true, "details": true, "summary": true,
	"pre": true, "h1": true, "h2": true, "h3": true, "nav": true, "table": true, "tr": true, "td": true, "th": true,
	"input": true, "select": true, "button": true,
}

var voidTags = map[string]bool{"input": true, "br": true, "img": true, "hr": true}
