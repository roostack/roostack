package web

import (
	"context"
	"errors"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/store"
)

// The sign-in page for services, login.<domain> (single sign-on, SPEC.md
// §5.2): its own listener (SignInHandler), which serves nothing of
// Roostack's own pages. People sign in with their Roostack account; apps
// are told who they are through OIDC, and walled services through forward
// auth (internal/sso).

const (
	// signInCookie is the sign-in page's own session, on its host only.
	signInCookie = "roostack_signin"
	// signInPendingCookie links the password step to the code step.
	signInPendingCookie = "roostack_signin_pending"
	// signInLength is how long a sign-in lasts without "keep me signed
	// in" (its cookie also ends when the browser closes); signInRemember
	// with it (SPEC.md §5.2).
	signInLength   = 12 * time.Hour
	signInRemember = 30 * 24 * time.Hour
	// pendingSignIn prefixes the sign-in page's logins waiting for a code
	// in Server.pending, so they're apart from Roostack's own.
	pendingSignIn = "sign-in:"
)

// SSOServices turns single sign-on on and off for services
// (internal/manager).
type SSOServices interface {
	// SSOAvailable says whether a service can have it, and if not why.
	SSOAvailable(name string) (bool, string)
	SetSSO(ctx context.Context, name string, on bool) error
	SSOClients() []sso.Client
	// SSOOff is the title of an installed service whose package takes
	// single sign-on but which has it off ("" otherwise): an app may keep
	// its button after it's turned off (Forgejo, with accounts made
	// through it).
	SSOOff(name string) string
}

var _ SSOServices = (*manager.Manager)(nil)

// signInPages are the sign-in page's templates: a page of their own,
// without Roostack's sidebar.
func parseSignInPages() (*template.Template, error) {
	return template.New("signin.html").Funcs(templateFuncs()).ParseFS(templateFS, "templates/signin.html", "templates/components.html")
}

// SignInHandler serves the sign-in page for services, on its own
// listener (config.SignInPort). Besides its own host, it answers only
// Caddy's forward auth checks and the callbacks on walled services'
// addresses.
func (s *Server) SignInHandler() http.Handler {
	static, _ := fs.Sub(staticFS, "static")
	p := s.SignIn
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler(static)))
	mux.HandleFunc("GET "+sso.PathDiscovery, p.Discovery)
	mux.HandleFunc("GET "+sso.PathJWKS, p.JWKS)
	mux.HandleFunc("POST "+sso.PathToken, p.Token)
	mux.HandleFunc(sso.PathUserInfo, p.UserInfo)
	// OIDC Core §3.1.2.1: GET and POST alike.
	mux.HandleFunc("GET "+sso.PathAuthorize, s.signInAuthorize)
	mux.HandleFunc("POST "+sso.PathAuthorize, s.signInAuthorize)
	mux.HandleFunc("GET "+sso.PathStart, s.signInStart)
	mux.HandleFunc("GET "+signInPagesPath, s.signInToPages)
	mux.HandleFunc("POST "+signInConfirmPath, s.signInConfirm)
	mux.HandleFunc("GET "+signInServedPath, s.signInServedAnswer)
	mux.HandleFunc("GET /{$}", s.signInHome)
	mux.HandleFunc("GET /sign-in", s.signInPage)
	mux.HandleFunc("POST /sign-in", s.signInSubmit)
	mux.HandleFunc("POST /sign-in/code", s.signInCode)
	mux.HandleFunc(sso.PathSignOut, s.signOut)
	mux.HandleFunc("GET /look/{part}", s.lookFile)
	mux.HandleFunc("GET /look/app/{name}", s.lookApp)
	mux.HandleFunc("GET /favicon.ico", s.lookFavicon)
	return s.recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		// No scripts at all. No form-action: after signing in, the browser
		// is redirected on to the app's address, which form-action would
		// block in some browsers.
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; style-src 'self'; img-src 'self' data:; "+
			"frame-ancestors 'none'; base-uri 'none'")
		// Forward auth: Caddy asks with the walled service's own address.
		if r.URL.Path == sso.PathCheck {
			p.Check(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, caddy.WallCallbackPath+"/") {
			if r.URL.Path == sso.PathCallback && r.Method == http.MethodGet {
				p.Callback(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		if !strings.EqualFold(hostOnly(r.Host), s.SignInHost) {
			http.NotFound(w, r)
			return
		}
		// Forms are this page's own (CSRF); apps' calls to the token and
		// userinfo endpoints come from their servers, with no Origin. An
		// app may post its authorisation request from its own page: that's
		// no more than the link it could send instead.
		if r.Method == http.MethodPost && r.URL.Path != sso.PathToken && r.URL.Path != sso.PathUserInfo && r.URL.Path != sso.PathAuthorize {
			if o, err := url.Parse(r.Header.Get("Origin")); err != nil || !strings.EqualFold(o.Host, r.Host) {
				http.Error(w, "Cross-site request rejected.", http.StatusForbidden)
				return
			}
		}
		mux.ServeHTTP(w, r)
	}))
}

