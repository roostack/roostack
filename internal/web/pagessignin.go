package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/store"
)

// Signing in to Roostack's own pages at login.<domain> (SPEC.md §5.2,
// "Signing in to Roostack's own pages"): one sign-in for the apps and for
// Roostack. Someone without a session at roost.<domain> is sent to the
// sign-in page, which sends them back with a one-time code; Roostack swaps
// it for its own session, linked to the sign-in, so signing out of either
// ends both. Members' sessions last as long as the sign-in; Owners and
// Admins keep Roostack's idle limit, after which the sign-in page asks
// them for their two-factor code (or, without two-factor, their password).

const (
	// pathPagesStart, on Roostack's own address, starts the trip to the
	// sign-in page; pathPagesCallback is where it comes back with a code.
	pathPagesStart    = "/login/sign-in"
	pathPagesCallback = "/login/sign-in/callback"
	// pagesStateCookie ties a code to the browser that started the trip,
	// so nobody can sign someone else's browser in as themselves.
	pagesStateCookie = "roostack_sign_in_state"
	// pagesTrip is how long the trip may take, the sign-in included.
	pagesTrip = 10 * time.Minute

	// signInPagesPath, on the sign-in page's address, hands Roostack's
	// pages a code; signInConfirmPath is the step Owners and Admins take
	// there after Roostack's idle limit.
	signInPagesPath   = "/roostack"
	signInConfirmPath = "/roostack/confirm"
)

// pagesHandoff is a sign-in on its way to Roostack's pages.
type pagesHandoff struct {
	userID int64
	// signIn is the sign-in's hash ("" for Roostack's pages alone), ends
	// when it ends, and remember whether its cookie outlives the browser.
	signIn   string
	ends     time.Time
	remember bool
	// state is the hash of the trip's cookie, and next the page to go to.
	state, next string
	// how is said in the audit log.
	how     string
	expires time.Time
}

// pagesSignIn reports whether Roostack's own pages, as r reached them,
// sign in through the sign-in page: at their own address, over HTTPS,
// while the sign-in page is served (a service has single sign-on on).
// Through the SSH tunnel, a local address or the setup pages, Roostack's
// own login page is the way in.
func (s *Server) pagesSignIn(r *http.Request) bool {
	if s.SignIn == nil || s.SSO == nil || s.SignInHost == "" || s.UIHost == "" || fromSetupListener(r) ||
		!isHTTPS(r) || !strings.EqualFold(hostOnly(r.Host), s.UIHost) {
		return false
	}
	return len(s.SSO.SSOClients()) > 0
}

// toSignInPage sends someone without a session to start the trip, back to
// the page they asked for.
func (s *Server) toSignInPage(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	if r.Method == http.MethodGet && r.Header.Get("HX-Request") != "true" && r.URL.Path != "/login" {
		q.Set("next", r.URL.RequestURI())
	}
	if sessionOf(r).Expired || r.URL.Query().Get("expired") == "1" {
		q.Set("away", "1")
	}
	to := pathPagesStart
	if len(q) > 0 {
		to += "?" + q.Encode()
	}
	redirect(w, r, to)
}

// localNext keeps a page of Roostack's own to go to after signing in, or
// "/" (no other address, and not the login pages).
func localNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n") ||
		next == "/login" || strings.HasPrefix(next, "/login?") || strings.HasPrefix(next, "/login/") || strings.HasPrefix(next, "/logout") {
		return "/"
	}
	return next
}

// pagesStart sets the trip's cookie and goes to the sign-in page.
func (s *Server) pagesStart(w http.ResponseWriter, r *http.Request) {
	if s.needsSetup() {
		redirect(w, r, "/setup")
		return
	}
	if !s.pagesSignIn(r) {
		redirect(w, r, "/login")
		return
	}
	state, err := authkit.NewToken()
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: pagesStateCookie, Value: state, Path: pathPagesStart, MaxAge: int(pagesTrip.Seconds()),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	q := url.Values{"state": {authkit.HashToken(state)}, "next": {localNext(r.FormValue("next"))}}
	if r.FormValue("away") == "1" {
		q.Set("away", "1")
	}
	redirect(w, r, "https://"+s.SignInHost+signInPagesPath+"?"+q.Encode())
}

