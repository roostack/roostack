package web

import (
	"net/http"
	"sync"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/attention"
)

// Showing the recovery key again (SPEC.md §6.9): it opens every backup,
// and keeps working after a password change, so the Owner proves it's
// them first with their password and a fresh two-factor code, even though
// they're logged in. That way a session left open, or stolen, isn't
// enough. Each showing, and each failed attempt, goes in the audit log.

// maxStepUpFailures wrong password-and-code attempts end the session, so
// a stolen session can't be used to guess the password.
const maxStepUpFailures = 5

// stepUpFailures counts wrong passwords or codes per user when showing
// the recovery key.
type stepUpFailures struct {
	mu     sync.Mutex
	byUser map[int64]int
}

func (s *Server) recoveryKeyShowPage(w http.ResponseWriter, r *http.Request) {
	if !s.hasRecoveryKey() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, "backups-recovery-key", map[string]any{"User": currentUser(r)})
}

func (s *Server) recoveryKeyShowSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.hasRecoveryKey() {
		http.NotFound(w, r)
		return
	}
	u := currentUser(r)
	w.Header().Set("Cache-Control", "no-store")
	again := func(status int, msg string) {
		w.WriteHeader(status)
		s.render(w, "backups-recovery-key", map[string]any{"User": u, "Error": msg})
	}
	if !s.stepUp(w, r, "recovery key", again) {
		return
	}
	key, err := s.Setup.RecoveryKey()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "recovery key", "shown")
	s.render(w, "backups-recovery-key", map[string]any{"User": u, "Key": key})
}

// stepUp checks it's the Owner, though logged in: their password and a
// fresh two-factor code (SPEC.md §6.9). Wrong codes count against the
// account (checkCode), and maxStepUpFailures wrong tries end the session.
// Each failure is audited under action. On failure it has answered, with
// again (the page and an error) or by logging out, and returns false.
func (s *Server) stepUp(w http.ResponseWriter, r *http.Request, action string, again func(status int, msg string)) bool {
	u := currentUser(r)
	ok, err := authkit.VerifyPassword(u.PasswordHash, r.FormValue("password"))
	if err != nil {
		s.serverError(w, err)
		return false
	}
	if ok {
		// Wrong codes count against the account here as at login.
		res, until, err := s.checkCode(r.Context(), u.ID, r.FormValue("code"))
		if err != nil {
			s.serverError(w, err)
			return false
		}
		if res == codeLocked {
			s.Store.Audit(r.Context(), u, attention.AuditLocked, "checking it's you for "+action)
			again(http.StatusTooManyRequests, lockedMessage(until))
			return false
		}
		ok = res == codeOK
	}
	if !ok {
		s.Log.Warn("wrong password or code", "for", action, "user", u.Username, "remote", r.RemoteAddr)
		time.Sleep(failedLoginDelay)
		s.stepUps.mu.Lock()
		s.stepUps.byUser[u.ID]++
		tooMany := s.stepUps.byUser[u.ID] >= maxStepUpFailures
		if tooMany {
			delete(s.stepUps.byUser, u.ID)
		}
		s.stepUps.mu.Unlock()
		if tooMany {
			s.Store.Audit(r.Context(), u, action, "too many wrong passwords or codes: logged out")
			s.endSession(w, r)
			redirect(w, r, "/login")
			return false
		}
		s.Store.Audit(r.Context(), u, action, "wrong password or code")
		again(http.StatusBadRequest, "That password or code didn't work. Check your authenticator app, and try again.")
		return false
	}
	s.stepUps.mu.Lock()
	delete(s.stepUps.byUser, u.ID)
	s.stepUps.mu.Unlock()
	return true
}
