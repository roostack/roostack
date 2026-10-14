package web

import (
	"net/http"
	"strings"

	"roostack.cloud/roostack/internal/errpage"
)

// Roostack's own error pages (SPEC.md §6.3.4): a scene from
// internal/errpage, a joke headline and a plain explanation. Handlers keep
// answering with http.Error and http.NotFound; errorPages turns those into
// pages for a browser.

// ownPages are Roostack's pages, by status.
var ownPages = map[int]errpage.Page{
	http.StatusNotFound: {Status: http.StatusNotFound, Scene: "flown", Headline: "This page has flown the coop.",
		Text: "There's nothing at this address in Roostack. It may have moved, or the link is wrong."},
	http.StatusForbidden: {Status: http.StatusForbidden, Scene: "pecking", Headline: "That's above your pecking order.",
		Text: "Your account can't open this page. The lab's Owner can change what you can do."},
	http.StatusInternalServerError: {Status: http.StatusInternalServerError, Scene: "scrambled", Headline: "Roostack scrambled that one.",
		Text: "Something went wrong showing this page. The details are in Roostack's log; trying again often works."},
}

// errorPages shows the page for a plain-text 403, 404 or 500 (http.Error,
// http.NotFound) to a browser loading a page. Parts of pages (htmx) and
// other requests keep the plain answer, which their callers expect.
func (s *Server) errorPages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("HX-Request") != "" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&errorCatcher{ResponseWriter: w, s: s, r: r}, r)
	})
}

// errorCatcher swaps a plain-text error for its page.
type errorCatcher struct {
	http.ResponseWriter
	s       *Server
	r       *http.Request
	wrote   bool // the header went out
	swapped bool // the page was sent instead: drop the plain text
}

func (c *errorCatcher) WriteHeader(code int) {
	if c.wrote {
		return
	}
	c.wrote = true
	if _, ok := ownPages[code]; ok && strings.HasPrefix(c.Header().Get("Content-Type"), "text/plain") {
		c.swapped = true
		c.s.renderError(c.ResponseWriter, c.r, code)
		return
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *errorCatcher) Write(b []byte) (int, error) {
	if !c.wrote {
		c.WriteHeader(http.StatusOK)
	}
	if c.swapped {
		return len(b), nil
	}
	return c.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer (flushing).
func (c *errorCatcher) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// renderError writes an error's page with its status.
func (s *Server) renderError(w http.ResponseWriter, r *http.Request, code int) {
	p := ownPages[code]
	h := w.Header()
	h.Del("Content-Length")
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	data := map[string]any{"Page": p}
	if u := currentUser(r); u != nil {
		data["User"] = u
	}
	s.render(w, "error", data)
}
