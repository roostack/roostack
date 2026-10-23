package web

import (
	"net/http"
)

// Changes to a server that Roostack didn't make (SPEC.md §3.2): shown on its
// Updates page and in "needs attention", until the Owner says they made
// them on purpose.

func (s *Server) hostChangesKnownSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil || n.Updates() == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	if err := n.Updates().AcknowledgeHostChanges(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.hostChecks.Forget(m.ID)
	s.Store.Audit(r.Context(), currentUser(r), "host", "marked host changes on "+m.InSentence()+" as known")
	redirect(w, r, m.Updates())
}
