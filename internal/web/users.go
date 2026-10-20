package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/store"
)

// usersPage lists accounts for the Owner (SPEC.md §5).
func (s *Server) usersPage(w http.ResponseWriter, r *http.Request) {
	s.renderUsers(w, r, http.StatusOK, map[string]any{})
}

// renderUsers shows the users page; extra holds a message or a temporary
// password to show once.
func (s *Server) renderUsers(w http.ResponseWriter, r *http.Request, status int, extra map[string]any) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	data := map[string]any{"User": currentUser(r), "Users": users,
		"Roles": []string{auth.RoleOwner, auth.RoleAdmin, auth.RoleMember},
		// Signing out after inactivity (session.go).
		"IdleMinutes": int(s.idleLimit(r.Context()).Minutes()), "IdleChoices": idleChoices,
		"OfferRemember": s.offerRemember(r.Context()), "SignInSaved": r.URL.Query().Get("signin") == "saved",
		"LookSaved": r.URL.Query().Get("look") == "saved"}
	s.addPeopleSSO(r.Context(), data)
	for k, v := range extra {
		data[k] = v
	}
	// The add form's dialog opens with a mistake to fix, or from
	// ?add=someone (its button, without scripts).
	data["Adding"] = data["Adding"] == true || r.URL.Query().Get("add") == "someone"
	// ?person=<id> opens that person's dialog (its Change… without scripts).
	if _, set := data["OpenPerson"]; !set {
		if id, err := strconv.ParseInt(r.URL.Query().Get("person"), 10, 64); err == nil {
			data["OpenPerson"] = id
		}
	}
	// ?group=<id> opens that group's dialog (its Change… without scripts).
	if _, set := data["OpenGroup"]; !set {
		if id, err := strconv.ParseInt(r.URL.Query().Get("group"), 10, 64); err == nil {
			data["OpenGroup"] = id
		}
	}
	// A role that isn't one comes back as the first choice.
	if role, _ := data["NewRole"].(string); !auth.ValidRole(role) {
		data["NewRole"] = ""
	}
	// Temporary passwords must not be cached or shown again.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	s.render(w, "users", withArea(data, "people"))
}

func (s *Server) userAddSubmit(w http.ResponseWriter, r *http.Request) {
	name, role := strings.TrimSpace(r.FormValue("username")), r.FormValue("role")
	email := strings.TrimSpace(r.FormValue("email"))
	fail := func(msg string) {
		s.renderUsers(w, r, http.StatusBadRequest, map[string]any{"Error": msg, "NewName": name, "NewRole": role, "NewEmail": email, "Adding": true})
	}
	switch {
	case !auth.ValidUsername(name):
		fail("Choose a username of letters and digits (dots, dashes and underscores are fine too), up to 32 characters.")
		return
	case !auth.ValidRole(role):
		fail("Choose a role.")
		return
	}
	if email != "" {
		if a, err := mail.ParseAddress(email); err != nil || a.Address != email {
			fail(email + " isn't an email address.")
			return
		}
	}
	if _, err := s.Store.UserByUsername(r.Context(), name); err == nil {
		fail("There's already someone called " + name + ".")
		return
	}
	password, hash, err := newTemporaryPassword()
	if err != nil {
		s.serverError(w, err)
		return
	}
	id, err := s.Store.CreateUser(r.Context(), store.User{Username: name, PasswordHash: hash, Role: role,
		MustChangePassword: true, Email: email})
	if err != nil {
		s.serverError(w, err)
		return
	}
	if groups := formIDs(r, "group"); len(groups) > 0 {
		if err := s.Store.SetUserGroups(r.Context(), id, groups); err != nil {
			s.serverError(w, err)
			return
		}
	}
	s.Store.Audit(r.Context(), currentUser(r), "user add", fmt.Sprintf("%s (%s)", name, role))
	s.renderUsers(w, r, http.StatusOK, map[string]any{"TempFor": name, "TempPassword": password, "TempNew": true})
}

// targetUser loads the user in the URL; the Owner can't use these actions
// on themselves (their own password and 2FA are on the Account page, and
// changing their own role or removing themselves could lock them out).
func (s *Server) targetUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, false
	}
	u, err := s.Store.UserByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, err)
		return nil, false
	}
	if u.ID == currentUser(r).ID {
		s.renderUsers(w, r, http.StatusBadRequest, map[string]any{
			"Error": "You can't change your own account here. Use the Account page for your password."})
		return nil, false
	}
	return u, true
}

func (s *Server) userRoleSubmit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	role := r.FormValue("role")
	if !auth.ValidRole(role) {
		s.renderUsers(w, r, http.StatusBadRequest, map[string]any{"Error": "Choose a role."})
		return
	}
	if err := s.Store.SetRole(r.Context(), u.ID, role); err != nil {
		s.userError(w, r, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "user role", fmt.Sprintf("%s: %s → %s", u.Username, u.Role, role))
	redirect(w, r, "/settings/people")
}

func (s *Server) userResetPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	password, hash, err := newTemporaryPassword()
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.Username, hash, true); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "password reset", u.Username)
	s.renderUsers(w, r, http.StatusOK, map[string]any{"TempFor": u.Username, "TempPassword": password})
}

func (s *Server) userReset2FASubmit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if err := s.Store.ResetTOTP(r.Context(), u.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "2FA reset", u.Username)
	msg := "Two-factor authentication is off for " + u.Username + ", and they've been logged out."
	if u.Role == auth.RoleOwner {
		msg += " They'll set it up again when they next log in."
	}
	s.renderUsers(w, r, http.StatusOK, map[string]any{"Done": msg})
}

func (s *Server) userRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	u, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteUser(r.Context(), u.ID); err != nil {
		s.userError(w, r, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "user remove", u.Username)
	s.renderUsers(w, r, http.StatusOK, map[string]any{"Done": u.Username + " has been removed."})
}

func (s *Server) userError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrLastOwner) {
		s.renderUsers(w, r, http.StatusBadRequest, map[string]any{
			"Error": "The lab needs at least one Owner. Make someone else an Owner first."})
		return
	}
	s.serverError(w, err)
}

func newTemporaryPassword() (password, hash string, err error) {
	if password, err = auth.NewTemporaryPassword(); err != nil {
		return "", "", err
	}
	hash, err = authkit.HashPassword(password)
	return password, hash, err
}
