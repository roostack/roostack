package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/mylab/mylab/internal/backup"
)

// backupStale is when the dashboard warns that backups aren't happening.
const backupStale = 48 * time.Hour

var templateFuncs = template.FuncMap{
	"when":  whenText,
	"stale": isStale,
	// date is a day, e.g. 28 September 2027.
	"date": func(t time.Time) string { return t.Local().Format("2 January 2006") },
	// list makes a slice, e.g. {{range list "light" "dark"}}.
	"list": func(items ...string) []string { return items },
	// percent shows progress, or nothing if it's unknown (-1).
	"size": sizeText,
	"gb":   func(b int64) int64 { return b >> 30 },
	// image is a container image without its digest (shown in full on
	// hover): docker.io/vaultwarden/server:1.34.3.
	"image": func(ref string) string { name, _, _ := strings.Cut(ref, "@"); return name },
	"percent": func(p int) string {
		if p < 0 {
			return ""
		}
		return fmt.Sprintf("%d%%", p)
	},
	// palette is the built-in theme (internal/web/static/css/themes).
	"palette": func() string { return DefaultPalette },
	"themes":  func() []Theme { return Themes },
	"bytes":   backup.Bytes,
	// roleName shows a role as people say it ("Owner").
	"roleName": func(role string) string {
		if role == "" {
			return ""
		}
		return strings.ToUpper(role[:1]) + role[1:]
	},
}

// whenText formats a time for people, e.g. "today 02:00" or "Mon 3 Oct 02:00".
func whenText(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	// Times from another server arrive in its time zone; show them in
	// this one's, like everything else (SPEC.md §3.2).
	t = t.Local()
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
	if s.backups(r) == nil {
		return nil
	}
	st, err := s.backups(r).Status(r.Context())
	if err != nil {
		s.Log.Error("backup status", "err", err)
		return nil
	}
	return st
}

func (s *Server) backupRun(w http.ResponseWriter, r *http.Request) {
	if s.backups(r) == nil {
		http.NotFound(w, r)
		return
	}
	err := s.backups(r).RunNow()
	if err != nil && !errors.Is(err, backup.ErrAlreadyQueued) {
		s.serverError(w, fmt.Errorf("starting backup: %w", err))
		return
	}
	m := s.serverRefOf(r)
	s.Store.Audit(r.Context(), currentUser(r), "backup", "started manually on "+m.Label())
	redirect(w, r, m.Backups())
}
