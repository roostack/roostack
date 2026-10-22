package web

import "net/http"

// The lab's name (Settings → Lab name): shown on every page and to other
// labs sharing storage.

func (s *Server) labNamePage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "settings-lab", withArea(data, "lab"))
}

func (s *Server) labNameSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	err := s.Setup.SetLabName(r.Context(), name, true)
	if msg, ok := userMessage(err); ok {
		data, derr := s.pageData(r)
		if derr != nil {
			s.serverError(w, derr)
			return
		}
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
