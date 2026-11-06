package web

import (
	"context"
	"net/http"

	"roostack.cloud/roostack/internal/usererr"
)

// Settings → Name and look (SPEC.md §5.2, "The lab's name and look";
// docs/mockups/lab-notes): the lab's name, its logo, and its colours,
// used by the sign-in page, the status page, and Roostack's own pages.
// Until 2026-11-06 the name had a page of its own (Lab name) and the
// logo and colours were a card on People, for the sign-in page only.

// lookRoutes are the page, its two forms, and the logo's files.
func (s *Server) lookRoutes(mux *http.ServeMux) {
	mux.Handle("GET /settings/look", s.requireOwner(s.lookPage))
	mux.Handle("POST /settings/look", s.requireOwner(s.lookSubmit))
	mux.Handle("POST /settings/lab", s.requireOwner(s.labNameSubmit))
	mux.Handle("GET /settings/lab", s.requireOwner(http.RedirectHandler("/settings/look", http.StatusMovedPermanently).ServeHTTP))
	// The logo isn't private: the sign-in page shows it to anyone, and
	// Roostack's pages use it before login too (the browser tab's icon).
	mux.HandleFunc("GET /brand/icon", s.lookFavicon)
	mux.HandleFunc("GET /brand/{part}", s.lookFile)
}

func (s *Server) lookPage(w http.ResponseWriter, r *http.Request) {
	s.renderLook(w, r, http.StatusOK, nil)
}

// renderLook shows the page with extra (an error or a notice).
func (s *Server) renderLook(w http.ResponseWriter, r *http.Request, status int, extra map[string]any) {
	data := s.pageData(r)
	data["Look"] = s.signInLook(r.Context())
	data["Themes"] = Themes
	data["LookSaved"] = r.URL.Query().Get("look") == "saved"
	if h := s.signInAddress(); h != "" {
		data["SignInHost"] = h
	}
	data["StatusHost"] = s.statusHost()
	for k, v := range extra {
		data[k] = v
	}
	w.WriteHeader(status)
	s.render(w, "settings-look", withArea(data, "look"))
}

func (s *Server) labNameSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	err := s.Setup.SetLabName(r.Context(), name, true)
	if msg, ok := usererr.Message(err); ok {
		s.renderLook(w, r, http.StatusBadRequest, map[string]any{"NameError": msg, "LabName": name})
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "lab", "renamed the lab")
	redirect(w, r, "/restarting")
}

// lookSubmit saves the logo and colours.
func (s *Server) lookSubmit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4*maxLogo+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		s.renderLook(w, r, http.StatusBadRequest, map[string]any{"LookError": "Those files are too big: each logo can be at most 200 KB."})
		return
	}
	if err := s.saveLook(r); err != nil {
		if msg, ok := usererr.Message(err); ok {
			s.renderLook(w, r, http.StatusBadRequest, map[string]any{"LookError": msg})
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "settings", "the lab's logo and colours")
	// The status page shows them too: write it again now rather than at
	// the next round of checks.
	go s.WriteStatusPage(context.Background())
	redirect(w, r, "/settings/look?look=saved#logo")
}
