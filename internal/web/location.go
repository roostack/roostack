package web

import (
	"context"
	"net/http"
	"strings"

	"roostack.cloud/roostack/internal/manager"
)

// Where each server is, for the 3-2-1 checks (SPEC.md §6.9): the Owner
// says, once per server, and the manager keeps it (manager.ServerLocation).

// locationAsk is a server whose location the plan needs.
type locationAsk struct{ ID, Name string }

// backupPlace says whether a backup on server host is at home or away,
// seen from server m (manager.ServerPlace), naming the servers whose
// location is needed and not known.
func (s *Server) backupPlace(ctx context.Context, m serverRef, host string) (atHome, away bool, ask []locationAsk) {
	atHome, away, unknown := s.Manager.ServerPlace(ctx, m.ID, host)
	if unknown == nil {
		return atHome, away, nil
	}
	names := map[string]string{}
	if servers, err := s.Manager.Servers(); err == nil {
		for _, sv := range servers {
			names[sv.ID] = sv.Name
		}
	}
	for _, id := range unknown {
		ask = append(ask, locationAsk{id, names[id]})
	}
	return false, false, ask
}

// locationSubmit records where a server is.
func (s *Server) locationSubmit(w http.ResponseWriter, r *http.Request) {
	if s.serverOf(r) == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	loc := r.FormValue("location")
	if loc != manager.LocationHome && loc != manager.LocationAway {
		http.Error(w, "Choose at home or somewhere else.", http.StatusBadRequest)
		return
	}
	if err := s.Manager.SetServerLocation(r.Context(), m.ID, loc); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "servers", m.Label()+" is "+map[string]string{manager.LocationHome: "at home", manager.LocationAway: "away from home"}[loc])
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/servers#server-" + m.ID
	}
	redirect(w, r, back)
}
