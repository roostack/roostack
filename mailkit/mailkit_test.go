package mailkit

import (
	"bytes"
	"encoding/base64"
	"flag"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// verify is a Cloud email written as text, as Cloud's are.
const verify = "Thanks for signing up for Roostack Cloud. To finish, open this link, which will work for a day:\n\n" +
	"→ Verify your email address: https://roost.ac/verify/k3J9vQ2m\n\n" +
	"If you didn't sign up for Roostack Cloud, you can ignore this email.\n"

// labNotice is a lab's notification, written as blocks.
var labNotice = Email{Brand: Roostack, Heading: "Home Lab",
	Blocks: []Block{
		Paragraph("Hello tom,"),
		Section{Title: "Security", Tone: Highlight, Items: []Item{{Text: "Someone logged in to your account from a new browser or device.", Tone: Highlight}}},
		Section{Title: "Needs attention", Tone: Bad, Items: []Item{
			{Text: "nas's last backup failed.", URL: "https://home.example.com/backups", LinkText: "See backups", Tone: Bad},
			{Text: "The disk Data on nas is 91% full.", Tone: Warn},
		}},
		Section{Title: "Still needs attention", Tone: Warn, Items: []Item{
			{Text: "Roostack Cloud hasn't been reachable.", URL: "https://home.example.com/settings/cloud", LinkText: "Roostack Cloud",
				Detail: "(since 20 October at 02:40)", Tone: Warn},
		}},
	},
	Footer: []Block{Paragraph("Roostack, for Home Lab."), Link{"Choose which emails you get", "https://home.example.com/account/notifications"}},
}

func TestFromText(t *testing.T) {
	text := "You've closed your account.\n\n" +
		"- Your labs are unlinked.\n" +
		"- Backups are kept.\n" +
		"  until 21 November\n" +
		"- A case:\n" +
		"  https://console.example/abuse/4\n" +
		"  (since Monday)\n\n" +
		"Its signals:\n" +
		"- one\n" +
		"After the list, on its own line.\n" +
		"  indented: 12 GB\n\n" +
		"→ Reset your password: https://roost.ac/reset/abc\n" +
		"→ not an action https://x\n"
	want := []Block{
		Paragraph("You've closed your account."),
		List{{Text: "Your labs are unlinked."}, {Text: "Backups are kept.", Detail: "until 21 November"},
			{Text: "A case:", URL: "https://console.example/abuse/4", Detail: "(since Monday)"}},
		Paragraph("Its signals:"),
		List{{Text: "one"}},
		Paragraph("After the list, on its own line.\nindented: 12 GB"),
		Action{Label: "Reset your password", URL: "https://roost.ac/reset/abc"},
		Paragraph("→ not an action https://x"),
	}
	if got := FromText(text); !reflect.DeepEqual(got, want) {
		t.Errorf("FromText:\n got %#v\nwant %#v", got, want)
	}
}

// Text and FromText agree: what one writes, the other reads back.
func TestTextRoundTrip(t *testing.T) {
	blocks := []Block{
		Paragraph("Hello,\nsecond line"),
		List{{Text: "a"}, {Text: "b", URL: "https://example.com/b", Detail: "(since then)"}},
		Action{Label: "Verify your email address", URL: "https://roost.ac/verify/x"},
		Paragraph("The end."),
	}
	text := Email{Blocks: blocks}.Text()
	if got := FromText(text); !reflect.DeepEqual(got, blocks) {
		t.Errorf("FromText(Text()):\n got %#v\nwant %#v\ntext:\n%s", got, blocks, text)
	}
}

func TestText(t *testing.T) {
	want := "Hello tom,\n\n" +
		"Security:\n\n- Someone logged in to your account from a new browser or device.\n\n" +
		"Needs attention:\n\n- nas's last backup failed.\n  https://home.example.com/backups\n- The disk Data on nas is 91% full.\n\n" +
		"Still needs attention:\n\n- Roostack Cloud hasn't been reachable.\n  https://home.example.com/settings/cloud\n  (since 20 October at 02:40)\n\n" +
		"-- \nRoostack, for Home Lab.\nChoose which emails you get: https://home.example.com/account/notifications\n"
	if got := labNotice.Text(); got != want {
		t.Errorf("Text:\n%s\nwant:\n%s", got, want)
	}
}

func TestSegments(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []seg
	}{
		{"reopen it (https://roost.ac/login); then", []seg{{Text: "reopen it ("}, {"https://roost.ac/login", "https://roost.ac/login"}, {Text: "); then"}}},
		{"The case: https://c.example/abuse/4.", []seg{{Text: "The case: "}, {"https://c.example/abuse/4", "https://c.example/abuse/4"}, {Text: "."}}},
		{"https://en.wikipedia.org/wiki/Hen_(bird)", []seg{{"https://en.wikipedia.org/wiki/Hen_(bird)", "https://en.wikipedia.org/wiki/Hen_(bird)"}}},
		{"javascript:alert(1) and ftp://x", []seg{{Text: "javascript:alert(1) and ftp://x"}}},
	} {
		if got := segments(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("segments(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

// Text from people (a lab's name, an abuse report) can't add markup.
func TestHTMLEscapes(t *testing.T) {
	e := Email{Heading: `<script>alert("h")</script>`, Blocks: []Block{
		Paragraph(`<b>bold</b> & https://example.com/?a=1&b="2"`),
		Section{Title: "<i>", Items: []Item{{Text: "<img src=x>", URL: `https://example.com/"onmouseover="x`, LinkText: "<u>"}}},
		Action{Label: "<em>Go</em>", URL: "javascript:alert(1)"},
	}}
	h, err := e.HTML()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script>", "<b>", "<i>", "<img src=x>", "<u>", "<em>", `"onmouseover`, `href="javascript:`} {
		if strings.Contains(h, bad) {
			t.Errorf("the HTML has %s unescaped", bad)
		}
	}
	if !strings.Contains(h, `href="https://example.com/?a=1&amp;b="`) {
		t.Errorf("the address in the paragraph isn't a link up to its quote")
	}
}

func TestHTMLGolden(t *testing.T) {
	for name, e := range map[string]Email{
		"cloud-verify": {Brand: Cloud, Heading: "Verify your email address for Roostack Cloud", Blocks: FromText(verify),
			Footer: []Block{Paragraph("Roostack Cloud sent this email to tom@example.com."), Link{"roost.ac", "https://roost.ac"}}},
		"lab-notice": labNotice,
	} {
		h, err := e.HTML()
		if err != nil {
			t.Fatal(err)
		}
		golden(t, filepath.Join("testdata", name+".html"), h)
	}
}

func golden(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (go test ./mailkit -update writes it)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from what HTML writes (go test ./mailkit -update, and read the diff)", path)
	}
}

// Body reads back as the text, the HTML and the mark the HTML names, in
// short 7-bit lines.
func TestBody(t *testing.T) {
	text := "Hello – a long line " + strings.Repeat("and more ", 20) + "\n"
	for _, brand := range []Brand{Roostack, Cloud} {
		ct, body, err := Body(text, Email{Brand: brand, Heading: "Hi", Blocks: FromText(text)})
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(string(body), "\r\n") {
			// Header lines may be longer (up to 998), but no line of a part's body.
			if len(l) > 76 && !strings.HasPrefix(l, "Content-") || strings.IndexFunc(l, func(r rune) bool { return r > '~' }) >= 0 {
				t.Fatalf("a line that isn't short 7-bit: %q", l)
			}
		}
		mt, params, err := mime.ParseMediaType(ct)
		if err != nil || mt != "multipart/alternative" {
			t.Fatalf("Content-Type %q: %v", ct, err)
		}
		parts := readParts(t, bytes.NewReader(body), params["boundary"])
		if len(parts) != 2 || parts[0].typ != "text/plain" || parts[1].typ != "multipart/related" {
			t.Fatalf("the parts are %v", parts)
		}
		if got := string(parts[0].body); got != strings.ReplaceAll(text, "\n", "\r\n") {
			t.Errorf("the text part is %q", got)
		}
		related := readParts(t, bytes.NewReader(parts[1].body), parts[1].params["boundary"])
		if len(related) != 2 || related[0].typ != "text/html" || related[1].typ != "image/png" {
			t.Fatalf("the related parts are %v", related)
		}
		if !strings.Contains(string(related[0].body), `src="cid:`+markID+`"`) || related[1].id != "<"+markID+">" {
			t.Errorf("the HTML doesn't name the mark (%s)", related[1].id)
		}
		want := markRoostack
		if brand == Cloud {
			want = markCloud
		}
		if !bytes.Equal(related[1].body, want) || !bytes.HasPrefix(want, []byte("\x89PNG")) {
			t.Errorf("brand %d: the mark isn't its PNG", brand)
		}
	}
}

type part struct {
	typ, id string
	params  map[string]string
	body    []byte
}

func readParts(t *testing.T, r io.Reader, boundary string) []part {
	t.Helper()
	var out []part
	mr := multipart.NewReader(r, boundary)
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		typ, params, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		var body []byte
		switch p.Header.Get("Content-Transfer-Encoding") {
		case "quoted-printable":
			body, err = io.ReadAll(quotedprintable.NewReader(p))
		case "base64":
			body, err = io.ReadAll(base64Reader(p))
		default:
			body, err = io.ReadAll(p)
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, part{typ, p.Header.Get("Content-Id"), params, body})
	}
}

// base64Reader decodes base64 (which skips the line breaks).
func base64Reader(r io.Reader) io.Reader { return base64.NewDecoder(base64.StdEncoding, r) }
