package web

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/store"
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
	s.render(w, "setup", map[string]any{"CanRestore": s.LabRestore != nil})
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
		s.render(w, "setup", map[string]any{"Error": msg, "Username": username, "LabName": labName, "CanRestore": s.LabRestore != nil})
	}
	if s.LabRestore != nil && s.LabRestore.Running() {
		fail("A lab is being restored onto this server.")
		return
	}

	s.mu.Lock()
	tokenOK := auth.TokensEqual(strings.TrimSpace(r.FormValue("token")), s.setupToken)
	s.mu.Unlock()
	switch {
	case !tokenOK:
		time.Sleep(failedLoginDelay)
		fail("The setup code is wrong. It is shown in the MyLab log and in /var/lib/mylab/setup-token.")
		return
	case !auth.ValidUsername(username):
		fail("Choose a username of letters and digits (dots, dashes and underscores are fine too), up to 32 characters.")
		return
	case password != r.FormValue("confirm"):
		fail("The passwords don't match.")
		return
	}
	if err := auth.CheckPasswordPolicy(password); err != nil {
		fail(capitalize(err.Error()) + ".")
		return
	}
	hash, err := auth.HashPassword(password)
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
	if err := s.startSession(w, r, u); err != nil {
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
	s.render(w, "login", map[string]any{})
}

func (s *Server) loginSubmit(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	u, err := s.Store.UserByUsername(r.Context(), username)
	ok := false
	if err == nil {
		ok, err = auth.VerifyPassword(u.PasswordHash, r.FormValue("password"))
		if err != nil {
			s.Log.Error("verify password", "user", username, "err", err)
		}
	}
	if !ok {
		s.Log.Warn("failed login", "user", username, "remote", r.RemoteAddr)
		time.Sleep(failedLoginDelay)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "login", map[string]any{"Error": "Wrong username or password.", "Username": username})
		return
	}
	if u.TOTPSecret != "" {
		s.startPending(w, r, u)
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "login", "")
	redirect(w, r, "/")
}

// startPending records a login that passed the password step and asks for
// the 2FA code.
func (s *Server) startPending(w http.ResponseWriter, r *http.Request, u *store.User) {
	token, err := auth.NewToken()
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
	s.pending[auth.HashToken(token)] = &pendingLogin{userID: u.ID, expires: now.Add(pendingDuration)}
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
	key := auth.HashToken(c.Value)
	s.mu.Lock()
	p := s.pending[key]
	if p != nil && time.Now().After(p.expires) {
		delete(s.pending, key)
		p = nil
	}
	s.mu.Unlock()
	if p == nil {
		s.render(w, "login", map[string]any{"Error": "That took too long. Please log in again."})
		return
	}

	u, err := s.Store.UserByID(r.Context(), p.userID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	step, ok := auth.VerifyTOTP(u.TOTPSecret, r.FormValue("code"), time.Now(), u.TOTPLastStep)
	if ok {
		// Atomic check, so the same code can't complete two logins.
		ok, err = s.Store.UseTOTPStep(r.Context(), u.ID, step)
		if err != nil {
			s.serverError(w, err)
			return
		}
	}
	if !ok {
		s.Log.Warn("failed 2FA code", "user", u.Username, "remote", r.RemoteAddr)
		time.Sleep(failedLoginDelay)
		s.mu.Lock()
		p.attempts++
		tooMany := p.attempts >= maxTOTPAttempts
		if tooMany {
			delete(s.pending, key)
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		if tooMany {
			s.render(w, "login", map[string]any{"Error": "Too many wrong codes. Please log in again."})
			return
		}
		s.render(w, "totp", map[string]any{"Error": "That code didn't work. Check your authenticator app and try again."})
		return
	}

	s.mu.Lock()
	delete(s.pending, key)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: pendingCookie, Value: "", Path: "/login", MaxAge: -1, HttpOnly: true})
	if err := s.startSession(w, r, u); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "login", "with two-factor code")
	redirect(w, r, "/")
}

func (s *Server) twoFactorPage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if u.TOTPSecret != "" {
		s.render(w, "twofactor", map[string]any{"User": u, "Enabled": true})
		return
	}
	secret, err := auth.NewTOTPSecret()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "twofactor", s.twoFactorData(u, secret, ""))
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
	step, ok := auth.VerifyTOTP(secret, r.FormValue("code"), time.Now(), 0)
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "twofactor", s.twoFactorData(u, secret,
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
	url := auth.TOTPURL("MyLab", u.Username, secret)
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
		"Required":  u.Role == auth.RoleOwner,
		"Error":     errMsg,
	}
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	redirect(w, r, "/login")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
