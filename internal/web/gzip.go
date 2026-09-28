package web

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"
)

// Responses are gzip-compressed when the browser accepts it and they're
// text (HTML, CSS, JavaScript, JSON, SVG): pages shrink to about a fifth.
// Anything else, and range requests, go out as they are.

var gzipWriters = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(nil, gzip.DefaultCompression); return w }}

func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if !acceptsGzip(r) || r.Header.Get("Range") != "" {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponse{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, q, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.TrimSpace(enc) == "gzip" && strings.ReplaceAll(q, " ", "") != "q=0" {
			return true
		}
	}
	return false
}

// gzipResponse compresses the body if, when the handler starts writing,
// the response turns out to be compressible.
type gzipResponse struct {
	http.ResponseWriter
	decided bool
	gz      *gzip.Writer
}

func (g *gzipResponse) decide(status int) {
	if g.decided {
		return
	}
	g.decided = true
	h := g.Header()
	if status < 200 || status == http.StatusNoContent || status == http.StatusNotModified || status == http.StatusPartialContent ||
		h.Get("Content-Encoding") != "" || !compressible(h.Get("Content-Type")) {
		return
	}
	h.Del("Content-Length")
	h.Set("Content-Encoding", "gzip")
	g.gz = gzipWriters.Get().(*gzip.Writer)
	g.gz.Reset(g.ResponseWriter)
}

func compressible(contentType string) bool {
	ct, _, _ := strings.Cut(contentType, ";")
	return strings.HasPrefix(ct, "text/") || ct == "application/javascript" || ct == "application/json" || ct == "image/svg+xml"
}

func (g *gzipResponse) WriteHeader(status int) {
	g.decide(status)
	g.ResponseWriter.WriteHeader(status)
}

func (g *gzipResponse) Write(b []byte) (int, error) {
	if !g.decided {
		if g.Header().Get("Content-Type") == "" {
			g.Header().Set("Content-Type", http.DetectContentType(b))
		}
		g.WriteHeader(http.StatusOK)
	}
	if g.gz != nil {
		return g.gz.Write(b)
	}
	return g.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection.
func (g *gzipResponse) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func (g *gzipResponse) close() {
	if g.gz != nil {
		g.gz.Close()
		gzipWriters.Put(g.gz)
		g.gz = nil
	}
}
