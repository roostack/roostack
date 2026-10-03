package web

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"roostack.cloud/roostack/internal/store"
)

// Signing out after a while without activity (SPEC.md §5): someone who
// walks away from a signed-in browser is signed out after the Owner's
// chosen time (30 minutes by default). Opening a page or sending a form
// is activity; the parts of pages that refresh themselves aren't, so an
// open tab doesn't keep a session alive, and once it's over they take the
// page to the login page. "Keep me signed in" (if the Owner allows it)
// makes a session that lasts 30 days on that browser, without the idle
// limit; otherwise the session ends when the browser closes, too.

const (
	idleKey     = "session.idle_minutes"
	rememberKey = "session.remember" // "off": "keep me signed in" isn't offered
	// rememberDuration is how long "keep me signed in" lasts.
	rememberDuration = 30 * 24 * time.Hour
	// touchEvery limits how often a session's last use is saved.
	touchEvery = 30 * time.Second
)

// idleChoices are the choices, in minutes; 0 is never.
var idleChoices = []int{15, 30, 60, 240, 0}

type sessionKey struct{}

// sessionState is what loadSession found about the request's session.
type sessionState struct {
	Expired  bool // it ended for inactivity, just now
	Remember bool
}

func sessionOf(r *http.Request) sessionState {
	st, _ := r.Context().Value(sessionKey{}).(sessionState)
	return st
}

// idleLimit is how long a session may go unused (0: no limit).
func (s *Server) idleLimit(ctx context.Context) time.Duration {
	v, _ := s.Store.Setting(ctx, idleKey)
	if n, err := strconv.Atoi(v); err == nil && slices.Contains(idleChoices, n) {
		return time.Duration(n) * time.Minute
	}
	return 30 * time.Minute
}

// offerRemember says whether the login page offers "keep me signed in".
func (s *Server) offerRemember(ctx context.Context) bool {
	v, _ := s.Store.Setting(ctx, rememberKey)
	return v != "off"
}

// activityRequest reports whether a request is someone doing something:
// any form sent, or a page opened (not a part of one refreshing itself,
// which htmx asks for).
func activityRequest(r *http.Request) bool {
	return r.Method != http.MethodGet || r.Header.Get("HX-Request") != "true"
}

// checkSession applies the idle limit to a session found for a request:
// it returns the request with the user, or marks it expired.
func (s *Server) checkSession(r *http.Request, hash string, u *store.User, lastSeen time.Time, remember bool) *http.Request {
	now := time.Now()
	if limit := s.idleLimit(r.Context()); !remember && limit > 0 && now.Sub(lastSeen) > limit {
		s.Store.DeleteSession(r.Context(), hash)
		return r.WithContext(context.WithValue(r.Context(), sessionKey{}, sessionState{Expired: true}))
	}
	if activityRequest(r) && now.Sub(lastSeen) > touchEvery {
		if err := s.Store.TouchSession(r.Context(), hash, now); err != nil {
			s.Log.Error("recording a session's use", "err", err)
		}
	}
	ctx := context.WithValue(r.Context(), userKey{}, u)
	return r.WithContext(context.WithValue(ctx, sessionKey{}, sessionState{Remember: remember}))
}

// renderLogin shows the login page, saying why if a session just ended.
func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, data map[string]any) {
	data["OfferRemember"] = s.offerRemember(r.Context())
	if r.URL.Query().Get("expired") == "1" && data["Error"] == nil {
		data["Expired"] = int(s.idleLimit(r.Context()).Minutes())
	}
	s.render(w, "login", data)
}

// sessionSettingsSubmit sets the idle limit and whether "keep me signed
// in" is offered (Settings → People).
func (s *Server) sessionSettingsSubmit(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(r.FormValue("idle"))
	if err != nil || !slices.Contains(idleChoices, n) {
		http.Error(w, "Choose when people are signed out.", http.StatusBadRequest)
		return
	}
	remember := "on"
	if r.FormValue("remember") != "on" {
		remember = "off"
	}
	s.Store.SetSetting(r.Context(), idleKey, strconv.Itoa(n))
	s.Store.SetSetting(r.Context(), rememberKey, remember)
	s.Store.Audit(r.Context(), currentUser(r), "settings", "sign out after "+idleText(n)+`; "keep me signed in" `+remember)
	redirect(w, r, "/settings/people?signin=saved#signing-in")
}

// idleText says an idle choice in words.
func idleText(minutes int) string {
	switch {
	case minutes == 0:
		return "never (only when they sign out)"
	case minutes%60 == 0:
		h := minutes / 60
		if h == 1 {
			return "1 hour"
		}
		return strconv.Itoa(h) + " hours"
	}
	return strconv.Itoa(minutes) + " minutes"
}
