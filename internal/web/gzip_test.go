package web

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompress(t *testing.T) {
	page := strings.Repeat("<p>Roostack</p>", 200)
	h := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/png":
			w.Header().Set("Content-Type", "image/png")
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, page)
	}))
	get := func(path, accept string) *http.Response {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if accept != "" {
			req.Header.Set("Accept-Encoding", accept)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Result()
	}

	resp := get("/", "gzip, deflate, br")
	if resp.Header.Get("Content-Encoding") != "gzip" || !strings.Contains(resp.Header.Get("Vary"), "Accept-Encoding") {
		t.Fatalf("headers: %v", resp.Header)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if body, _ := io.ReadAll(zr); string(body) != page {
		t.Error("the page doesn't survive compression")
	}
	for _, c := range []struct{ path, accept string }{{"/", ""}, {"/", "gzip;q=0"}, {"/png", "gzip"}, {"/empty", "gzip"}} {
		if resp := get(c.path, c.accept); resp.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s with %q shouldn't be compressed", c.path, c.accept)
		}
	}
}
