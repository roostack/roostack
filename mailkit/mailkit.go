// Package mailkit writes Roostack's and Roostack Cloud's emails in HTML,
// beside their plain text (SPEC.md §6.13, "Email in HTML"): one look, in
// the default theme's colours, with the brand's mark embedded in each
// email, so nothing is fetched from elsewhere and nothing tracks who read
// it. Public, not under internal, so Roostack Cloud's repository uses the
// same code, as with authkit; each side keeps its own wording and headers.
//
// An email is blocks (Paragraph, List, Section, Action, Link). Text writes
// them as plain text, and FromText reads plain text back into blocks, so
// an email written as text gets its HTML without changing a word: blank
// lines make paragraphs, lines starting "- " a list, an indented line under
// an item that item's link or detail, a line "→ Label: https://…" a button
// (ActionLine), and web addresses links.
package mailkit

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"html/template"
	"io"
	"mime/multipart"
	"mime/quotedprintable"
	"net/textproto"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Brand is whose email it is: its mark and its name in the header.
type Brand int

const (
	// Roostack is a lab's email: the hen on a server.
	Roostack Brand = iota
	// Cloud is Roostack Cloud's: the hen on a cloud, and "roostack cloud".
	Cloud
)

// The marks, made by assets/brand/generate.py (96px, for 36px on a
// high-density screen).
var (
	//go:embed mark.png
	markRoostack []byte
	//go:embed mark-cloud.png
	markCloud []byte
)

// markID is the mark's Content-ID, which the HTML names (cid:).
const markID = "mark@roostack.cloud"

// Tone is a notice's status colour, as in the web UI: the bar beside a
// section's item and the dot by its title.
type Tone string

const (
	Plain     Tone = ""
	Highlight Tone = "hi" // security
	Bad       Tone = "bad"
	Warn      Tone = "warn"
	OK        Tone = "ok"
)

// Email is what an email says. Heading tops the card (the subject, or a
// lab's name); Footer is under it, small (who sent it, and why).
type Email struct {
	Brand   Brand
	Heading string
	Blocks  []Block
	Footer  []Block
}

// Block is one part of an email: Paragraph, List, Section, Action or Link.
type Block interface{ block() }

// Paragraph is text whose line breaks are kept; web addresses in it
// become links.
type Paragraph string

// List is a bulleted list.
type List []Item

// Section is a titled group of notices, each in a box marked with its
// tone (a lab's "Needs attention"); Tone colours the title's dot. With no
// items it's a heading for the blocks after it.
type Section struct {
	Title string
	Tone  Tone
	Items []Item
}

// Item is one entry of a List or Section: its text, a muted Detail under
// it, and a link (LinkText, or the address itself, in HTML).
type Item struct {
	Text, Detail  string
	URL, LinkText string
	Tone          Tone
}

// Action is a button, with its address written out under it (to copy, or
// to see where it goes).
type Action struct{ Label, URL string }

// Link is a link on its own line: "Label: URL" in plain text.
type Link struct{ Label, URL string }

func (Paragraph) block() {}
func (List) block()      {}
func (Section) block()   {}
func (Action) block()    {}
func (Link) block()      {}

// ActionLine is an Action as a line of plain text, which FromText reads
// back as a button: an email written as text asks for one this way.
func ActionLine(label, url string) string {
	return "→ " + label + ": " + url
}

// Text is e as plain text: blocks apart by a blank line, and the footer
// after the signature line ("-- ").
func (e Email) Text() string {
	var b strings.Builder
	for i, bl := range e.Blocks {
		if i > 0 {
			b.WriteString("\n\n")
		}
		writeText(&b, bl)
	}
	if len(e.Footer) > 0 {
		if len(e.Blocks) > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("-- ")
		for _, bl := range e.Footer {
			b.WriteString("\n")
			writeText(&b, bl)
		}
	}
	b.WriteString("\n")
	return b.String()
}

func writeText(b *strings.Builder, bl Block) {
	switch bl := bl.(type) {
	case Paragraph:
		b.WriteString(string(bl))
	case List:
		writeItems(b, bl)
	case Section:
		b.WriteString(bl.Title + ":")
		if len(bl.Items) > 0 {
			b.WriteString("\n\n")
			writeItems(b, bl.Items)
		}
	case Action:
		b.WriteString(ActionLine(bl.Label, bl.URL))
	case Link:
		b.WriteString(bl.Label + ": " + bl.URL)
	}
}

