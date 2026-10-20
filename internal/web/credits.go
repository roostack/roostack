package web

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Credit is open-source work Roostack includes or builds on. This list is the
// source for the About page and THIRD_PARTY.md (a test regenerates the
// file and checks every Go module in the binary is listed).
type Credit struct {
	Name, By, URL, License string
	// Module is the Go module path, for Go code compiled into Roostack.
	Module string
	Note   string
}

// Included in the roostack binary.
var bundledCredits = []Credit{
	{Name: "Go standard library", By: "The Go Authors", URL: "https://go.dev", License: "BSD-3-Clause"},
	{Name: "htmx", URL: "https://htmx.org", License: "0BSD", Note: "Page updates without reloading"},
	{Name: "Alpine.js", URL: "https://alpinejs.dev", License: "MIT", Note: "Small interactive bits of pages"},
	{Name: "yaml", By: "Eemeli Aro", URL: "https://eemeli.org/yaml", License: "ISC", Note: "Reading and writing YAML in the package editor, comments kept"},
	{Name: "Space Grotesk", By: "The Space Grotesk Project Authors", URL: "https://github.com/floriankarsten/space-grotesk",
		License: "SIL Open Font License 1.1", Note: "The font for headings and figures"},
	{Name: "SQLite for Go", By: "The Sqlite Authors", URL: "https://gitlab.com/cznic/sqlite", License: "BSD-3-Clause",
		Module: "modernc.org/sqlite", Note: "Roostack's database (SQLite itself is public domain)"},
	{Name: "libc, mathutil, memory", By: "The modernc.org Authors", URL: "https://gitlab.com/cznic", License: "BSD-3-Clause",
		Module: "modernc.org/libc modernc.org/mathutil modernc.org/memory", Note: "Used by SQLite for Go"},
	{Name: "Go crypto and sys", By: "The Go Authors", URL: "https://go.googlesource.com/crypto", License: "BSD-3-Clause",
		Module: "golang.org/x/crypto golang.org/x/sys", Note: "Password hashing (argon2id)"},
	{Name: "YAML for Go", By: "The go-yaml Authors", URL: "https://github.com/yaml/go-yaml", License: "MIT and Apache 2.0",
		Module: "go.yaml.in/yaml/v3"},
	{Name: "qr", By: "The Go Authors (Russ Cox)", URL: "https://pkg.go.dev/rsc.io/qr", License: "BSD-3-Clause",
		Module: "rsc.io/qr", Note: "QR codes for two-factor setup"},
	{Name: "uuid", By: "Google", URL: "https://github.com/google/uuid", License: "BSD-3-Clause", Module: "github.com/google/uuid"},
	{Name: "go-humanize", By: "Dustin Sallings", URL: "https://github.com/dustin/go-humanize", License: "MIT",
		Module: "github.com/dustin/go-humanize"},
	{Name: "bigfft", By: "Rémy Oudompheng", URL: "https://github.com/remyoudompheng/bigfft", License: "BSD-3-Clause",
		Module: "github.com/remyoudompheng/bigfft"},
}

// Programs Roostack installs or runs alongside it (not part of Roostack).
var programCredits = []Credit{
	{Name: "Podman", URL: "https://podman.io", License: "Apache 2.0", Note: "Runs services in containers"},
	{Name: "Caddy", URL: "https://caddyserver.com", License: "Apache 2.0", Note: "Web addresses and HTTPS certificates"},
	{Name: "restic", URL: "https://restic.net", License: "BSD-2-Clause", Note: "Backups"},
	{Name: "Debian", URL: "https://www.debian.org", License: "Various free licences", Note: "The operating system, and its updates"},
}

// themeCredits are the open-source palettes behind built-in themes.
func themeCredits() []Credit {
	var out []Credit
	for _, t := range Themes {
		if t.Source != nil {
			c := *t.Source
			c.Note = "Colours for the " + t.Name + " theme"
			out = append(out, c)
		}
	}
	return out
}

// aboutPage shows Roostack's version and the open-source work it builds on.
func (s *Server) aboutPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "about", map[string]any{"User": currentUser(r), "Version": s.Version,
		"Bundled": bundledCredits, "Themes": themeCredits(), "Programs": programCredits})
}

// writeThirdParty writes THIRD_PARTY.md from the same lists.
func writeThirdParty(w io.Writer) {
	fmt.Fprint(w, "# Third-party software\n\n"+
		"Generated from internal/web/credits.go (go test ./internal/web -run TestThirdPartyFile -update-credits).\n"+
		"Roostack shows the same list on its About page.\n")
	section := func(title, intro string, cs []Credit) {
		fmt.Fprintf(w, "\n## %s\n\n%s\n\n| Name | Licence | By | Notes |\n|---|---|---|---|\n", title, intro)
		for _, c := range cs {
			note := c.Note
			if c.Module != "" {
				note = strings.TrimSpace(note + " (Go: `" + strings.ReplaceAll(c.Module, " ", "`, `") + "`)")
			}
			fmt.Fprintf(w, "| [%s](%s) | %s | %s | %s |\n", c.Name, c.URL, c.License, c.By, note)
		}
	}
	section("Included in Roostack", "Compiled into the `roostack` binary or served by it.", bundledCredits)
	section("Colour themes", "Palettes behind built-in themes (`internal/web/static/css/themes`). Roostack adjusts a few colours so text stays readable.", themeCredits())
	section("Programs Roostack uses", "Installed alongside Roostack and run by it; not part of Roostack.", programCredits)
}