func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// signInSession is who's signed in on the sign-in page, if anyone.
func (s *Server) signInSession(r *http.Request) (sso.Session, bool) {
	c, err := r.Cookie(signInCookie)
	if err != nil || c.Value == "" {
		return sso.Session{}, false
	}
	hash := authkit.HashToken(c.Value)
	si, err := s.Store.SignInSession(r.Context(), hash)
	if err != nil {
		return sso.Session{}, false
	}
	return sso.Session{User: si.User, Hash: hash, Expires: si.Expires, Remember: si.Remember, AuthTime: si.At}, true
}

// safeNext keeps where to go after signing in to the sign-in page's own
// authorisation and hand-off pages (to walled services and to Roostack's
// pages), or its home.
func safeNext(next string) string {
	for _, p := range []string{sso.PathAuthorize + "?", sso.PathStart + "?", signInPagesPath + "?"} {
		if strings.HasPrefix(next, p) && !strings.ContainsAny(next, "\r\n\\") {
			return next
		}
	}
	return "/"
}

// signInView is what every sign-in page shows: the lab's look, and the app
// someone is signing in to, if any.
type signInView struct {
	State   string
	LabName string
	Look    signInLook
	// App is what they're signing in to ("" for the page itself).
	App, AppLogo string
	Next         string
	Error        string
	Username     string
	Remember     bool
	// User is who's signed in, on the pages for someone signed in.
	User *store.User
	// Apps are the services they can use (home).
	Apps []signInApp
	// Until is when their sign-in ends (home).
	Until time.Time
	// Owner names who to ask for access or a password.
	Owner string
	// RoostackURL is Roostack's own pages, for things done there.
	RoostackURL string
	// Trip is a trip from Roostack's pages (pagessignin.go), on the step
	// confirming it's them; Code says it asks for a two-factor code (not
	// the password), and Away that their session there just ended.
	Trip       string
	Code, Away bool
	// PagesHost is Roostack's pages' address (home), if they sign in here.
	PagesHost string
	// Photos: the person may migrate photos into Immich (SPEC.md §8.5),
	// on Roostack's own pages.
	Photos bool
}

type signInApp struct {
	Title, URL, Logo, Host string
}

