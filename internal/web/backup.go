package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/mylab/mylab/internal/backup"
)

// backupStale is when the dashboard warns that backups aren't happening.
const backupStale = 48 * time.Hour

var templateFuncs = template.FuncMap{
	"when":  whenText,
	"stale": isStale,
}

// whenText formats a time for people, e.g. "today 02:00" or "Mon 3 Oct 02:00".
func whenText(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	now := time.Now()
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "today " + t.Format("15:04")
	case y1 == y2 && m1 == m2 && d2 == d1-1:
		return "yesterday " + t.Format("15:04")
	case y1 == y2 && m1 == m2 && d2 == d1+1:
		return "tomorrow " + t.Format("15:04")
	}
	return t.Format("Mon 2 Jan 15:04")
}

// isStale reports whether the last backup is too old.
func isStale(t time.Time) bool { return time.Since(t) > backupStale }

// backupView is the dashboard's backup card.
func (s *Server) backupView(r *http.Request) any {
	if s.Backups == nil {
		return nil
	}
	st, err := s.Backups.Status(r.Context())
	if err != nil {
		s.Log.Error("backup status", "err", err)
		return nil
	}
	return st
}

func (s *Server) backupRun(w http.ResponseWriter, r *http.Request) {
	if s.Backups == nil {
		http.NotFound(w, r)
		return
	}
	err := s.Backups.RunNow()
	if err != nil && !errors.Is(err, backup.ErrAlreadyQueued) {
		s.serverError(w, fmt.Errorf("starting backup: %w", err))
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "backup", "started manually")
	redirect(w, r, "/")
}
