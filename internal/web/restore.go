package web

import (
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/usererr"
)

// Restoring a service (SPEC.md §6.9, §10.10 "Restoring across the lab"):
// one page per service lists its backups from every server and copy, the
// suggested one chosen, and which server it goes onto.

// restoreService finds the installed service named in the URL.
func (s *Server) restoreService(w http.ResponseWriter, r *http.Request) (ServiceView, bool) {
	svc, ok, err := s.serviceByName(r.Context(), r.PathValue("name"))
	if err != nil {
		s.serverError(w, err)
		return ServiceView{}, false
	}
	if !ok {
		http.NotFound(w, r)
	}
	return svc, ok
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
	if x := s.restores(r); x != nil {
		p, err := x.Pending(r.Context())
		if err != nil {
			s.serverError(w, err)
			return
		}
		pending = pending || p
	}
	// Its kept copies on every server (asked of each at once).
	found := make([][]manager.KeptCopy, len(servers))
	var wg sync.WaitGroup
	for i, mc := range servers {
		wg.Go(func() { found[i], _ = s.Manager.KeptCopies(r.Context(), mc.ID) }) // nil from a server that can't be reached
	}
	wg.Wait()
	names := serverNames(servers)
	kept := keptList{Action: "/backups/restore/" + svc.Name + "/kept"}
	for _, copies := range found {
		for _, c := range copies {
			if c.Service == svc.Name {
				kept.Copies = append(kept.Copies, newKeptView(c, svc.Title, names, true))
			}
		}
	}
	slices.SortStableFunc(kept.Copies, func(a, b keptView) int { return b.Time.Compare(a.Time) })
	kept.Total = keptTotal(kept.Copies)
	data := map[string]any{"User": currentUser(r), "Service": svc, "Groups": groups, "Notes": notes,
		"Servers": servers, "Host": host, "Kept": kept, "Pending": pending, "Error": errMsg}
	if len(groups) > 0 {
		// The newest backup, or the one asked for (?backup=: "Restore
		// again…" after a restore that didn't finish, SPEC.md §6.9).
		chosen, place := chosenBackup(groups, r.FormValue("backup"))
		data["Chosen"], data["ChosenPlace"], data["NewestID"] = chosen, place, groups[0].Moments[0].ID
	}
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

// chosenBackup is the moment and place of the backup with this ID, or the
// newest.
func chosenBackup(groups []restoreGroup, id string) (restoreMoment, restorePlace) {
	for _, g := range groups {
		for _, mo := range g.Moments {
			for _, p := range mo.Places {
				if id != "" && p.ID == id {
					return mo, p
				}
			}
		}
	}
	first := groups[0].Moments[0]
	return first, first.Places[0]
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
	if msg, ok := usererr.Message(err); ok {
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
	_, err := s.putBackKept(r, s.keptServer(r, svc))
	if msg, ok := usererr.Message(err); ok {
		s.renderRestore(w, r, svc, msg, http.StatusBadRequest)
		return
	}
	if err != nil {
		s.renderRestore(w, r, svc, "Couldn't put the copy back: "+err.Error(), http.StatusBadRequest)
		return
	}
	redirect(w, r, "/backups/restore/"+svc.Name)
}

func (s *Server) removeKeptSubmit(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.restoreService(w, r)
	if !ok {
		return
	}
	if err := s.removeKept(r, s.keptServer(r, svc)); err != nil {
		s.renderRestore(w, r, svc, "Couldn't delete the copy: "+err.Error(), http.StatusBadRequest)
		return
	}
	redirect(w, r, "/backups/restore/"+svc.Name)
}

// keptServer is the server a kept copy on the restore page is on: the
// form's, or the one the service runs on.
func (s *Server) keptServer(r *http.Request, svc ServiceView) string {
	if server := r.FormValue("server"); server != "" {
		return server
	}
	if n := s.Manager.NodeFor(svc.Name); n != nil {
		return n.Identity().ID
	}
	return ""
}
