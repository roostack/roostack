package web

import (
	"context"
	"net/http"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/usererr"
)

// Changing the recovery key (SPEC.md §6.9, internal/manager keychange.go):
// after the same check as showing it (password and a fresh code), a new
// key is made and shown; once the Owner confirms saving it, Roostack adds
// it to every backup and removes the old one, showing progress on the
// Backups page.

// RecoveryKeys changes the recovery key, in internal/manager.
type RecoveryKeys interface {
	KeyChangeStatus() (*manager.KeyChange, error)
	StartKeyChange(ctx context.Context) error
	NewRecoveryKey() (string, error)
	ConfirmKeyChange(ctx context.Context) error
	CancelKeyChange() error
}

func (s *Server) keyChangePage(w http.ResponseWriter, r *http.Request) {
	s.renderKeyChange(w, r, http.StatusOK, "")
}

// renderKeyChange shows the change page (without the new key: it's only
// shown right after the check), with an error.
func (s *Server) renderKeyChange(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if !s.hasRecoveryKey() {
		http.NotFound(w, r)
		return
	}
	k, err := s.Manager.KeyChangeStatus()
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	s.render(w, "backups-key-change", map[string]any{"User": currentUser(r), "Change": k, "Error": msg})
}

// keyChangeSubmit checks it's the Owner, makes the new key if there isn't
// one waiting, and shows it.
func (s *Server) keyChangeSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.hasRecoveryKey() {
		http.NotFound(w, r)
		return
	}
	u := currentUser(r)
	again := func(status int, msg string) { s.renderKeyChange(w, r, status, msg) }
	if !s.stepUp(w, r, "recovery key change", again) {
		return
	}
	k, err := s.Manager.KeyChangeStatus()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if k == nil {
		if err := s.Manager.StartKeyChange(r.Context()); err != nil {
			if msg, ok := usererr.Message(err); ok {
				again(http.StatusBadRequest, msg)
				return
			}
			s.serverError(w, err)
			return
		}
		s.Store.Audit(r.Context(), u, "recovery key change", "new key made")
	}
	key, err := s.Manager.NewRecoveryKey()
	if err != nil {
		// Already confirmed: there's nothing to show here.
		redirect(w, r, "/backups#recovery-key")
		return
	}
	s.Store.Audit(r.Context(), u, "recovery key change", "new key shown")
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "backups-key-change", map[string]any{"User": u, "NewKey": key})
}

func (s *Server) keyChangeConfirm(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("saved") != "on" {
		s.renderKeyChange(w, r, http.StatusBadRequest,
			"Tick the box once you've saved the new key. To see it again, enter your password and a code.")
		return
	}
	if err := s.Manager.ConfirmKeyChange(r.Context()); err != nil {
		if msg, ok := usererr.Message(err); ok {
			s.renderKeyChange(w, r, http.StatusBadRequest, msg)
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "recovery key change", "confirmed saved; changing every backup")
	redirect(w, r, "/backups#recovery-key")
}

func (s *Server) keyChangeCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.Manager.CancelKeyChange(); err != nil {
		if msg, ok := usererr.Message(err); ok {
			s.renderKeyChange(w, r, http.StatusBadRequest, msg)
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "recovery key change", "cancelled before it started")
	redirect(w, r, "/backups#recovery-key")
}
