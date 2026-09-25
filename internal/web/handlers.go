package web

import (
	"net/http"
	"strings"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/store"
)

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	if !s.needsSetup() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "setup", map[string]any{})
}

func (s *Server) setupSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.needsSetup() {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "setup", map[string]any{"Error": msg, "Username": username})
	}

	s.mu.Lock()
	tokenOK := auth.TokensEqual(strings.TrimSpace(r.FormValue("token")), s.setupToken)
	s.mu.Unlock()
	switch {
	case !tokenOK:
		time.Sleep(failedLoginDelay)
		fail("The setup code is wrong. It is shown in the MyLab log and in /var/lib/mylab/setup-token.")
		return
	case username == "":
		fail("Choose a username.")
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
	created, err := s.Store.CreateFirstUser(r.Context(), store.User{Username: username, PasswordHash: hash, Role: auth.RoleOwner})
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !created {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
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
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	if s.needsSetup() {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
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
	if err := s.startSession(w, r, u); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "login", "")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(r.Context(), auth.HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	services, err := s.Services(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	u := currentUser(r)
	s.render(w, "dashboard", map[string]any{
		"User":      u,
		"Services":  services,
		"CanManage": auth.Can(u.Role, auth.ManageServices),
	})
}

// servicesPartial re-renders just the service list; htmx polls it.
func (s *Server) servicesPartial(w http.ResponseWriter, r *http.Request) {
	services, err := s.Services(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages["dashboard"].ExecuteTemplate(w, "services", map[string]any{"Services": services}); err != nil {
		s.Log.Error("render partial", "err", err)
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
