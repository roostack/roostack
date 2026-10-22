package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/manager"
)

// A restore that didn't finish (SPEC.md §6.9): a service whose files are
// back from a backup but whose databases didn't load is held stopped
// (instance.Hold) until the Owner restores it again, puts back what was
// there, or starts it anyway. A move through a backup that fails there
// goes back instead, and Needs attention says so.

// Holds are the Owner's choices for a held service, and the moves that
// didn't finish.
type Holds interface {
	StartHeld(ctx context.Context, name string) error
	PutBackHeld(ctx context.Context, name string) error
	FailedMoves(ctx context.Context) map[string]manager.FailedMove
}

// StatusHeld is a service held stopped after a restore that didn't finish.
const StatusHeld = "held"

// heldView is a held service, for its page and Needs attention.
type heldView struct {
	instance.Hold
	// On and From name the server it's held on and, after a move that
	// couldn't go back, the one it came from.
	On, From string
	// TakenText and AtText are when the backup was made and when the
	// restore didn't finish, as the page says them.
	TakenText, AtText string
}

// heldViewOf describes a service's hold, or nil.
func (s *Server) heldViewOf(svc ServiceView) *heldView {
	if svc.Held == nil {
		return nil
	}
	v := &heldView{Hold: *svc.Held, On: svc.Server, TakenText: svc.Held.Taken.Local().Format("Mon 2 Jan 15:04"),
		AtText: svc.Held.At.Local().Format("Mon 2 Jan 15:04")}
	if svc.Held.MovedFrom != "" && s.Manager != nil {
		v.From = svc.Held.MovedFrom
		if n := s.Manager.NodeByID(svc.Held.MovedFrom); n != nil {
			v.From = n.Identity().Name
		}
	}
	return v
}

// RestoreAgain is the restore page with the same backup chosen.
func (v *heldView) RestoreAgain(service string) string {
	u := "/backups/restore/" + service
	if v.Backup != "" {
		u += "?backup=" + url.QueryEscape(v.Backup)
	}
	return u
}

// holdAttention is a problem for each held service, and a warning for
// each move that didn't finish and went back.
func (s *Server) holdAttention(r *http.Request, services []ServiceView, add func(item attentionItem)) {
	for _, svc := range services {
		v := s.heldViewOf(svc)
		if v == nil {
			continue
		}
		item := attentionItem{Level: levelProblem, Link: "/services/" + svc.Name + "#held", LinkText: "What to do",
			Key: "held:" + svc.Name + ":" + v.At.Format(time.RFC3339)}
		if v.MovedFrom != "" {
			item.Text = svc.Title + " is stopped on " + v.On + ": its database didn't load there, and moving it back to " +
				v.From + " failed too."
		} else {
			item.Text = svc.Title + " is stopped: its files are back from the backup of " + v.TakenText +
				", but its database couldn't be loaded."
		}
		add(item)
	}
	moves := s.Manager.FailedMoves(r.Context())
	for _, svc := range services {
		if f, ok := moves[svc.Name]; ok && svc.Held == nil {
			add(attentionItem{Level: levelWarning, Link: "/services/" + svc.Name + "/move", LinkText: "See what went wrong",
				Key:  "move-failed:" + svc.Name + ":" + f.At.Format(time.RFC3339),
				Text: svc.Title + " couldn't be moved to " + f.ToName + ". It's running on " + f.FromName + ", as before."})
		}
	}
}

// heldStartSubmit starts a held service anyway, once the Owner has
// confirmed it.
func (s *Server) heldStartSubmit(w http.ResponseWriter, r *http.Request) {
	s.heldChoice(w, r, s.Manager.StartHeld, "Started %s after its restore didn't finish")
}

// heldPutBackSubmit puts back a held service's data from before its
// restore.
func (s *Server) heldPutBackSubmit(w http.ResponseWriter, r *http.Request) {
	s.heldChoice(w, r, s.Manager.PutBackHeld, "Put back %s's data from before its restore, which didn't finish")
}

// heldChoice carries out one of the Owner's choices for a held service and
// records it in the audit log.
func (s *Server) heldChoice(w http.ResponseWriter, r *http.Request, do func(context.Context, string) error, audit string) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	title := name
	if p, err := s.Manager.ServicePackage(name); err == nil {
		title = p.Title
	}
	err := do(r.Context(), name)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if msg, ok := userMessage(err); ok {
		s.renderServiceWith(w, r, map[string]any{"HeldError": msg}, true)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", fmt.Sprintf(audit, title))
	redirect(w, r, "/services/"+name)
}
