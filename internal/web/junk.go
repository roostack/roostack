package web

import (
	"context"
	"net/http"

	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/manager"
)

// Files the controller found in backups and couldn't read (SPEC.md
// §10.10, "Junk in a repository"; internal/manager quarantine.go): it
// removes them by itself, and says so under Needs attention, with this
// page for the detail, until the Owner has seen it.

// BackupJunk is what the controller found, in internal/manager.
type BackupJunk interface {
	JunkReports(ctx context.Context) []manager.JunkReport
	DismissJunk(ctx context.Context) error
}

func (s *Server) junkPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "backups-junk", map[string]any{"User": currentUser(r), "Reports": s.Manager.JunkReports(r.Context()),
		"OrList": attention.OrList})
}

// junkDismissSubmit clears the list: the Owner has seen it.
func (s *Server) junkDismissSubmit(w http.ResponseWriter, r *http.Request) {
	if err := s.Manager.DismissJunk(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "backups", "Dismissed the files Roostack removed from backups")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