// writeItems writes "- " items, with each one's link, then detail,
// indented on the lines under it.
func writeItems(b *strings.Builder, items []Item) {
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- " + indent(it.Text))
		if it.URL != "" {
			b.WriteString("\n  " + it.URL)
		}
		if it.Detail != "" {
			b.WriteString("\n  " + indent(it.Detail))
		}
	}
}

func indent(s string) string { return strings.ReplaceAll(s, "\n", "\n  ") }

// actionLine is ActionLine's form.
var actionLine = regexp.MustCompile(`^→ (\S.*?): (https?://\S+)$`)

// FromText reads plain text as blocks (see the package's comment).
func FromText(text string) []Block {
	var (
		out  []Block
		para []string
		list List
	)
	flush := func() {
		if len(para) > 0 {
			out = append(out, Paragraph(strings.Join(para, "\n")))
			para = nil
		}
		if len(list) > 0 {
			out = append(out, list)
			list = nil
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
		case strings.HasPrefix(line, "- "):
			if len(para) > 0 {
				flush()
			}
			list = append(list, Item{Text: strings.TrimSpace(line[2:])})
		case len(list) > 0 && strings.HasPrefix(line, "  "):
			it := &list[len(list)-1]
			switch {
			case it.URL == "" && it.Detail == "" && bareURL(trimmed):
				it.URL = trimmed
			case it.Detail != "":
				it.Detail += "\n" + trimmed
			default:
				it.Detail = trimmed
			}
		case actionLine.MatchString(trimmed):
			flush()
			m := actionLine.FindStringSubmatch(trimmed)
			out = append(out, Action{Label: m[1], URL: m[2]})
		default:
			if len(list) > 0 {
				flush()
			}
			para = append(para, trimmed)
		}
	}
	flush()
	return out
}

// webAddress is a web address in text, up to a space or what can't be in
// one unescaped.
var webAddress = regexp.MustCompile(`https?://[^\s<>"]+`)

func bareURL(s string) bool {
	return webAddress.FindString(s) == s
}

// seg is a run of text, a link if URL is set.
type seg struct{ Text, URL string }

// segments splits a line at its web addresses, leaving off the
// punctuation that ends a sentence after one ("…/login).").
func segments(line string) []seg {
	var out []seg
	for line != "" {
		loc := webAddress.FindStringIndex(line)
		if loc == nil {
			out = append(out, seg{Text: line})
			break
		}
		url := trimURL(line[loc[0]:loc[1]])
		if loc[0] > 0 {
			out = append(out, seg{Text: line[:loc[0]]})
		}
		out = append(out, seg{Text: url, URL: url})
		line = line[loc[0]+len(url):]
	}
	return out
}

func trimURL(u string) string {
	for u != "" {
		last := u[len(u)-1]
		switch {
		case strings.IndexByte(".,;:!?'", last) >= 0,
			last == ')' && strings.Count(u, "(") < strings.Count(u, ")"),
			last == ']' && strings.Count(u, "[") < strings.Count(u, "]"):
			u = u[:len(u)-1]
		default:
			return u
		}
	}
	return u
}

func lines(s string) [][]seg {
	var out [][]seg
	for _, l := range strings.Split(s, "\n") {
		out = append(out, segments(l))
	}
	return out
}

// view is what the template shows.
type view struct {
	Heading, Preview, MarkID string
	Cloud                    bool
	Blocks, Footer           []viewBlock
}

type viewBlock struct {
	Kind       string // p, list, section, action, link
	Lines      [][]seg
	Items      []viewItem
	Title      string
	Tone       Tone
	Label, URL string
}

type viewItem struct {
	Lines, Detail [][]seg
	URL, LinkText string
	Tone          Tone
}

func toView(bl Block) viewBlock {
	items := func(its []Item) []viewItem {
		var out []viewItem
		for _, it := range its {
			v := viewItem{Lines: lines(it.Text), URL: it.URL, LinkText: it.LinkText, Tone: it.Tone}
			if it.Detail != "" {
				v.Detail = lines(it.Detail)
			}
			if v.LinkText == "" {
				v.LinkText = it.URL
			}
			out = append(out, v)
		}
		return out
	}
	switch bl := bl.(type) {
	case Paragraph:
		return viewBlock{Kind: "p", Lines: lines(string(bl))}
	case List:
		return viewBlock{Kind: "list", Items: items(bl)}
	case Section:
		return viewBlock{Kind: "section", Title: bl.Title, Tone: bl.Tone, Items: items(bl.Items)}
	case Action:
		return viewBlock{Kind: "action", Label: bl.Label, URL: bl.URL}
	case Link:
		return viewBlock{Kind: "link", Label: bl.Label, URL: bl.URL}
	}
	return viewBlock{}
}

