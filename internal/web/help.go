package web

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"io/fs"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
)

// The help site (SPEC.md §6.3.1): built from help/ with Zensical by
// `make build` into helpsite/site, embedded, and served at /help/ to
// anyone (it's the same as the public help site, and "Locked out" must be
// readable before logging in).
//
//go:embed helpsite
var helpFS embed.FS

// helpSite is the embedded help and the Content-Security-Policy its pages
// need.
type helpSite struct {
	// files is nil when this build has no help site.
	files fs.FS
	csp   string
}

// newHelpSite finds the built site in fsys (under helpsite/site) and works
// out its CSP.
func newHelpSite(fsys fs.FS) (*helpSite, error) {
	files, err := fs.Sub(fsys, "helpsite/site")
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(files, "index.html"); err != nil {
		return &helpSite{}, nil // not built: `make build` needs uv
	}
	csp, err := helpCSP(files)
	if err != nil {
		return nil, err
	}
	return &helpSite{files: files, csp: csp}, nil
}

// inlineScript matches a <script> element's attributes and content.
var inlineScript = regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)

// helpCSP is the UI's policy (securityHeaders), with two differences for
// Zensical's pages:
//
//   - Scripts: its pages have a few small inline scripts (the colour
//     scheme, the page's base address), which differ only by how deep the
//     page is. Exactly those are allowed, by their SHA-256 hashes, worked
//     out from the embedded files when Roostack starts, so they always
//     match the build.
//   - Styles: 'unsafe-inline'. Its search box injects a stylesheet held in
//     its minified script, and pages have style attributes (aligned table
//     columns). Hashing the stylesheet would mean picking a string out of
//     minified code, which breaks silently with a new Zensical. Inline
//     styles can't run code, and the help has nothing written by users to
//     inject into. (Browsers ignore 'unsafe-inline' if a hash is listed
//     too, so there are no style hashes.)
func helpCSP(files fs.FS) (string, error) {
	scripts := map[string]bool{}
	err := fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		page, err := fs.ReadFile(files, path)
		if err != nil {
			return err
		}
		for _, m := range inlineScript.FindAllSubmatch(page, -1) {
			attrs, body := string(m[1]), m[2]
			if strings.Contains(attrs, " src=") || len(body) == 0 {
				continue
			}
			// Data (type="application/json") isn't run, so isn't covered.
			if strings.Contains(attrs, " type=") && !strings.Contains(attrs, "javascript") && !strings.Contains(attrs, "module") {
				continue
			}
			scripts[cspHash(body)] = true
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	scriptSrc := "script-src 'self'"
	for _, h := range slices.Sorted(maps.Keys(scripts)) {
		scriptSrc += " " + h
	}
	return "default-src 'self'; " + scriptSrc + "; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'", nil
}

// cspHash is a CSP source for exactly this script or style text.
func cspHash(b []byte) string {
	sum := sha256.Sum256(b)
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// Handler serves the help site under /help/.
func (h *helpSite) Handler() http.Handler {
	if h.files == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "This copy of Roostack was built without its help pages "+
				"(`make build` builds them when uv is installed).", http.StatusNotFound)
		})
	}
	files := http.StripPrefix("/help", http.FileServerFS(noListings{h.files}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", h.csp)
		files.ServeHTTP(w, r)
	})
}