// pagesCallback swaps a code from the sign-in page for a session of
// Roostack's own pages.
func (s *Server) pagesCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := authkit.HashToken(r.FormValue("code"))
	s.mu.Lock()
	h := s.handoffs[key]
	delete(s.handoffs, key)
	s.mu.Unlock()
	c, err := r.Cookie(pagesStateCookie)
	http.SetCookie(w, &http.Cookie{Name: pagesStateCookie, Value: "", Path: pathPagesStart, MaxAge: -1, HttpOnly: true, Secure: true})
	if h == nil || time.Now().After(h.expires) || err != nil || !authkit.TokensEqual(authkit.HashToken(c.Value), h.state) {
		if currentUser(r) != nil {
			// Another tab's trip got here first.
			redirect(w, r, "/")
			return
		}
		s.Log.Warn("refused a sign-in from the sign-in page", "remote", r.RemoteAddr)
		s.Store.Audit(ctx, nil, "failed login", "a link from the sign-in page that didn't work, from "+r.RemoteAddr)
		w.WriteHeader(http.StatusBadRequest)
		s.renderLogin(w, r, map[string]any{"Error": "Signing in through the sign-in page didn't work: the link was too old, " +
			"or opened in another browser. Try again, or log in here."})
		return
	}
	u, err := s.Store.UserByID(ctx, h.userID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if old, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(ctx, authkit.HashToken(old.Value))
	}
	if h.signIn == "" {
		// Turned away by the sign-in page: Roostack's pages alone, for the
		// step it asked for.
		err = s.startSession(w, r, u, false)
	} else {
		err = s.startLinkedSession(ctx, w, r, u, h)
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(ctx, u, "login", "through the sign-in page"+h.how)
	s.noteDevice(w, r, u)
	redirect(w, r, h.next)
}

// startLinkedSession signs u in to Roostack's pages for as long as their
// sign-in lasts. A Member's isn't signed out for inactivity (remember);
// an Owner's or Admin's is (checkSession).
func (s *Server) startLinkedSession(ctx context.Context, w http.ResponseWriter, r *http.Request, u *store.User, h *pagesHandoff) error {
	token, err := authkit.NewToken()
	if err != nil {
		return err
	}
	if err := s.Store.CreateLinkedSession(ctx, authkit.HashToken(token), u.ID, h.signIn, h.ends, u.Role == auth.RoleMember); err != nil {
		return err
	}
	c := &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r)}
	if h.remember {
		c.Expires = h.ends
	}
	http.SetCookie(w, c)
	return nil
}

// signInToPages is Roostack's pages asking the sign-in page who's signed in:
// someone signed in goes back with a code, once an Owner or Admin has
// confirmed it's them if Roostack's idle limit has passed.
func (s *Server) signInToPages(w http.ResponseWriter, r *http.Request) {
	trip, ok := pagesTripOf(r.URL.Query())
	if !ok {
		s.renderSignIn(w, r, http.StatusBadRequest, signInView{State: "error", Error: "This link isn't right. Open Roostack's pages again."})
		return
	}
	sess, ok := s.signInSession(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusSeeOther)
		return
	}
	if s.pagesConfirm(r.Context(), sess) {
		s.renderSignIn(w, r, http.StatusOK, s.confirmView(r, sess, trip, ""))
		return
	}
	s.handOffPages(w, r, sess.User, &sess, trip, "")
}

// pagesTripInfo is a trip from Roostack's pages: its state, and where to
// go there.
type pagesTripInfo struct{ state, next string }

// pagesTripOf reads a trip from the sign-in page's query or form.
func pagesTripOf(q url.Values) (pagesTripInfo, bool) {
	state := q.Get("state")
	if len(state) != 64 || strings.Trim(state, "0123456789abcdef") != "" {
		return pagesTripInfo{}, false
	}
	return pagesTripInfo{state: state, next: localNext(q.Get("next"))}, true
}

// pagesTripFrom reads a trip from a next address on the sign-in page.
func pagesTripFrom(next string) (pagesTripInfo, bool) {
	rest, ok := strings.CutPrefix(next, signInPagesPath+"?")
	if !ok {
		return pagesTripInfo{}, false
	}
	q, err := url.ParseQuery(rest)
	if err != nil {
		return pagesTripInfo{}, false
	}
	return pagesTripOf(q)
}

