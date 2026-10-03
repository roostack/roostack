package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

// staticHandler serves Roostack's own CSS, scripts and images. Embedded
// files have no modification time, so without help a browser could keep
// using an old app.js after an update. Each file gets an ETag (a hash of
// its content) and "no-cache": the browser asks each time, and gets a
// short 304 Not Modified while the file hasn't changed.
func staticHandler(static fs.FS) http.Handler {
	etags := map[string]string{}
	fs.WalkDir(static, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, err := fs.ReadFile(static, path); err == nil {
			sum := sha256.Sum256(b)
			etags[path] = `"` + hex.EncodeToString(sum[:8]) + `"`
		}
		return nil
	})
	files := http.FileServerFS(static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tag, ok := etags[strings.TrimPrefix(r.URL.Path, "/")]; ok {
			w.Header().Set("ETag", tag)
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
