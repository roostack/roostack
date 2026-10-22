package web

import (
	"context"
	"fmt"
	"net/http"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/words"
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
func (s *Server) junkAttention(ctx context.Context, l *attentionList) {
	for _, rep := range s.Manager.JunkReports(ctx) {
		files, they, them := fmt.Sprintf("%d files", len(rep.Files)), "they", "them"
		if len(rep.Files) == 1 {
			files, they, them = "a file", "it", "it"
		}
		backups, hold := rep.Server+"'s backups", "hold"
		if rep.Copy {
			backups, hold = "the copy of "+rep.Server+"'s backups", "holds"
		}
		if rep.Removed() {
			l.add(attentionItem{Key: "junk-removed:" + rep.ServerID + ":" + rep.Provider, Level: levelWarning,
				Text: "Roostack removed " + files + " it can't read from " + backups + " on " + rep.Where +
					": " + they + " stopped those backups working. Only " + orList(rep.Writers) + " could have put " + them + " there.",
				Link: "/backups/junk", LinkText: "Details"})
			continue
		}
		l.add(attentionItem{Key: "junk:" + rep.ServerID + ":" + rep.Provider, Level: levelProblem,
			Text: words.Capitalise(backups) + " on " + rep.Where + " " + hold + " " + files + " Roostack can't read, which " +
				words.Plural(len(rep.Files), "stops", "stop") + " those backups working, and Roostack couldn't remove " + them + ".",
			Link: "/backups/junk", LinkText: "What to do"})
	}
}

// orList joins names as "a, b or c", or says "nobody" if there are none.
func orList(names []string) string {
	if len(names) == 0 {
		return "nobody"
	}
	return words.Or(names)
}

func (s *Server) junkPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "backups-junk", map[string]any{"User": currentUser(r), "Reports": s.Manager.JunkReports(r.Context()),
		"OrList": orList})
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
