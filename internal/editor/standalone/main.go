// Command standalone writes the package editor as one HTML file, for
// opening with no network: the download on roostack.cloud (SPEC.md
// §8.3.7, "The package editor"). Run from the repository's root:
//
//	go run ./internal/editor/standalone -version 0.9.0 -o roostack-package-editor.html
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"roostack.cloud/roostack/internal/editor"
)

func main() {
	version := flag.String("version", "", "the Roostack release to suggest as requires.roostack")
	out := flag.String("o", "roostack-package-editor.html", "the file to write")
	css := flag.String("css", filepath.Join("internal", "web", "static", "css"), "the folder of the design system's tokens.css and themes")
	help := flag.String("help", "https://help.roostack.cloud/", "the help site's address")
	flag.Parse()
	var tokens []byte
	for _, f := range []string{"tokens.css", filepath.Join("themes", "teal.css")} {
		b, err := os.ReadFile(filepath.Join(*css, f))
		if err != nil {
			fail(err)
		}
		tokens = append(append(tokens, b...), '\n')
	}
	page, err := editor.Standalone(editor.Options{Version: *version, Help: *help}, tokens)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, page, 0o644); err != nil {
		fail(err)
	}
	fmt.Println("wrote", *out)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "standalone:", err)
	os.Exit(1)
}
