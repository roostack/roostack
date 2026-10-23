// Package editor is Roostack's package editor: a page for writing a
// service's package file, which runs entirely in the browser (SPEC.md
// §8.3.7, "The package editor"). It imports a Docker Compose file, gives
// help and suggestions for every field, and checks the package as
// `roostack validate` does.
//
// It runs in three places from the same files: inside Roostack (Add your
// own), on roostack.cloud, and as one HTML file opened with no network
// (Standalone). So it's plain scripts, no modules (a page opened from a
// file can't load them) and no Alpine (the website's CSP has no
// 'unsafe-eval'); everything it knows about the format comes from
// pkgspec.Describe, through static/schema.js, which a test keeps current.
package editor

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"strings"
)

//go:embed static
var static embed.FS

// Files are the editor's files: its scripts, in the order a page loads
// them (Scripts), its stylesheet (Stylesheet), its markup (editor.html)
// and the list of them all (scripts.json).
func Files() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return sub
}

// editorManifest is static/scripts.json, the one list of the files a page
// loads: Roostack's pages, the Node tests (test/load.js) and the copy on
// roostack.cloud (Cloud's tools/sync-editor.sh) all read it, and
// TestManifest checks it names every script there is.
type editorManifest struct {
	// Scripts are the editor's scripts in the order a page loads them:
	// the YAML library, what Go says about the format, then the editor's
	// own, editor.js (the page itself) last.
	Scripts []string `json:"scripts"`
	// Stylesheet is the editor's own styles, from the design system's
	// purpose tokens (tokens.css, which the page loads too).
	Stylesheet string `json:"stylesheet"`
	// Markup is the editor's markup, which a page puts in its own frame
	// (Markup).
	Markup string `json:"markup"`
	// Vendored are the scripts that aren't Roostack's own (VENDORED.md).
	Vendored []string `json:"vendored"`
}

var manifest = func() editorManifest {
	var m editorManifest
	b, err := fs.ReadFile(Files(), "scripts.json")
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	if err != nil {
		panic("editor: scripts.json: " + err.Error())
	}
	return m
}()

// Scripts and Stylesheet are the manifest's, for a page to load.
var (
	Scripts    = manifest.Scripts
	Stylesheet = manifest.Stylesheet
)

// Where says where the editor runs, which changes what it offers: inside
// Roostack it can check a package with the server and add it to the lab.
type Where string

const (
	InRoostack Where = "roostack"
	OnWebsite  Where = "website"
	FromFile   Where = "file"
)

// Options are what a page tells the editor.
type Options struct {
	Where Where
	// Version is the Roostack release the editor came with, suggested
	// as a package's requires.roostack.
	Version string
	// Help is where the help site is: /help/ inside Roostack.
	Help string
	// Check and Add are the lab's addresses for checking a package and
	// adding it (InRoostack only).
	Check, Add string
}

// Markup returns the editor's markup for a page to put inside its own
// frame, with the options as data attributes the script reads.
func Markup(o Options) string {
	b, err := fs.ReadFile(Files(), manifest.Markup)
	if err != nil {
		panic(err)
	}
	attrs := fmt.Sprintf(`data-where="%s" data-version="%s" data-help="%s" data-check="%s" data-add="%s"`,
		html.EscapeString(string(o.Where)), html.EscapeString(o.Version), html.EscapeString(o.Help),
		html.EscapeString(o.Check), html.EscapeString(o.Add))
	return strings.Replace(string(b), `data-editor`, `data-editor `+attrs, 1)
}

// Standalone is the editor as one HTML file with everything in it (the
// design system's tokens and the Teal theme, given as tokensCSS, the
// styles and every script), for opening from a file with no network: the
// download on roostack.cloud.
func Standalone(o Options, tokensCSS []byte) ([]byte, error) {
	o.Where = FromFile
	var b bytes.Buffer
	b.WriteString("<!doctype html>\n<html lang=\"en\" data-palette=\"teal\">\n<head>\n<meta charset=\"utf-8\">\n" +
		"<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n" +
		"<title>Package editor · Roostack</title>\n<style>\n")
	b.Write(tokensCSS)
	css, err := fs.ReadFile(Files(), Stylesheet)
	if err != nil {
		return nil, err
	}
	b.WriteString("\n")
	b.Write(css)
	b.WriteString("\n</style>\n</head>\n<body class=\"pe-page\">\n")
	b.WriteString(Markup(o))
	for _, name := range Scripts {
		js, err := fs.ReadFile(Files(), name)
		if err != nil {
			return nil, err
		}
		// A script's text can't hold "</script" in an inline script; none
		// of ours does, which this keeps true.
		if bytes.Contains(bytes.ToLower(js), []byte("</script")) {
			return nil, fmt.Errorf("%s contains </script, which would end the inline script", name)
		}
		fmt.Fprintf(&b, "<script>/* %s */\n%s\n</script>\n", name, js)
	}
	b.WriteString("</body>\n</html>\n")
	return b.Bytes(), nil
}
