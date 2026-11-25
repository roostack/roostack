package web

import (
	"context"
	"crypto/tls"
	"errors"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/store"
)

// Signing in to Roostack's own pages at my.<domain> (SPEC.md §5.2,
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
	// pagesConfirmFor is how long confirming it's them on the sign-in page
	// lasts for another trip, such as the one started again when the
	// first's cookie had gone (pagesCallback).
	pagesConfirmFor = 2 * time.Minute

	// signInPagesPath, on the sign-in page's address, hands Roostack's
	// pages a code; signInConfirmPath is the step Owners and Admins take
	// there after Roostack's idle limit.
	signInPagesPath   = "/roostack"
	signInConfirmPath = "/roostack/confirm"
	// signInServedPath answers Roostack asking, through Caddy, whether
	// Caddy serves the sign-in page yet (signInServed).
	signInServedPath = "/roostack/served"
)

// SingleSignOn is the sign-in page (SPEC.md §5.2), and Roostack's own
// pages signing in through it.
type SingleSignOn struct {
	// SignIn is the identity provider (internal/sso), served on its own
	// listener (SignInHandler); nil: none. SSO turns single sign-on on and
	// off for services. SignInHost is my.<domain>, and SSODir holds the
	// sign-in page's logo.
	SignIn     *sso.Provider
	SSO        SSOServices
	SignInHost string
	// SignInServed asks whether Caddy serves the sign-in page yet
	// (CaddyServesSignIn); nil: never, so Roostack's own pages keep their
	// own login.
	SignInServed func(context.Context) bool
	SSODir       string
	signInPages  *template.Template
	// acctPages are Your account's (youraccount.go).
	acctPages *template.Template

	// served is the last answer to whether Caddy serves the sign-in page
	// (signInServed), under servedMu, and servedToken what the sign-in
	// listener answers the question with, so the answer can only come
	// from it.
	servedMu    sync.Mutex
	served      servedCheck
	servedToken string
}

// pagesHandoff is a sign-in on its way to Roostack's pages.
type pagesHandoff struct {
	userID int64
	// signIn is the sign-in's hash ("" for Roostack's pages alone), ends
	// when it ends, and remember whether its cookie outlives the browser.
	signIn   string
	ends     time.Time
	remember bool
	// state is the hash of the trip's cookie, next the page to go to, and
	// again that the trip is a second try (pagesTripInfo).
	state, next string
	again       bool
	// how is said in the audit log.
	how string
}

// pagesSignIn reports whether Roostack's own pages, as r reached them,
// sign in through the sign-in page: at their own address, over HTTPS,
// once Caddy serves the sign-in page (always, once the lab has an
// address: SPEC.md §5.3).
// Through the SSH tunnel, a local address or the setup pages, Roostack's
// own login page is the way in.
func (s *Server) pagesSignIn(r *http.Request) bool {
	if s.SignIn == nil || s.SSO == nil || s.SignInHost == "" || s.UIHost == "" || fromSetupListener(r) ||
		!isHTTPS(r) || !strings.EqualFold(public.HostOnly(r.Host), s.UIHost) {
		return false
	}
	return s.signInServed(r.Context())
}

// servedCheck is an answer to whether Caddy serves the sign-in page, and
// when it was asked.
type servedCheck struct {
	ok bool
	at time.Time
}

// signInServed says whether Caddy serves the sign-in page: a service has
// single sign-on on as soon as it's turned on, but Caddy serves
// my.<domain> only once the apply reloads it, after downloading the
// service's image (minutes), or never if the apply fails (SPEC.md §5.2).
// Until then Roostack's own pages keep their own login. The answer is kept
// a minute once yes, 5 seconds while no, so someone waiting gets in soon
// after it changes.
func (s *Server) signInServed(ctx context.Context) bool {
	s.servedMu.Lock()
	c := s.served
	s.servedMu.Unlock()
	keep := 5 * time.Second
	if c.ok {
		keep = time.Minute
	}
	if !c.at.IsZero() && time.Since(c.at) < keep {
		return c.ok
	}
	ok := s.SignInServed != nil && s.SignInServed(ctx)
	if ok != c.ok && !c.at.IsZero() {
		s.Log.Info("Roostack's pages sign in through the sign-in page", "now", ok)
	}
	s.servedMu.Lock()
	s.served = servedCheck{ok: ok, at: time.Now()}
	s.servedMu.Unlock()
	return ok
}

