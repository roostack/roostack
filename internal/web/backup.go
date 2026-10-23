package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/backup"
)

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
	err := s.backups(r).RunNow(r.Context())
	if err != nil && !errors.Is(err, backup.ErrAlreadyQueued) {
		s.serverError(w, fmt.Errorf("starting backup: %w", err))
		return
	}
	m := s.serverRefOf(r)
	s.Store.Audit(r.Context(), currentUser(r), "backup", "started manually on "+m.InSentence())
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
