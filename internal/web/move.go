package web

import (
	"errors"
	"fmt"
	"net/http"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/manager"
)

// Moving a service to another server (SPEC.md §10.10).

func (s *Server) movePage(w http.ResponseWriter, r *http.Request) {
	s.renderMove(w, r, "")
}

func (s *Server) renderMove(w http.ResponseWriter, r *http.Request, errMsg string) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	p, err := s.Manager.Package(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := s.Manager.MoveInfo(r.Context(), name)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	servers, _ := s.Manager.Servers()
	var targets []manager.Server
	for _, mc := range servers {
		if mc.ID != info.From.ID {
			targets = append(targets, mc)
		}
	}
	data["Package"], data["Info"], data["Targets"], data["Error"] = p, info, targets, errMsg
	data["Size"] = sizeText(info.Size)
	if a := s.activityFor(r, activity.Move); a != nil {
		data["MoveActivity"] = a
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "move", data)
}

func (s *Server) moveSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	name, to := r.PathValue("name"), r.FormValue("to")
	err := s.Manager.MoveService(r.Context(), name, to, r.FormValue("way"))
	if msg, ok := userMessage(err); ok {
		s.renderMove(w, r, msg)
		return
	}
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "move", name+" to "+to)
	redirect(w, r, "/services/"+name+"/move")
}

// sizeText is a size for people.
func sizeText(b int64) string {
	switch {
	case b < 0:
		return "unknown"
	case b < 1<<20:
		return fmt.Sprintf("%d kB", (b+1023)>>10)
	case b < 1<<30:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	}
	return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
}