func (s *Server) renderSignIn(w http.ResponseWriter, r *http.Request, status int, v signInView) {
	v.LabName = s.labName()
	v.Look = s.signInLook(r.Context())
	v.Owner = s.ownerName(r.Context())
	v.RoostackURL = "https://" + s.UIHost
	if v.App == "" && v.Next != "" {
		v.App, v.AppLogo = s.appFor(v.Next)
	}
	var buf strings.Builder
	if err := s.signInPages.Execute(&buf, v); err != nil {
		s.Log.Error("render sign-in page", "err", err)
		http.Error(w, "Something went wrong showing this page. Details are in the Roostack log.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write([]byte(buf.String()))
}

// appFor is the app a next address leads to, and its logo.
func (s *Server) appFor(next string) (string, string) {
	if _, ok := pagesTripFrom(next); ok {
		return "Roostack", "/static/favicon.svg"
	}
	u, err := url.Parse(next)
	if err != nil {
		return "", ""
	}
	id := u.Query().Get("client_id")
	if id == "" {
		id = u.Query().Get("service")
	}
	if c, ok := s.SignIn.Client(id); ok {
		return c.Title, "/look/app/" + c.ID
	}
	return "", ""
}

// ownerName is who to ask: the lab's Owner by name, or "an Owner".
func (s *Server) ownerName(ctx context.Context) string {
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return "the lab's Owner"
	}
	var owners []store.User
	for _, u := range users {
		if u.Role == auth.RoleOwner {
			owners = append(owners, u)
		}
	}
	if len(owners) != 1 {
		return "an Owner of the lab"
	}
	if owners[0].Name != "" {
		first, _, _ := strings.Cut(owners[0].Name, " ")
		return first
	}
	return owners[0].Username
}

// signInAuthorize is an app sending someone to sign in (OIDC), with the
// request in the query or, posted, in the form.
func (s *Server) signInAuthorize(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderSignIn(w, r, http.StatusBadRequest, signInView{State: "error", Error: "This request can't be read."})
		return
	}
	params := r.Form
	req, aerr := s.SignIn.ParseAuthorize(params)
	if aerr != nil {
		if aerr.Redirect != "" {
			http.Redirect(w, r, aerr.Redirect, http.StatusFound)
			return
		}
		msg := aerr.Message
		if title := s.SSO.SSOOff(params.Get("client_id")); title != "" {
			msg = "Single sign-on is off for " + title + ". Sign in with " + title + "'s own password instead."
		}
		s.renderSignIn(w, r, http.StatusBadRequest, signInView{State: "error", Error: msg})
		return
	}
	sess, ok := s.signInSession(r)
	if !ok || req.SignInAgain(sess, time.Now()) {
		if req.PromptNone {
			http.Redirect(w, r, req.LoginRequired(), http.StatusFound)
			return
		}
		// Once signed in, carry on with the same request, less what asked
		// for signing in again (it's just been done).
		next := url.Values{}
		for k, v := range params {
			if k != "prompt" && k != "max_age" {
				next[k] = v
			}
		}
		http.Redirect(w, r, "/sign-in?"+url.Values{"next": {sso.PathAuthorize + "?" + next.Encode()}}.Encode(), http.StatusSeeOther)
		return
	}
	if ok, err := s.Store.CanUse(r.Context(), req.Client.ID, sess.User.ID); err != nil || !ok {
		if err != nil {
			s.serverError(w, err)
			return
		}
		if req.PromptNone {
			http.Redirect(w, r, req.AccessDenied(), http.StatusFound)
			return
		}
		s.renderSignIn(w, r, http.StatusForbidden, signInView{State: "denied", App: req.Client.Title,
			AppLogo: "/look/app/" + req.Client.ID, User: sess.User})
		return
	}
	to, err := s.SignIn.Approve(req, sess)
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// signInStart is a walled service's visitor, sent here to sign in.
func (s *Server) signInStart(w http.ResponseWriter, r *http.Request) {
	req, ok := s.SignIn.ParseStart(r.URL.Query())
	if !ok {
		s.renderSignIn(w, r, http.StatusNotFound, signInView{State: "error",
			Error: "This service isn't behind the lab's sign-in page. Open it again from its own address."})
		return
	}
	sess, ok := s.signInSession(r)
	if !ok {
		http.Redirect(w, r, "/sign-in?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusSeeOther)
		return
	}
	if ok, err := s.Store.CanUse(r.Context(), req.Client.ID, sess.User.ID); err != nil || !ok {
		if err != nil {
			s.serverError(w, err)
			return
		}
		s.renderSignIn(w, r, http.StatusForbidden, signInView{State: "denied", App: req.Client.Title,
			AppLogo: "/look/app/" + req.Client.ID, User: sess.User})
		return
	}
	to, err := s.SignIn.Handoff(req, sess)
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// signInHome is the sign-in page itself: the services someone can use,
// once signed in.
func (s *Server) signInHome(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.signInSession(r)
	if !ok {
		s.signInPage(w, r)
		return
	}
	v := signInView{State: "home", User: sess.User, Until: sess.Expires}
	if s.SSO != nil && len(s.SSO.SSOClients()) > 0 {
		v.PagesHost = s.UIHost
	}
	for _, c := range s.SignIn.Clients() {
		if ok, err := s.Store.CanUse(r.Context(), c.ID, sess.User.ID); err != nil || !ok {
			continue
		}
		host := c.Host
		if host == "" && len(c.RedirectURIs) > 0 {
			if u, err := url.Parse(c.RedirectURIs[0]); err == nil {
				host = u.Host
			}
		}
		if host != "" {
			v.Apps = append(v.Apps, signInApp{Title: c.Title, URL: "https://" + host + "/", Logo: "/look/app/" + c.ID, Host: host})
		}
	}
	v.Photos = len(s.migratable(r.Context(), sess.User)) > 0
	s.renderSignIn(w, r, http.StatusOK, v)
}

func (s *Server) signInPage(w http.ResponseWriter, r *http.Request) {
	s.renderSignIn(w, r, http.StatusOK, signInView{State: "sign-in", Next: safeNext(r.FormValue("next")),
		Remember: s.signInRememberDefault(r.Context())})
}

// signInSubmit is the password step: someone's username or email, and
// their Roostack password.
func (s *Server) signInSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	name := strings.TrimSpace(r.FormValue("username"))
	next := safeNext(r.FormValue("next"))
	remember := r.FormValue("remember") == "on"
	again := func(status int, msg string) {
		s.renderSignIn(w, r, status, signInView{State: "sign-in", Next: next, Error: msg, Username: name, Remember: remember})
	}
	u, err := s.Store.UserByUsername(ctx, name)
	if errors.Is(err, store.ErrNotFound) && strings.Contains(name, "@") {
		u, err = s.Store.UserByEmail(ctx, name)
	}
	ok, known := false, err == nil
	switch {
	case err == nil:
		ok, err = authkit.VerifyPassword(u.PasswordHash, r.FormValue("password"))
		if err != nil {
			s.Log.Error("verify password", "user", u.Username, "err", err)
		}
	case errors.Is(err, store.ErrNotFound):
		authkit.VerifyNoAccount(r.FormValue("password"))
	default:
		s.serverError(w, err)
		return
	}
	if !ok {
		who := "an unknown username"
		if known {
			who = u.Username
		}
		s.Log.Warn("failed sign-in", "user", who, "remote", r.RemoteAddr)
		s.Store.Audit(ctx, nil, "failed sign-in", who+" on the sign-in page from "+r.RemoteAddr)
		time.Sleep(failedLoginDelay)
		again(http.StatusUnauthorized, "Wrong username or password.")
		return
	}
	blocked := ""
	switch {
	case u.MustChangePassword:
		blocked = "You have a temporary password. Choose your own on Roostack's pages first, then sign in here with it."
	case u.TOTPSecret == "" && s.signInNeedsTwoFactor(ctx, u):
		blocked = "Your account needs two-factor authentication. Turn it on on Roostack's pages first, then sign in here."
	}
	trip, toPages := pagesTripFrom(next)
	if blocked != "" && !toPages {
		s.renderSignIn(w, r, http.StatusForbidden, signInView{State: "blocked", Next: next, Error: blocked})
		return
	}
	if blocked != "" && u.TOTPSecret == "" {
		// On the way to Roostack's pages: they go on there for that step
		// alone, as Roostack's own login would let them.
		s.handOffPages(w, r, u, nil, trip, " (Roostack's pages only, until they've done what the sign-in page asks)")
		return
	}
	if u.TOTPSecret != "" {
		if until := authkit.TOTPLockedUntil(u.TOTPFailures, u.TOTPFailedAt); time.Now().Before(until) {
			again(http.StatusTooManyRequests, lockedMessage(until))
			return
		}
		token, err := authkit.NewToken()
		if err != nil {
			s.serverError(w, err)
			return
		}
		now := time.Now()
		s.mu.Lock()
		for k, p := range s.pending {
			if now.After(p.expires) {
				delete(s.pending, k)
			}
		}
		s.pending[pendingSignIn+authkit.HashToken(token)] = &pendingLogin{userID: u.ID, expires: now.Add(pendingDuration), remember: remember,
			pagesOnly: blocked != ""}
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: signInPendingCookie, Value: token, Path: "/sign-in", MaxAge: int(pendingDuration.Seconds()),
			HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: isHTTPS(r)})
		s.renderSignIn(w, r, http.StatusOK, signInView{State: "code", Next: next})
		return
	}
	s.startSignIn(w, r, u, remember, next, "")
}