// toneColours are the status colours in light mode (tokens.css; the
// template's style block has dark mode's), and a plain item's border.
var toneColours = map[Tone]string{Plain: "#dbe3e2", Highlight: "#7c3aed", Bad: "#b3261e", Warn: "#8a4b00", OK: "#17603f"}

//go:embed email.html
var layout string

var tmpl = template.Must(template.New("email").Funcs(template.FuncMap{
	"toneColour": func(t Tone) template.CSS { return template.CSS(toneColours[t]) },
}).Parse(layout))

// previewLength is about what a mail client shows beside the subject.
const previewLength = 140

// HTML is e as an HTML page, made for email: tables and inline styles,
// light, with a style block for dark mode and narrow screens where the
// client reads one. The mark is cid:mark@roostack.cloud (Body attaches it).
func (e Email) HTML() (string, error) {
	v := view{Heading: e.Heading, MarkID: markID, Cloud: e.Brand == Cloud}
	for _, bl := range e.Blocks {
		vb := toView(bl)
		v.Blocks = append(v.Blocks, vb)
		if p, ok := bl.(Paragraph); ok && v.Preview == "" {
			v.Preview = preview(string(p))
		}
	}
	for _, bl := range e.Footer {
		v.Footer = append(v.Footer, toView(bl))
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, v); err != nil {
		return "", err
	}
	return b.String(), nil
}

// preview is a paragraph on one line, cut at about previewLength.
func preview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= previewLength {
		return s
	}
	r := []rune(s)[:previewLength]
	if i := strings.LastIndexByte(string(r), ' '); i > 0 {
		return string(r)[:i] + "…"
	}
	return string(r) + "…"
}

// Body is an email's MIME body: text, and e in HTML with the mark, as
// alternatives (multipart/alternative, the text first, so a client that
// shows only text shows it), with the Content-Type header line's value to
// write before it. Every part is 7-bit (quoted-printable, or base64 for
// the mark), so no line is longer than 76 characters.
func Body(text string, e Email) (contentType string, body []byte, err error) {
	html, err := e.HTML()
	if err != nil {
		return "", nil, err
	}
	var rel bytes.Buffer
	related := multipart.NewWriter(&rel)
	if err := writeQP(related, "text/html; charset=utf-8", html); err != nil {
		return "", nil, err
	}
	mark := markRoostack
	if e.Brand == Cloud {
		mark = markCloud
	}
	w, err := related.CreatePart(textproto.MIMEHeader{"Content-Type": {"image/png"}, "Content-Transfer-Encoding": {"base64"},
		"Content-Id": {"<" + markID + ">"}, "Content-Disposition": {`inline; filename="mark.png"`}})
	if err != nil {
		return "", nil, err
	}
	writeBase64(w, mark)
	if err := related.Close(); err != nil {
		return "", nil, err
	}

	var out bytes.Buffer
	alt := multipart.NewWriter(&out)
	if err := writeQP(alt, "text/plain; charset=utf-8", text); err != nil {
		return "", nil, err
	}
	w, err = alt.CreatePart(textproto.MIMEHeader{"Content-Type": {`multipart/related; type="text/html"; boundary="` + related.Boundary() + `"`}})
	if err != nil {
		return "", nil, err
	}
	if _, err := w.Write(rel.Bytes()); err != nil {
		return "", nil, err
	}
	if err := alt.Close(); err != nil {
		return "", nil, err
	}
	return `multipart/alternative; boundary="` + alt.Boundary() + `"`, out.Bytes(), nil
}

// writeQP adds a quoted-printable part, its lines ending in CRLF.
func writeQP(m *multipart.Writer, contentType, s string) error {
	w, err := m.CreatePart(textproto.MIMEHeader{"Content-Type": {contentType}, "Content-Transfer-Encoding": {"quoted-printable"}})
	if err != nil {
		return err
	}
	qp := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(qp, strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")); err != nil {
		return err
	}
	return qp.Close()
}

// writeBase64 writes data in base64, 76 characters to a line (RFC 2045).
func writeBase64(w io.Writer, data []byte) {
	s := base64.StdEncoding.EncodeToString(data)
	for len(s) > 76 {
		io.WriteString(w, s[:76]+"\r\n")
		s = s[76:]
	}
	io.WriteString(w, s+"\r\n")
}
