package web

import (
	"errors"
	"net/http"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
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

// restoreGroup is a period on the restore page ("This week"), with its
// backup moments.
type restoreGroup struct {
	Title   string
	Moments []restoreMoment
}

// restoreMoment is one backup moment: its label, and the places to read
// it from (the first is read unless another is chosen).
type restoreMoment struct {
	ID, Label string
	Places    []restorePlace
}

type restorePlace struct{ ID, Title, Short string }

// restoreGroups sorts moments (newest first) into periods, as the
// clean-up thins them out: daily for a week, then weekly, then monthly.
// Each label names the server only if the backups come from more than one.
func restoreGroups(moments []manager.BackupMoment, now time.Time) []restoreGroup {
	servers := map[string]bool{}
	for _, mo := range moments {
		servers[mo.ServerID] = true
	}
	var groups []restoreGroup
	for _, mo := range moments {
		title := "Older"
		switch age := now.Sub(mo.Time); {
		case age < 7*24*time.Hour:
			title = "This week"
		case age < 31*24*time.Hour:
			title = "This month"
		}
		if len(groups) == 0 || groups[len(groups)-1].Title != title {
			groups = append(groups, restoreGroup{Title: title})
		}
		label := momentLabel(mo.Time, now)
		if len(servers) > 1 {
			label += ", from " + mo.ServerName
		}
		v := restoreMoment{ID: mo.ID(), Label: label}
		for _, p := range mo.Places {
			title := p.Where + " (copy)"
			if p.Main {
				title = p.Where + " (main backup)"
			}
			v.Places = append(v.Places, restorePlace{ID: p.ID(), Title: title, Short: p.Snapshot.Short})
		}
		g := &groups[len(groups)-1]
		g.Moments = append(g.Moments, v)
	}
	return groups
}

// keptView is data a restore (or a move) kept aside, on the restore page.
type keptView struct {
	Path string
	// Server and ServerName: where it is, if a restore from elsewhere or
	// a move kept it (else on the server the service runs on).
	Server, ServerName string
	Failed             bool // a rolled-back partial restore
	Time               time.Time
	// What it is, for people: "Before bringing data in", say (SPEC.md §8.4).
	What string
	// CanPutBack: it can go back in place of the data (it's on the
	// service's server, or kept by its restores).
	CanPutBack bool
}

func (s *Server) renderRestore(w http.ResponseWriter, r *http.Request, svc ServiceView, errMsg string, status int) {
	backups, notes, err := s.Manager.ServiceBackups(r.Context(), svc.Name)
	if err != nil {
		s.serverError(w, err)
		return
	}
	groups := restoreGroups(backups, time.Now())
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
			kept = append(kept, keptView{Path: k.Path, Failed: k.Failed, Time: k.Time, CanPutBack: !k.Failed,
				What: map[bool]string{true: "From a restore that didn't finish", false: "Kept aside"}[k.Failed]})
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
				v := keptView{Path: f.Path, Server: m.ID, ServerName: m.Name, Time: f.Time, What: "Kept aside",
					CanPutBack: !f.MovedAway && m.ID == host}
				switch {
				case f.Imported:
					v.What = "Before bringing data in"
				case f.FailedImport:
					v.What = "Brought in, then set aside"
				case f.MovedAway:
					v.What = "The old copy, after moving"
				}
				kept = append(kept, v)
			}
		}
	}
	data := map[string]any{"User": currentUser(r), "Service": svc, "Groups": groups, "Notes": notes,
		"Servers": servers, "Host": host, "Kept": kept, "Pending": pending, "Error": errMsg}
	if a := s.restoreActivity(r, svc); a != nil {
		data["RestoreActivity"] = a
		data["Pending"] = pending || a.Running()
		// Just restored: the page shows that it's done, with restoring
		// again a click away rather than the form (not after a failure:
		// then trying again is the next step).
		data["JustDone"] = !a.Running() && a.Err == "" && time.Since(a.Finished) < justRestored
	}
	w.WriteHeader(status)
	s.render(w, "restore", data)
}

// justRestored is how long after a restore its page leads with the result.
const justRestored = 15 * time.Minute

// restoreActivity is the latest restore of the service: the server it runs
// on restores from its own backup, the controller from anywhere else.
func (s *Server) restoreActivity(r *http.Request, svc ServiceView) *activityView {
	return s.serviceActivity(r, activity.Restore, func(title string) bool { return title == "Restoring "+svc.Title })
}

// serviceActivity is a service's latest activity of a kind whose title
// matches, from the server it runs on or the controller (which runs
// moves, and restores from elsewhere), whichever is newer.
func (s *Server) serviceActivity(r *http.Request, kind string, match func(title string) bool) *activityView {
	var best *activityView
	for _, n := range []ServiceNode{s.serverOf(r), s.Node} {
		if n == nil {
			continue
		}
		snap, ok := n.LatestActivity(kind)
		if !ok || !match(snap.Title) || (best != nil && !snap.Started.After(best.Started)) {
			continue
		}
		v := &activityView{Kind: kind, Snapshot: snap}
		if s.Node != nil && n.Identity().ID != s.Node.Identity().ID {
			v.Server = n.Identity().ID // polling asks that server
		}
		best = v
	}
	if best == nil || !auth.Can(currentUser(r).Role, activityPerm[kind]) {
		return nil
	}
	return best
}

// momentLabel names when a backup was made, e.g. "Last night's backup,
// 02:00".
func momentLabel(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	switch {
	case !t.Before(today.Add(-6*time.Hour)) && t.Before(today.Add(6*time.Hour)):
		return "Last night's backup, " + t.Format("15:04")
	case !t.Before(today):
		return "Today's backup, " + t.Format("15:04")
	case !t.Before(today.AddDate(0, 0, -1)):
		return "Yesterday's backup, " + t.Format("15:04")
	}
	return "The backup of " + t.Format("Mon 2 Jan, 15:04")
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

// putBackKeptSubmit puts a kept copy back in place of a service's data
// (SPEC.md §8.4): what's there now is kept in turn.
func (s *Server) putBackKeptSubmit(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.restoreService(w, r)
	if !ok {
		return
	}
	path, server := r.FormValue("path"), r.FormValue("server")
	var err error
	switch x := s.restores(r); {
	case server != "":
		err = s.Manager.PutBackKeptData(r.Context(), server, path)
	case x != nil:
		err = x.PutBack(r.Context(), svc.Name, path)
	default:
		err = node.ErrNotFound
	}
	if msg, ok := userMessage(err); ok {
		s.renderRestore(w, r, svc, msg, http.StatusBadRequest)
		return
	}
	if err != nil {
		s.renderRestore(w, r, svc, "Couldn't put the copy back: "+err.Error(), http.StatusBadRequest)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "restore", "put back kept copy "+path)
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
