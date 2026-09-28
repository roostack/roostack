package web

import (
	"context"
	"net/http"
	"strings"
)

// Where each server is, for the 3-2-1 checks (SPEC.md §6.9): at home, or
// somewhere else (a VPS, a relative's house). Roostack can't tell, so the
// Owner says, once per server. Kept on the controller.

const locationKey = "server-location."

const (
	locationHome = "home"
	locationAway = "away"
)

// serverLocation is where a server is: locationHome, locationAway, or ""
// if nobody has said.
func (s *Server) serverLocation(ctx context.Context, id string) string {
	v, _ := s.Store.Setting(ctx, locationKey+id)
	if v != locationHome && v != locationAway {
		return ""
	}
	return v
}

// locationAsk is a server whose location the plan needs.
type locationAsk struct{ ID, Name string }

// backupPlace says whether a backup on server host is at home or away,
// seen from server m: at home if both are at home; away if they're in
// different places or both away from home. It lists the servers whose
// location is needed and not known.
func (s *Server) backupPlace(ctx context.Context, m serverRef, host string) (atHome, away bool, ask []locationAsk) {
	names := map[string]string{}
	if servers, err := s.Manager.Servers(); err == nil {
		for _, sv := range servers {
			names[sv.ID] = sv.Name
		}
	}
	here, there := s.serverLocation(ctx, m.ID), s.serverLocation(ctx, host)
	if here == "" {
		ask = append(ask, locationAsk{m.ID, names[m.ID]})
	}
	if there == "" {
		ask = append(ask, locationAsk{host, names[host]})
	}
	if ask != nil {
		return false, false, ask
	}
	if here == locationHome && there == locationHome {
		return true, false, nil
	}
	return false, true, nil
}

// locationSubmit records where a server is.
func (s *Server) locationSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil || s.serverOf(r) == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	loc := r.FormValue("location")
	if loc != locationHome && loc != locationAway {
		http.Error(w, "Choose at home or somewhere else.", http.StatusBadRequest)
		return
	}
	if err := s.Store.SetSetting(r.Context(), locationKey+m.ID, loc); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "servers", m.Label()+" is "+map[string]string{locationHome: "at home", locationAway: "away from home"}[loc])
	back := r.FormValue("back")
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		back = "/settings/servers#server-" + m.ID
	}
	redirect(w, r, back)
}
