package web

import (
	"html/template"
	"net/http"
	"net/url"
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
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	labName := strings.TrimSpace(r.FormValue("lab_name"))
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "setup", s.setupData(map[string]any{"Choice": "new", "Error": msg, "Username": username, "Email": email,
			"LabName": labName}))
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
	case emailProblem(email) != "":
		// Every Owner has one (ownerNeedsEmail).
		fail(emailProblem(email))
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
	created, err := s.Store.CreateFirstUser(r.Context(), store.User{Username: username, PasswordHash: hash, Role: auth.RoleOwner, Email: email})
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

// twoFactorPage turns on two-factor: a page of the account, or for the
// Owner while the setup wizard lasts its first step (twoFactorInWizard),
// shown like its other steps.
func (s *Server) twoFactorPage(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if s.twoFactorElsewhere(w, r, u) {
		return
	}
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
	s.renderTwoFactor(w, r, s.twoFactorData(r, u, secret, ""))
}

// twoFactorElsewhere sends u to where they turn on two-factor if r isn't
// it (twoFactorPath), or once it's on, from the wizard's step on to the
// wizard's next. It reports whether it did.
func (s *Server) twoFactorElsewhere(w http.ResponseWriter, r *http.Request, u *store.User) bool {
	switch {
	case r.URL.Path == wizardTwoFactor && u.TOTPSecret != "":
		redirect(w, r, s.wizardNext(r))
	case u.TOTPSecret == "" && r.URL.Path != s.twoFactorPath(r, u):
		redirect(w, r, firstThen(s.twoFactorPath(r, u), r))
	default:
		return false
	}
	return true
}

// renderTwoFactor shows the two-factor page where r asked for it.
func (s *Server) renderTwoFactor(w http.ResponseWriter, r *http.Request, data map[string]any) {
	if r.URL.Path == wizardTwoFactor {
		data["Wizard"] = true
		s.render(w, "twofactor", data)
		return
	}
	s.renderAccount(w, "twofactor", "2fa", data)
}

func (s *Server) twoFactorSubmit(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	if s.twoFactorElsewhere(w, r, u) {
		return
	}
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
		s.renderTwoFactor(w, r, s.twoFactorData(r, u, secret,
			"That code didn't work. Make sure you added the key above, then enter the current code."))
		return
	}
	if err := s.Store.SetTOTP(r.Context(), u.ID, secret, step); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "2fa enabled", "")
	if r.URL.Path == wizardTwoFactor {
		redirect(w, r, s.wizardNext(r))
		return
	}
	redirect(w, r, nextOf(r))
}

func (s *Server) twoFactorData(r *http.Request, u *store.User, secret, errMsg string) map[string]any {
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
		"Action":    withQuery(r.URL.Path, nextQuery(r)),
	}
}

// nextQuery keeps r's ?next= for a form's action, so the step after it
// carries on to that page.
func nextQuery(r *http.Request) url.Values {
	if next := nextOf(r); next != "/" {
		return url.Values{"next": {next}}
	}
	return nil
}
