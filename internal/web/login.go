package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/store"
)

// Logging in to Roostack's own pages: the password, then the two-factor
// code, the session they start, and logging out (SPEC.md §5).

const (
	sessionCookie   = "roostack_session"
	sessionDuration = 7 * 24 * time.Hour
	// pendingCookie links the password step of a login to the 2FA step.
	pendingCookie   = "roostack_pending"
	pendingDuration = 5 * time.Minute
	// failedLoginDelay slows down password guessing. Caddy rate limiting is
	// the main defence (SPEC.md §6.10).
	failedLoginDelay = 500 * time.Millisecond
)

type pendingLogin struct {
	userID   int64
	expires  time.Time
	remember bool   // "keep me signed in" was ticked
	next     string // the page to go to afterwards (localNext)
	// pagesOnly: a sign-in at login.<domain> that the sign-in page itself
	// turns away (a temporary password, two-factor to turn on), going on to
	// Roostack's pages for that step alone (pagessignin.go).
	pagesOnly bool
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.needsSetup() {
		redirect(w, r, "/setup")
		return
	}
	// Roostack's own login page stays at /login?here, for when the sign-in
	// page can't be reached.
	if s.pagesSignIn(r) && !r.URL.Query().Has("here") {
		s.toSignInPage(w, r)
		return
	}
	s.renderLogin(w, r, map[string]any{"Next": nextOf(r)})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	u, err := s.Store.UserByUsername(r.Context(), username)
	ok, known := false, err == nil
	switch {
	case err == nil:
		ok, err = authkit.VerifyPassword(u.PasswordHash, r.FormValue("password"))
		if err != nil {
			s.Log.Error("verify password", "user", username, "err", err)
		}
	case errors.Is(err, store.ErrNotFound):
		authkit.VerifyNoAccount(r.FormValue("password"))
	default:
		s.serverError(w, err)
		return
	}
	if !ok {
		// What was typed isn't logged or audited unless it's an account's
		// name: people type passwords into the wrong box.
		who := "an unknown username"
		if known {
			who = u.Username
		}
		s.Log.Warn("failed login", "user", who, "remote", r.RemoteAddr)
		s.Store.Audit(r.Context(), nil, "failed login", who+" from "+r.RemoteAddr)
		time.Sleep(failedLoginDelay)
		w.WriteHeader(http.StatusUnauthorized)
		s.renderLogin(w, r, map[string]any{"Error": "Wrong username or password.", "Username": username, "Next": localNext(r.FormValue("next"))})
		return
	}
	if u.TOTPSecret != "" {
		// Say so now rather than after a code that can't work.
		if until := authkit.TOTPLockedUntil(u.TOTPFailures, u.TOTPFailedAt); time.Now().Before(until) {
			w.WriteHeader(http.StatusTooManyRequests)
			s.renderLogin(w, r, map[string]any{"Error": lockedMessage(until), "Username": username, "Next": localNext(r.FormValue("next"))})
			return
		}
		s.startPending(w, r, u)
		return
	}
	if err := s.startSession(w, r, u, r.FormValue("remember") == "on"); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "login", "")
	s.noteDevice(w, r, u)
	redirect(w, r, localNext(r.FormValue("next")))
}

// startPending records a login that passed the password step and asks for
// the 2FA code.
func (s *Server) startPending(w http.ResponseWriter, r *http.Request, u *store.User) {
	token, err := authkit.NewToken()
	if err != nil {
		s.serverError(w, err)
		return
	}
	now := time.Now()
	s.mu.Lock()
	for k, p := range s.pending { // drop expired entries
		if now.After(p.expires) {
			delete(s.pending, k)
		}
	}
	s.pending[originOf(r)+authkit.HashToken(token)] = &pendingLogin{userID: u.ID, expires: now.Add(pendingDuration),
		remember: r.FormValue("remember") == "on", next: localNext(r.FormValue("next"))}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: pendingCookie, Value: token, Path: "/login", MaxAge: int(pendingDuration.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r),
	})
	s.render(w, "totp", map[string]any{})
}

func (s *Server) loginTOTP(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(pendingCookie)
	if err != nil {
		redirect(w, r, "/login")
		return
	}
	key := originOf(r) + authkit.HashToken(c.Value)
	s.mu.Lock()
	p := s.pending[key]
	if p != nil && time.Now().After(p.expires) {
		delete(s.pending, key)
		p = nil
	}
	s.mu.Unlock()
	if p == nil {
		s.renderLogin(w, r, map[string]any{"Error": "That took too long. Please log in again."})
		return
	}

	u, err := s.Store.UserByID(r.Context(), p.userID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	res, until, err := s.checkCode(r.Context(), u.ID, r.FormValue("code"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if res != codeOK {
		s.Log.Warn("failed 2FA code", "user", u.Username, "remote", r.RemoteAddr)
		time.Sleep(failedLoginDelay)
		if res == codeLocked {
			s.Store.Audit(r.Context(), u, auditLocked, "at login")
			s.mu.Lock()
			delete(s.pending, key)
			s.mu.Unlock()
			w.WriteHeader(http.StatusTooManyRequests)
			s.renderLogin(w, r, map[string]any{"Error": lockedMessage(until)})
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "totp", map[string]any{"Error": "That code didn't work. Check your authenticator app and try again."})
		return
	}

	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: "", Path: "/login", MaxAge: -1, HttpOnly: true})
	if err := s.startSession(w, r, u, p.remember); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "login", "with two-factor code")
	s.noteDevice(w, r, u)
	redirect(w, r, p.next)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if s.endSession(w, r) {
		// Signed out of the sign-in page too: it says so.
		redirect(w, r, "https://"+s.SignInHost+sso.PathSignOut)
		return
	}
	redirect(w, r, "/login")
}

// endSession logs this browser out, and out of the sign-in at
// login.<domain> its session came from, if any (one sign-in, one
// sign-out: SPEC.md §5.2). It reports whether there was one.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) bool {
	signedIn := false
	if c, err := r.Cookie(sessionCookie); err == nil {
		hash := authkit.HashToken(c.Value)
		if signIn, err := s.Store.SessionSignIn(r.Context(), hash); err == nil && signIn != "" {
			s.Store.DeleteSession(r.Context(), signIn) // and this session with it
			signedIn = true
			if u := currentUser(r); u != nil {
				s.Store.Audit(r.Context(), u, "signed out", "of Roostack's pages and the sign-in page")
			}
		}
		s.Store.DeleteSession(r.Context(), hash)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	return signedIn
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// startSession signs u in. remember ("keep me signed in", if offered)
// makes it last rememberDuration on this browser without the idle limit;
// otherwise the cookie ends with the browser (session.go).
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *store.User, remember bool) error {
	token, err := authkit.NewToken()
	if err != nil {
		return err
	}
	// Not on the setup pages: their sessions end when they close.
	remember = remember && s.offerRemember(r.Context()) && !fromSetupListener(r)
	expires := time.Now().Add(sessionDuration)
	if remember {
		expires = time.Now().Add(rememberDuration)
	}
	if err := s.Store.CreateSessionAt(r.Context(), sessionOrigin(r), authkit.HashToken(token), u.ID, expires, remember); err != nil {
		return err
	}
	c := &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r)}
	if remember {
		c.Expires = expires
	}
	http.SetCookie(w, c)
	return nil
}

// isHTTPS reports whether the browser connection is HTTPS. Behind Caddy the
// request arrives over plain HTTP with X-Forwarded-Proto set.
func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}
