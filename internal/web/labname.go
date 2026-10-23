package web

import (
	"net/http"

	"roostack.cloud/roostack/internal/usererr"
)

// The lab's name (Settings → Lab name): shown on every page and to other
// labs sharing storage.

func (s *Server) labNamePage(w http.ResponseWriter, r *http.Request) {
	data := s.pageData(r)
	s.render(w, "settings-lab", withArea(data, "lab"))
}

func (s *Server) labNameSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	err := s.Setup.SetLabName(r.Context(), name, true)
	if msg, ok := usererr.Message(err); ok {
		data := s.pageData(r)
		data["Error"], data["LabName"] = msg, name
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "settings-lab", withArea(data, "lab"))
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "lab", "renamed the lab")
	redirect(w, r, "/restarting")
}
