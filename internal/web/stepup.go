package web

import (
	"net/http"
	"time"
)

// Asking again before actions that reach the whole server (SPEC.md §6.9):
// adding a package of your own, restoring a service from a backup,
// removing a service, and changing who can log in over SSH. Each asks for
// the password, and a code when the account has two-factor (stepUp), so a
// session left open or stolen isn't enough. One confirmation covers the
// others for stepUpGrace in the same session, so a few changes in a row
// ask once. The recovery key always asks.

// stepUpGrace is how long one confirmation covers the actions that ask
// again (confirmIt), in the same session.
const stepUpGrace = 5 * time.Minute

// stepUpView says what an action's form must ask (step-up-fields): nothing
// within the grace; otherwise the password, and the code if the account
// has two-factor.
type stepUpView struct {
	Ask, Code bool
}

func (s *Server) stepUpView(r *http.Request) stepUpView {
	u := currentUser(r)
	return stepUpView{Ask: !s.recentlyConfirmed(r), Code: u != nil && u.TOTPSecret != ""}
}

// recentlyConfirmed reports whether this session confirmed it's them in
// the last stepUpGrace.
func (s *Server) recentlyConfirmed(r *http.Request) bool {
	hash := sessionOf(r).Hash
	if hash == "" {
		return false
	}
	s.stepUps.mu.Lock()
	defer s.stepUps.mu.Unlock()
	return time.Now().Before(s.stepUps.confirmed[hash])
}

// confirmIt is stepUp for the actions with a grace: it passes at once
// within stepUpGrace of the last confirmation, and starts the grace when
// the password and code are right.
func (s *Server) confirmIt(w http.ResponseWriter, r *http.Request, action string, again func(status int, msg string)) bool {
	if s.recentlyConfirmed(r) {
		return true
	}
	if !s.stepUp(w, r, action, again) {
		return false
	}
	if hash := sessionOf(r).Hash; hash != "" {
		now := time.Now()
		s.stepUps.mu.Lock()
		if s.stepUps.confirmed == nil {
			s.stepUps.confirmed = map[string]time.Time{}
		}
		for h, until := range s.stepUps.confirmed {
			if now.After(until) {
				delete(s.stepUps.confirmed, h)
			}
		}
		s.stepUps.confirmed[hash] = now.Add(stepUpGrace)
		s.stepUps.mu.Unlock()
	}
	return true
}