// CaddyServesSignIn asks Caddy, at caddy (this server's, as monitoring
// does: manager.CaddyLocal), for the sign-in page's signInServedPath, and
// says whether the answer came from this sign-in listener. Only whether
// Caddy routes the name is asked, so its certificate isn't checked (it
// may still be on its way) and nothing is sent.
func (s *Server) CaddyServesSignIn(caddy string) func(context.Context) bool {
	return func(ctx context.Context) bool {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		tr := &http.Transport{
			TLSClientConfig:   &tls.Config{ServerName: s.SignInHost, InsecureSkipVerify: true},
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, caddy)
			},
		}
		defer tr.CloseIdleConnections()
		client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+s.SignInHost+signInServedPath, nil)
		if err != nil {
			return false
		}
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return resp.StatusCode == http.StatusOK && authkit.TokensEqual(string(body), s.servedToken)
	}
}

// signInServedAnswer answers CaddyServesSignIn. The token isn't a secret
// (anyone who can reach the sign-in page may read it): it only shows the
// answer came from this listener, not Caddy's own page or another site.
func (s *Server) signInServedAnswer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, s.servedToken)
}

// toSignInPage sends someone without a session to start the trip, back to
// the page they asked for.
func (s *Server) toSignInPage(w http.ResponseWriter, r *http.Request) {
	q := url.Values{}
	if r.Method == http.MethodGet && r.Header.Get("HX-Request") != "true" && r.URL.Path != "/login" {
		q.Set("next", r.URL.RequestURI())
	} else if next := r.URL.Query().Get("next"); r.URL.Path == "/login" && next != "" {
		// /login?next=… (the page asked for before logging in) carries on.
		q.Set("next", next)
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
	if r.FormValue("again") == "1" {
		q.Set("again", "1")
	}
	redirect(w, r, "https://"+s.SignInHost+signInPagesPath+"?"+q.Encode())
}

// pagesCallback swaps a code from the sign-in page for a session of
// Roostack's own pages.
func (s *Server) pagesCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := authkit.HashToken(r.FormValue("code"))
	h, ok := s.handoffs.take(key)
	c, err := r.Cookie(pagesStateCookie)
	http.SetCookie(w, &http.Cookie{Name: pagesStateCookie, Value: "", Path: pathPagesStart, MaxAge: -1, HttpOnly: true, Secure: true})
	if !ok || err != nil || !authkit.TokensEqual(authkit.HashToken(c.Value), h.state) {
		if currentUser(r) != nil {
			// Another tab's trip got here first.
			redirect(w, r, "/")
			return
		}
		if ok && !h.again {
			// The sign-in was real, but this browser's cookie for the trip
			// had gone: it lasts pagesTrip, and a page left open on the
			// sign-in page, or another tab's trip, outlasts or replaces it.
			// Going again is safe, as the sign-in page will hand over
			// whoever is signed in there in this browser, and it won't ask
			// an Owner or Admin who has just confirmed (pagesConfirmFor).
			s.Log.Info("starting the trip to the sign-in page again: its cookie had gone", "remote", r.RemoteAddr)
			redirect(w, r, pathPagesStart+"?"+url.Values{"next": {h.next}, "again": {"1"}}.Encode())
			return
		}
		s.Log.Warn("refused a sign-in from the sign-in page", "remote", r.RemoteAddr)
		s.Store.Audit(ctx, nil, "failed login", "a link from the sign-in page that didn't work, from "+r.RemoteAddr)
		w.WriteHeader(http.StatusBadRequest)
		s.renderLogin(w, r, map[string]any{"Error": "Signing in through the sign-in page didn't work: the link was too old " +
			"or opened in another browser. Try again or log in here."})
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
	s.noteSessionDevice(r, authkit.HashToken(token))
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
	how := ""
	if s.pagesConfirm(r.Context(), sess) {
		var ok bool
		if how, ok = s.confirmed.get(sess.Hash); !ok {
			s.renderSignIn(w, r, http.StatusOK, s.confirmView(r, sess, trip, ""))
			return
		}
	}
	s.handOffPages(w, r, sess.User, &sess, trip, how)
}

// pagesTripInfo is a trip from Roostack's pages: its state, where to go
// there, and whether it's a second try, started again when the first's
// cookie had gone (pagesCallback), so it isn't started a third time.
type pagesTripInfo struct {
	state, next string
	again       bool
}

// pagesTripOf reads a trip from the sign-in page's query or form.
func pagesTripOf(q url.Values) (pagesTripInfo, bool) {
	state := q.Get("state")
	if len(state) != 64 || strings.Trim(state, "0123456789abcdef") != "" {
		return pagesTripInfo{}, false
	}
	return pagesTripInfo{state: state, next: localNext(q.Get("next")), again: q.Get("again") == "1"}, true
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
	return signInView{State: "confirm", User: sess.User, Trip: trip.state, Next: trip.next, Again: trip.again, Error: msg,
		Code: sess.User.TOTPSecret != "", Passkey: sess.User.Passkeys > 0, Away: r.FormValue("away") == "1",
		App: "Roostack", AppLogo: "/static/favicon.svg"}
}

// signInConfirm is that step: a passkey, the code from their
// authenticator app, or their password if they have neither.
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
		back := url.Values{"state": {trip.state}, "next": {trip.next}}
		if trip.again {
			back.Set("again", "1")
		}
		http.Redirect(w, r, "/sign-in?"+url.Values{"next": {signInPagesPath + "?" + back.Encode()}}.Encode(), http.StatusSeeOther)
		return
	}
	u := sess.User
	how := " (confirmed with a two-factor code)"
	if hasPasskeyAnswer(r) {
		if !s.checkPasskeyOf(w, r, u, ceremonyConfirm) {
			time.Sleep(failedLoginDelay)
			s.renderSignIn(w, r, http.StatusUnauthorized, s.confirmView(r, sess, trip,
				"The passkey didn't work, or you cancelled it. Try again, or use the box below."))
			return
		}
		how = " (confirmed with a passkey)"
	} else if u.TOTPSecret != "" {
		res, until, err := s.checkCode(ctx, u.ID, r.FormValue("code"))
		if err != nil {
			s.serverError(w, err)
			return
		}
		if res != codeOK {
			s.Log.Warn("failed 2FA code", "user", u.Username, "remote", r.RemoteAddr)
			time.Sleep(failedLoginDelay)
			if res == codeLocked {
				s.Store.Audit(ctx, u, attention.AuditLocked, "opening Roostack's pages from the sign-in page")
				s.renderSignIn(w, r, http.StatusTooManyRequests, s.confirmView(r, sess, trip, lockedMessage(until)))
				return
			}
			msg := usedCodeMessage
			if res == codeWrong {
				s.Store.Audit(ctx, u, "failed sign-in", "a wrong code opening Roostack's pages from "+r.RemoteAddr)
				msg = "That code didn't work. Check your authenticator app and try again."
			}
			s.renderSignIn(w, r, http.StatusUnauthorized, s.confirmView(r, sess, trip, msg))
			return
		}
	} else {
		ok, err := authkit.VerifyPassword(u.PasswordHash, r.FormValue("password"))
		if errors.Is(err, authkit.ErrBusy) {
			s.renderSignIn(w, r, http.StatusServiceUnavailable, s.confirmView(r, sess, trip, busyMessage))
			return
		}
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
	s.confirmed.put(sess.Hash, how, time.Now().Add(pagesConfirmFor))
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
	h := &pagesHandoff{userID: u.ID, state: trip.state, next: trip.next, again: trip.again, how: how}
	if sess != nil {
		h.signIn, h.ends, h.remember = sess.Hash, sess.Expires, sess.Remember
	}
	s.handoffs.put(authkit.HashToken(code), h, time.Now().Add(sso.CodeLifetime))
	http.Redirect(w, r, "https://"+s.UIHost+pathPagesCallback+"?"+url.Values{"code": {code}}.Encode(), http.StatusSeeOther)
}