// pagesConfirm says whether an Owner or Admin must confirm it's them
// before their sign-in opens Roostack's pages: their password was typed
// longer ago than Roostack's idle limit.
func (s *Server) pagesConfirm(ctx context.Context, sess sso.Session) bool {
	if sess.User.Role == auth.RoleMember {
		return false
	}
	limit := s.idleLimit(ctx)
	return limit > 0 && time.Since(sess.AuthTime) > limit
}

func (s *Server) confirmView(r *http.Request, sess sso.Session, trip pagesTripInfo, msg string) signInView {
	return signInView{State: "confirm", User: sess.User, Trip: trip.state, Next: trip.next, Error: msg,
		Code: sess.User.TOTPSecret != "", Away: r.FormValue("away") == "1",
		App: "Roostack", AppLogo: "/static/favicon.svg"}
}

// signInConfirm is that step: the code from their authenticator app, or
// their password if they have no two-factor.
func (s *Server) signInConfirm(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	r.ParseForm()
	trip, ok := pagesTripOf(r.PostForm)
	if !ok {
		s.renderSignIn(w, r, http.StatusBadRequest, signInView{State: "error", Error: "This link isn't right. Open Roostack's pages again."})
		return
	}
	sess, ok := s.signInSession(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?"+url.Values{"next": {signInPagesPath + "?" + url.Values{"state": {trip.state}, "next": {trip.next}}.Encode()}}.Encode(),
			http.StatusSeeOther)
		return
	}
	u := sess.User
	how := " (confirmed with a two-factor code)"
	if u.TOTPSecret != "" {
		res, until, err := s.checkCode(ctx, u.ID, r.FormValue("code"))
		if err != nil {
			s.serverError(w, err)
			return
		}
		if res != codeOK {
			s.Log.Warn("failed 2FA code", "user", u.Username, "remote", r.RemoteAddr)
			time.Sleep(failedLoginDelay)
			if res == codeLocked {
				s.Store.Audit(ctx, u, auditLocked, "opening Roostack's pages from the sign-in page")
				s.renderSignIn(w, r, http.StatusTooManyRequests, s.confirmView(r, sess, trip, lockedMessage(until)))
				return
			}
			s.Store.Audit(ctx, u, "failed sign-in", "a wrong code opening Roostack's pages from "+r.RemoteAddr)
			s.renderSignIn(w, r, http.StatusUnauthorized, s.confirmView(r, sess, trip,
				"That code didn't work. Check your authenticator app and try again."))
			return
		}
	} else {
		ok, err := authkit.VerifyPassword(u.PasswordHash, r.FormValue("password"))
		if err != nil {
			s.Log.Error("verify password", "user", u.Username, "err", err)
		}
		if !ok {
			s.Log.Warn("failed sign-in", "user", u.Username, "remote", r.RemoteAddr)
			s.Store.Audit(ctx, u, "failed sign-in", "a wrong password opening Roostack's pages from "+r.RemoteAddr)
			time.Sleep(failedLoginDelay)
			s.renderSignIn(w, r, http.StatusUnauthorized, s.confirmView(r, sess, trip, "Wrong password."))
			return
		}
		how = " (confirmed with a password)"
	}
	s.handOffPages(w, r, u, &sess, trip, how)
}

// handOffPages sends u back to Roostack's pages with a one-time code for
// a session there: linked to sess, or for Roostack's pages alone without
// one.
func (s *Server) handOffPages(w http.ResponseWriter, r *http.Request, u *store.User, sess *sso.Session, trip pagesTripInfo, how string) {
	code, err := authkit.NewToken()
	if err != nil {
		s.serverError(w, err)
		return
	}
	now := time.Now()
	h := &pagesHandoff{userID: u.ID, state: trip.state, next: trip.next, how: how, expires: now.Add(sso.CodeLifetime)}
	if sess != nil {
		h.signIn, h.ends, h.remember = sess.Hash, sess.Expires, sess.Remember
	}
	s.mu.Lock()
	for k, o := range s.handoffs {
		if now.After(o.expires) {
			delete(s.handoffs, k)
		}
	}
	s.handoffs[authkit.HashToken(code)] = h
	s.mu.Unlock()
	http.Redirect(w, r, "https://"+s.UIHost+pathPagesCallback+"?"+url.Values{"code": {code}}.Encode(), http.StatusSeeOther)
}
