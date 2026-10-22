package web

import (
	"cmp"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/manager"
)

// A server that's gone (SPEC.md §10.10, "Restoring across the lab"): the
// Owner chooses which of its services to restore now, onto which server;
// the rest wait to be restored.

// goneService is one of a gone server's services on its page.
type goneService struct {
	Name, Title string
	Size        int64 // -1 if unknown
	// Label and Place say which backup it comes from; empty when none can
	// be read.
	Label, Place string
	Waiting      bool
}

func (s *Server) serverGonePage(w http.ResponseWriter, r *http.Request) {
	s.renderServerGone(w, r, "", http.StatusOK)
}

func (s *Server) renderServerGone(w http.ResponseWriter, r *http.Request, errMsg string, status int) {
	id := r.PathValue("server")
	data := s.pageData(r)
	plan, err := s.Manager.LostServerPlan(r.Context(), id)
	// While (or just after) its services are restored, the page shows how
	// that's going; the server may have left the lab by then (its name
	// comes in the URL).
	act := s.goneActivity(cmp.Or(plan.Server.Name, r.URL.Query().Get("name")))
	if errors.Is(err, manager.ErrNotFound) && act == nil {
		redirect(w, r, "/servers")
		return
	}
	if msg, ok := userMessage(err); ok && act == nil {
		// Connected after all: nothing to do here.
		errMsg = msg
	} else if err != nil && !errors.Is(err, manager.ErrNotFound) && act == nil {
		s.serverError(w, err)
		return
	}
	var services []goneService
	var total int64
	fits := true
	for _, ls := range plan.Services {
		v := goneService{Name: ls.Name, Title: ls.Title, Size: ls.Size, Waiting: ls.Waiting}
		if ls.Backup != nil {
			v.Label = momentLabel(ls.Backup.Time, time.Now())
			p := ls.Backup.Places[0]
			v.Place = p.Where
			if !p.Main {
				v.Place += " (copy)"
			}
			if ls.Size > 0 {
				total += ls.Size
			}
		}
		services = append(services, v)
	}
	// All ticked when they fit on the first server offered (the
	// controller, if it's connected); otherwise none, for the Owner to choose.
	if len(plan.Targets) > 0 {
		if free := plan.Free[plan.Targets[0].ID]; free >= 0 && total > free*9/10 {
			fits = false
		}
	}
	data["Plan"], data["Services"], data["TickAll"], data["Error"] = plan, services, fits, errMsg
	if act != nil {
		data["RestoreActivity"] = act
	}
	w.WriteHeader(status)
	s.render(w, "settings-server-gone", data)
}

// goneActivity is the latest restore of a gone server's services (run by
// the controller), if it's the named server's.
func (s *Server) goneActivity(name string) *activityView {
	if s.Node == nil || name == "" {
		return nil
	}
	snap, ok := s.Node.LatestActivity(activity.Restore)
	if !ok || snap.Title != goneTitle(name) {
		return nil
	}
	return &activityView{Kind: activity.Restore, Snapshot: snap}
}

// goneTitle is the title of restoring a gone server's services
// (manager.RestoreLostServer), by the server's name.
func goneTitle(name string) string { return "Restoring " + name + "'s services" }

func (s *Server) serverGoneSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("server")
	if r.FormValue("confirm") != "on" {
		s.renderServerGone(w, r, "Tick the box to confirm.", http.StatusBadRequest)
		return
	}
	plan, err := s.Manager.LostServerPlan(r.Context(), id)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	names := r.Form["service"]
	err = s.Manager.RestoreLostServer(r.Context(), id, names, r.FormValue("to"))
	if errors.Is(err, backup.ErrRestoreQueued) {
		s.renderServerGone(w, r, "One of these services is already being restored. Wait until that's finished.", http.StatusConflict)
		return
	}
	if msg, ok := userMessage(err); ok {
		s.renderServerGone(w, r, msg, http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	what := "said " + plan.Server.Name + " is gone"
	if len(names) > 0 {
		what += "; restoring " + strings.Join(names, ", ") + " onto " + r.FormValue("to")
	}
	s.Store.Audit(r.Context(), currentUser(r), "restore", what)
	if len(names) == 0 {
		redirect(w, r, "/backups#restoring")
		return
	}
	redirect(w, r, "/settings/servers/"+id+"/gone?name="+url.QueryEscape(plan.Server.Name))
}

// removeWaitingSubmit removes a service waiting to be restored, with its
// backups.
func (s *Server) removeWaitingSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	err := s.Manager.RemoveWaiting(r.Context(), name)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if msg, ok := userMessage(err); ok {
		s.renderServiceWith(w, r, map[string]any{"Error": msg}, true)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "remove", name+" and its backups (it waited to be restored)")
	redirect(w, r, "/backups#restoring")
}
