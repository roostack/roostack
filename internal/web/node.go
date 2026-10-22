package web

import (
	"html/template"
	"io/fs"
	"net/http"

	"roostack.cloud/roostack/internal/backup"
)

// NodePage is the web page of a server that's part of a lab (a node): it
// has no UI of its own, so its address says which lab it belongs to and
// where to manage it (SPEC.md §10.2).
func NodePage(name, labName, ui string, fenced func() *backup.Fence) (http.Handler, error) {
	t, err := template.New("layout.html").Funcs(templateFuncs()).ParseFS(templateFS,
		"templates/layout.html", "templates/components.html", "templates/parts.html", "templates/node-home.html")
	if err != nil {
		return nil, err
	}
	static, _ := fs.Sub(staticFS, "static")
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(static)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		data := map[string]any{"Name": name, "Lab": labName, "UI": ui}
		if fenced != nil {
			data["Fenced"] = fenced()
		}
		t.Execute(w, data)
	})
	mux.Handle("/", http.RedirectHandler("/", http.StatusSeeOther))
	return securityHeaders(mux), nil
}
