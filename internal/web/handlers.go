package web

import (
	"errors"
	"html/template"
	"net/http"
	"strings"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/store"
)

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if !s.needsSetup() {
		redirect(w, r, "/")
		return
	}
	if s.LabRestore != nil && s.LabRestore.Running() {
		redirect(w, r, "/setup/restore/progress")
		return
	}
	if !s.setupPassed(r) {
		s.render(w, "setup", map[string]any{}) // first, the setup code
		return
	}
	s.render(w, "setup", s.setupData(nil))
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.needsSetup() {
		redirect(w, r, "/")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	labName := strings.TrimSpace(r.FormValue("lab_name"))
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "setup", s.setupData(map[string]any{"Choice": "new", "Error": msg, "Username": username, "LabName": labName}))
	}
	if s.LabRestore != nil && s.LabRestore.Running() {
		fail("A lab is being restored onto this server.")
		return
	}

	switch {
	case !s.setupPassed(r):
		redirect(w, r, "/setup") // the setup code first (again, if it's been a while)
		return
	case !auth.ValidUsername(username):
		fail("Choose a username of letters and digits (dots, dashes and underscores are fine too), up to 32 characters.")
		return
	case password != r.FormValue("confirm"):
		fail("The passwords don't match.")
		return
	}
	if err := authkit.CheckPasswordPolicy(password); err != nil {
		fail(capitalize(err.Error()) + ".")
		return
	}
	hash, err := authkit.HashPassword(password)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if labName != "" && s.Setup != nil {
		err := s.Setup.SetLabName(r.Context(), labName, false)
		if msg, ok := userMessage(err); ok {
			fail(msg)
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
		s.mu.Lock()
		s.LabName = labName
		s.mu.Unlock()
	}
	created, err := s.Store.CreateFirstUser(r.Context(), store.User{Username: username, PasswordHash: hash, Role: auth.RoleOwner})
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !created {
		redirect(w, r, "/login")
		return
	}

	s.mu.Lock()
	s.setupToken = ""
	s.mu.Unlock()
	if s.OnSetupComplete != nil {
		s.OnSetupComplete()
	}
	u, err := s.Store.UserByUsername(r.Context(), username)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "setup", "created the first Owner account")
	if err := s.startSession(w, r, u, false); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/")
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.needsSetup() {
		redirect(w, r, "/setup")
		return
	}
	s.renderLogin(w, r, map[string]any{})
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
		s.renderLogin(w, r, map[string]any{"Error": "Wrong username or password.", "Username": username})
		return
	}
	if u.TOTPSecret != "" {
		// Say so now rather than after a code that can't work.
		if until := authkit.TOTPLockedUntil(u.TOTPFailures, u.TOTPFailedAt); time.Now().Before(until) {
			w.WriteHeader(http.StatusTooManyRequests)
			s.renderLogin(w, r, map[string]any{"Error": lockedMessage(until), "Username": username})
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
	redirect(w, r, "/")
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
	s.pending[originOf(r)+authkit.HashToken(token)] = &pendingLogin{userID: u.ID, expires: now.Add(pendingDuration), remember: r.FormValue("remember") == "on"}
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
	redirect(w, r, "/")
}

func (s *Server) twoFactorPage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	w.Header().Set("Cache-Control", "no-store") // the key being set up
	if u.TOTPSecret != "" {
		s.renderAccount(w, "twofactor", "2fa", map[string]any{"User": u, "Enabled": true})
		return
	}
	secret, err := authkit.NewTOTPSecret()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderAccount(w, "twofactor", "2fa", s.twoFactorData(u, secret, ""))
}

func (s *Server) twoFactorSubmit(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u.TOTPSecret != "" {
		redirect(w, r, "/account/2fa")
		return
	}
	// The new secret round-trips through the form. Tampering with it only
	// affects the user's own account, and the code must match it.
	secret := strings.TrimSpace(r.FormValue("secret"))
	step, ok := authkit.VerifyTOTP(secret, r.FormValue("code"), time.Now(), 0)
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusBadRequest)
		s.renderAccount(w, "twofactor", "2fa", s.twoFactorData(u, secret,
			"That code didn't work. Make sure you added the key above, then enter the current code."))
		return
	}
	if err := s.Store.SetTOTP(r.Context(), u.ID, secret, step); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "2fa enabled", "")
	redirect(w, r, "/")
}

func (s *Server) twoFactorData(u *store.User, secret, errMsg string) map[string]any {
	// Group the key in fours so it's easier to type into an app.
	var groups []string
	for i := 0; i < len(secret); i += 4 {
		groups = append(groups, secret[i:min(i+4, len(secret))])
	}
	url := authkit.TOTPURL("Roostack", u.Username, secret)
	code, err := qrSVG(url)
	if err != nil {
		s.Log.Error("drawing the 2FA QR code", "err", err) // the typed key still works
	}
	return map[string]any{
		"User":      u,
		"Secret":    secret,
		"SecretFmt": strings.Join(groups, " "),
		"URL":       template.URL(url),
		"QR":        code,
		"Required":  s.twoFactorRequired(u),
		"Owner":     u.Role == auth.RoleOwner,
		"Error":     errMsg,
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	s.endSession(w, r)
	redirect(w, r, "/login")
}

// endSession logs this browser out.
func (s *Server) endSession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(r.Context(), authkit.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
