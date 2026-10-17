package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"

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

// junkAttention adds one item per backup with junk found.
func (s *Server) junkAttention(ctx context.Context, add func(level, text, link, linkText string)) {
	for _, rep := range s.Manager.JunkReports(ctx) {
		files := fmt.Sprintf("%d files", len(rep.Files))
		if len(rep.Files) == 1 {
			files = "a file"
		}
		backups := rep.Server + "'s backups"
		if rep.Copy {
			backups = "the copy of " + rep.Server + "'s backups"
		}
		if rep.Removed() {
			add(levelWarning, "Roostack removed "+files+" it can't read from "+backups+" on "+rep.Where+
				": they stopped those backups working. Only "+orList(rep.Writers)+" could have put them there.",
				"/backups/junk", "Details")
			continue
		}
		add(levelProblem, backups+" on "+rep.Where+" hold "+files+" Roostack can't read, which stop them working, "+
			"and Roostack couldn't remove them.", "/backups/junk", "What to do")
	}
}

// orList joins names as "a, b or c".
func orList(names []string) string {
	switch len(names) {
	case 0:
		return "nobody"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

func (s *Server) junkPage(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "backups-junk", map[string]any{"User": currentUser(r), "Reports": s.Manager.JunkReports(r.Context()),
		"OrList": orList})
}

// junkDismissSubmit clears the list: the Owner has seen it.
func (s *Server) junkDismissSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Manager.DismissJunk(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "backups", "Dismissed the files Roostack removed from backups")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