// signInNeedsTwoFactor: as for Roostack's own pages (Owners always,
// Admins while those are public), and everyone if the Owner chose so.
func (s *Server) signInNeedsTwoFactor(ctx context.Context, u *store.User) bool {
	if s.twoFactorRequired(u) {
		return true
	}
	v, _ := s.Store.Setting(ctx, settingSignInTwoFactor)
	return v == "everyone"
}

// signInCode is the code step.
func (s *Server) signInCode(w http.ResponseWriter, r *http.Request) {
	next := safeNext(r.FormValue("next"))
	c, err := r.Cookie(signInPendingCookie)
	if err != nil {
		redirect(w, r, "/sign-in?"+url.Values{"next": {next}}.Encode())
		return
	}
	key := pendingSignIn + authkit.HashToken(c.Value)
	s.mu.Lock()
	p := s.pending[key]
	if p != nil && time.Now().After(p.expires) {
		delete(s.pending, key)
		p = nil
	}
	s.mu.Unlock()
	if p == nil {
		s.renderSignIn(w, r, http.StatusOK, signInView{State: "sign-in", Next: next, Error: "That took too long. Please sign in again.",
			Remember: s.signInRememberDefault(r.Context())})
		return
	}
	res, until, err := s.checkCode(r.Context(), p.userID, r.FormValue("code"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if res != codeOK {
		time.Sleep(failedLoginDelay)
		if res == codeLocked {
			s.mu.Lock()
			delete(s.pending, key)
			s.mu.Unlock()
			s.renderSignIn(w, r, http.StatusTooManyRequests, signInView{State: "sign-in", Next: next, Error: lockedMessage(until)})
			return
		}
		s.renderSignIn(w, r, http.StatusUnauthorized, signInView{State: "code", Next: next,
			Error: "That code didn't work. Check your authenticator app and try again."})
		return
	}
	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: signInPendingCookie, Value: "", Path: "/sign-in", MaxAge: -1, HttpOnly: true})
	u, err := s.Store.UserByID(r.Context(), p.userID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if trip, ok := pagesTripFrom(next); ok && p.pagesOnly {
		s.handOffPages(w, r, u, nil, trip, " with two-factor code (Roostack's pages only)")
		return
	}
	s.startSignIn(w, r, u, p.remember, next, "with two-factor code")
}

// startSignIn signs someone in on the sign-in page and goes on to next.
// Someone already signed in here who signs in again (an app asked for a
// fresh sign-in) keeps their sign-in, and what it handed out; someone else
// signing in on this browser ends it.
func (s *Server) startSignIn(w http.ResponseWriter, r *http.Request, u *store.User, remember bool, next, how string) {
	if old, ok := s.signInSession(r); ok {
		if old.User.ID == u.ID {
			if err := s.Store.SignedInAgain(r.Context(), old.Hash, time.Now()); err != nil {
				s.serverError(w, err)
				return
			}
			s.Store.Audit(r.Context(), u, "signed in", strings.TrimSpace("again on the sign-in page for services "+how))
			http.Redirect(w, r, next, http.StatusSeeOther)
			return
		}
		if err := s.Store.DeleteSession(r.Context(), old.Hash); err != nil {
			s.serverError(w, err)
			return
		}
	}
	token, err := authkit.NewToken()
	if err != nil {
		s.serverError(w, err)
		return
	}
	length := signInLength
	if remember {
		length = signInRemember
	}
	if err := s.Store.CreateSessionAt(r.Context(), store.OriginSignIn, authkit.HashToken(token), u.ID, time.Now().Add(length), remember); err != nil {
		s.serverError(w, err)
		return
	}
	ck := &http.Cookie{Name: signInCookie, Value: token, Path: "/", HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode}
	if remember {
		ck.MaxAge = int(length.Seconds())
	}
	http.SetCookie(w, ck)
	s.Store.Audit(r.Context(), u, "signed in", strings.TrimSpace("on the sign-in page for services "+how))
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// signOut ends the sign-in, and with it every walled service's access and
// apps' refresh tokens from it. Apps send people here too (OIDC's
// end_session_endpoint), then back to post_logout_redirect_uri if it's
// theirs.
func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	if sess, ok := s.signInSession(r); ok {
		if err := s.Store.DeleteSession(r.Context(), sess.Hash); err != nil {
			s.serverError(w, err)
			return
		}
		s.Store.Audit(r.Context(), sess.User, "signed out", "of the sign-in page for services")
	}
	http.SetCookie(w, &http.Cookie{Name: signInCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: isHTTPS(r)})
	if back := r.FormValue("post_logout_redirect_uri"); back != "" {
		if c, ok := s.SignIn.Client(r.FormValue("client_id")); ok {
			for _, u := range c.RedirectURIs {
				if pu, err1 := url.Parse(u); err1 == nil {
					if bu, err2 := url.Parse(back); err2 == nil && bu.Scheme == "https" && bu.Host == pu.Host {
						http.Redirect(w, r, back, http.StatusFound)
						return
					}
				}
			}
		}
	}
	s.renderSignIn(w, r, http.StatusOK, signInView{State: "out"})
}
