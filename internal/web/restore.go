package web

import (
	"errors"
	"net/http"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/backup"
)

// restoreService finds the installed service named in the URL.
func (s *Server) restoreService(w http.ResponseWriter, r *http.Request) (ServiceView, bool) {
	if s.restores(r) == nil {
		http.NotFound(w, r)
		return ServiceView{}, false
	}
	services, err := s.Services(r.Context())
	if err != nil {
		s.serverError(w, err)
		return ServiceView{}, false
	}
	for _, svc := range services {
		if svc.Name == r.PathValue("name") {
			return svc, true
		}
	}
	http.NotFound(w, r)
	return ServiceView{}, false
}

func (s *Server) restorePage(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.restoreService(w, r)
	if !ok {
		return
	}
	s.renderRestore(w, r, svc, "", http.StatusOK)
}

func (s *Server) renderRestore(w http.ResponseWriter, r *http.Request, svc ServiceView, errMsg string, status int) {
	snaps, err := s.restores(r).Snapshots(r.Context(), svc.Name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	kept, err := s.restores(r).Kept(svc.Name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data := map[string]any{"User": currentUser(r), "Service": svc, "Snapshots": snaps, "Kept": kept,
		"Pending": s.restores(r).Pending(), "Error": errMsg}
	// The latest restore's log, if it's this service's.
	if a := s.activityFor(r, activity.Restore); a != nil && a.Title == "Restoring "+svc.Title {
		data["RestoreActivity"] = a
		data["Pending"] = data["Pending"].(bool) || a.Running()
	}
	w.WriteHeader(status)
	s.render(w, "restore", data)
}

func (s *Server) restoreSubmit(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.restoreService(w, r)
	if !ok {
		return
	}
	if r.FormValue("confirm") != "on" {
		s.renderRestore(w, r, svc, "Tick the box to confirm.", http.StatusBadRequest)
		return
	}
	snapshot := r.FormValue("snapshot")
	if snapshot == "" {
		s.renderRestore(w, r, svc, "Choose a backup to restore.", http.StatusBadRequest)
		return
	}
	err := s.restores(r).Restore(svc.Name, snapshot)
	if errors.Is(err, backup.ErrRestoreQueued) {
		s.renderRestore(w, r, svc, "Another restore is already running. Wait until it has finished.", http.StatusConflict)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "restore", svc.Name+" from backup "+snapshot)
	redirect(w, r, "/backups/restore/"+svc.Name)
}

func (s *Server) removeKeptSubmit(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.restoreService(w, r)
	if !ok {
		return
	}
	path := r.FormValue("path")
	if err := s.restores(r).RemoveKept(r.Context(), svc.Name, path); err != nil {
		s.renderRestore(w, r, svc, "Couldn't delete the copy: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "restore", "deleted kept copy "+path)
	redirect(w, r, "/backups/restore/"+svc.Name)
}
