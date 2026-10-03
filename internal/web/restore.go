package web

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
)

// Restoring a service (SPEC.md §6.9, §10.10 "Restoring across the lab"):
// one page per service lists its backups from every server and copy, the
// suggested one chosen, and which server it goes onto.

// restoreService finds the installed service named in the URL.
func (s *Server) restoreService(w http.ResponseWriter, r *http.Request) (ServiceView, bool) {
	if s.Manager == nil {
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

// restoreChoice is one backup on the restore page.
type restoreChoice struct {
	ID, Label, Short string
}

// keptView is data a restore (or a move) kept aside, on the restore page.
type keptView struct {
	Path string
	// Server and ServerName: where it is, if a restore from elsewhere or
	// a move kept it (else on the server the service runs on).
	Server, ServerName string
	Failed             bool // a rolled-back partial restore
	Time               time.Time
}

func (s *Server) renderRestore(w http.ResponseWriter, r *http.Request, svc ServiceView, errMsg string, status int) {
	backups, notes, err := s.Manager.ServiceBackups(r.Context(), svc.Name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	var choices []restoreChoice
	for _, b := range backups {
		choices = append(choices, restoreChoice{ID: b.ID(), Label: backupLabel(b, time.Now()), Short: b.Snapshot.Short})
	}
	host := ""
	if n := s.Manager.NodeFor(svc.Name); n != nil {
		host = n.Identity().ID
	}
	servers, err := s.Manager.Servers()
	if err != nil {
		s.serverError(w, err)
		return
	}
	pending := s.Manager.Restoring(svc.Name)
	var kept []keptView
	if x := s.restores(r); x != nil {
		pending = pending || x.Pending()
		copies, err := x.Kept(svc.Name)
		if err != nil {
			s.serverError(w, err)
			return
		}
		for _, k := range copies {
			kept = append(kept, keptView{Path: k.Path, Failed: k.Failed, Time: k.Time})
		}
	}
	// Data a restore from elsewhere (or a move) set aside, on any server.
	for _, m := range servers {
		folders, err := s.Manager.KeptData(r.Context(), m.ID)
		if err != nil {
			continue // a server that can't be reached
		}
		for _, f := range folders {
			if f.Service == svc.Name {
				kept = append(kept, keptView{Path: f.Path, Server: m.ID, ServerName: m.Name, Time: f.Time})
			}
		}
	}
	data := map[string]any{"User": currentUser(r), "Service": svc, "Choices": choices, "Notes": notes,
		"Servers": servers, "Host": host, "Kept": kept, "Pending": pending, "Error": errMsg}
	if a := s.restoreActivity(r, svc); a != nil {
		data["RestoreActivity"] = a
		data["Pending"] = pending || a.Running()
	}
	w.WriteHeader(status)
	s.render(w, "restore", data)
}

// restoreActivity is the latest restore of the service: the server it runs
// on restores from its own backup, the controller from anywhere else.
func (s *Server) restoreActivity(r *http.Request, svc ServiceView) *activityView {
	var best *activityView
	for _, n := range []ServiceNode{s.serverOf(r), s.Node} {
		if n == nil {
			continue
		}
		snap, ok := n.LatestActivity(activity.Restore)
		if !ok || snap.Title != "Restoring "+svc.Title || (best != nil && !snap.Started.After(best.Started)) {
			continue
		}
		v := &activityView{Kind: activity.Restore, Snapshot: snap}
		if s.Node != nil && n.Identity().ID != s.Node.Identity().ID {
			v.Server = n.Identity().ID // polling asks that server
		}
		best = v
	}
	if best == nil || s.activityFor(r, activity.Restore) == nil { // may the user see it?
		return nil
	}
	return best
}

// backupLabel describes a backup in one line, e.g. "Last night's backup,
// 02:00, from nas".
func backupLabel(b manager.ServiceBackup, now time.Time) string {
	t := b.Snapshot.Time.Local()
	now = now.Local()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	var when string
	switch {
	case !t.Before(today.Add(-6*time.Hour)) && t.Before(today.Add(6*time.Hour)):
		when = "Last night's backup, " + t.Format("15:04")
	case !t.Before(today):
		when = "Today's backup, " + t.Format("15:04")
	case !t.Before(today.AddDate(0, 0, -1)):
		when = "Yesterday's backup, " + t.Format("15:04")
	default:
		when = "The backup of " + t.Format("Mon 2 Jan, 15:04")
	}
	if b.Main {
		return fmt.Sprintf("%s, from %s", when, b.ServerName)
	}
	return fmt.Sprintf("%s, from %s's copy at %s", when, b.ServerName, b.Where)
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
	id, to := r.FormValue("backup"), r.FormValue("to")
	if id == "" {
		s.renderRestore(w, r, svc, "Choose a backup to restore.", http.StatusBadRequest)
		return
	}
	err := s.Manager.RestoreService(r.Context(), svc.Name, id, to)
	if errors.Is(err, backup.ErrRestoreQueued) {
		s.renderRestore(w, r, svc, "Another restore is already running. Wait until it has finished.", http.StatusConflict)
		return
	}
	if msg, ok := userMessage(err); ok {
		s.renderRestore(w, r, svc, msg, http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	what := svc.Name + " from backup " + id
	if to != "" {
		what += " onto " + to
	}
	s.Store.Audit(r.Context(), currentUser(r), "restore", what)
	redirect(w, r, "/backups/restore/"+svc.Name)
}

func (s *Server) removeKeptSubmit(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.restoreService(w, r)
	if !ok {
		return
	}
	path, server := r.FormValue("path"), r.FormValue("server")
	var err error
	switch x := s.restores(r); {
	case server != "":
		// Kept by a restore from elsewhere or a move: only folders the
		// server lists as kept are accepted (node.Local.RemoveKeptData).
		err = s.Manager.RemoveKeptData(r.Context(), server, path)
	case x != nil:
		err = x.RemoveKept(r.Context(), svc.Name, path)
	default:
		err = node.ErrNotFound
	}
	if err != nil {
		s.renderRestore(w, r, svc, "Couldn't delete the copy: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "restore", "deleted kept copy "+path)
	redirect(w, r, "/backups/restore/"+svc.Name)
}
