package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/labrestore"
	"roostack.cloud/roostack/internal/units"
)

// backupStale is when the dashboard warns that backups aren't happening.
const backupStale = 48 * time.Hour

var templateFuncs = template.FuncMap{
	"when": whenText,
	// retryText says when a failed backup is tried again (" Roostack tries
	// again today 14:00."), or nothing.
	"retryText": retryText,
	"stale":     isStale,
	// shownRepo is a backup's address without its password.
	"shownRepo": labrestore.Shown,
	// date is a day, e.g. 28 September 2027.
	"date": func(t time.Time) string { return t.Local().Format("2 January 2006") },
	// has says whether a list has an item.
	"has": func(list []string, item string) bool { return slices.Contains(list, item) },
	// list makes a slice, e.g. {{range list "light" "dark"}}.
	"list": func(items ...string) []string { return items },
	// percent shows progress, or nothing if it's unknown (-1).
	"size": sizeText,
	"gb":   func(b int64) int64 { return b / units.GB },
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
	"bytes":   func(n uint64) string { return units.Bytes(int64(n)) },
	// roleName shows a role as people say it ("Owner").
	"idleText": idleText,
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

func retryText(r *backup.Retry) string {
	switch {
	case r == nil:
		return ""
	case r.Drive:
		return " It backs up there as soon as the drive is plugged in."
	}
	return " Roostack tries again " + whenText(r.At) + "."
}

// isStale reports whether the last backup is too old.
func isStale(t time.Time) bool { return time.Since(t) > backupStale }

// backupView is the dashboard's backup card.
func (s *Server) backupView(r *http.Request) any {
	v := &backupCard{}
	m := s.serverRefOf(r)
	// Being set up by the controller: say so, not "not set up", until the
	// server restarts with its backups.
	if a := s.Node; a != nil {
		if snap, ok := a.LatestActivity(activity.BackupSetupFor(m.ID)); ok && snap.Running() {
			v.SettingUp = snap.Status
		}
	}
	b := s.backups(r)
	if b == nil {
		// Another server that doesn't answer (e.g. restarting after its
		// backups were set up): its status shows once it's back.
		if !m.This {
			if n := s.serverOf(r); n != nil && n.Settings() == nil {
				v.Unreachable = m.Label()
				return v
			}
		}
		if v.SettingUp != "" {
			return v
		}
		return nil
	}
	st, err := b.Status(r.Context())
	if err != nil {
		if !m.This {
			v.Unreachable = m.Label()
			return v
		}
		s.Log.Error("backup status", "err", err)
		return nil
	}
	v.Status = st
	return v
}

// backupCard is a server's backup status card: its status, or why it
// can't be shown yet.
type backupCard struct {
	backup.Status
	// SettingUp is the step of setting its backups up, while that runs.
	SettingUp string
	// Unreachable names a server that doesn't answer right now.
	Unreachable string
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

// keepThisCopySubmit is the Owner saying this copy of the lab is the right
// one after a restore elsewhere (e.g. a test restore): it backs up again,
// and the other stands down at its next backup (backup/fence.go).
func (s *Server) keepThisCopySubmit(w http.ResponseWriter, r *http.Request) {
	k, ok := s.backups(r).(interface {
		KeepThisCopy(ctx context.Context) error
	})
	if !ok || !s.serverRefOf(r).This {
		http.NotFound(w, r)
		return
	}
	if err := k.KeepThisCopy(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "backup", "kept this copy of the lab after it was restored elsewhere")
	redirect(w, r, "/backups")
}
