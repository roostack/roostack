package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/store"
)

// People's single sign-on parts (SPEC.md §5.2; docs/mockups/sso): each
// person's name, email and groups; the groups and the services each can
// use; the sign-in page's look; and its two sign-in choices.

// groupView is a group on the People page.
type groupView struct {
	store.Group
	// Services are the services with single sign-on chosen for it.
	Services []string
	// ServiceTitles says which, in words.
	ServiceTitles string
}

// Has reports whether a person is in the group.
func (g groupView) Has(id int64) bool { return slices.Contains(g.Members, id) }

// Uses reports whether the group was chosen for a service.
func (g groupView) Uses(service string) bool { return slices.Contains(g.Services, service) }

// ssoServiceTick is a service with single sign-on, to tick for a group.
type ssoServiceTick struct {
	Name, Title, Logo string
	// Everyone: everyone in the lab can use it, so there's nothing to choose.
	Everyone bool
}

// addPeopleSSO adds what People shows of single sign-on, when the lab has
// the sign-in page.
func (s *Server) addPeopleSSO(ctx context.Context, data map[string]any) {
	if s.SignIn == nil || s.SSO == nil {
		return
	}
	data["SSO"] = true
	data["SignInHost"] = s.SignInHost
	if s.SignInHost != "" {
		data["SignInURL"] = "https://" + s.SignInHost
	}
	data["LabName"] = s.labName()
	data["Look"] = s.signInLook(ctx)
	data["Themes"] = Themes
	data["SignInRemember"] = s.signInRememberDefault(ctx)
	v, _ := s.Store.Setting(ctx, settingSignInTwoFactor)
	data["SignInTwoFactor"] = v == "everyone"

	var ticks []ssoServiceTick
	chosen := map[int64][]string{} // group → services chosen for it
	titles := map[string]string{}
	for _, c := range s.SSO.SSOClients() {
		a, err := s.Store.ServiceAccess(ctx, c.ID)
		if err != nil {
			continue
		}
		t := ssoServiceTick{Name: c.ID, Title: c.Title, Everyone: !a.Chosen}
		if p, err := s.Manager.ServicePackage(c.ID); err == nil && p.About.Icon != "" {
			t.Logo = "/install/" + p.Name + "/icon.png"
		}
		ticks = append(ticks, t)
		titles[c.ID] = c.Title
		for _, g := range a.Groups {
			chosen[g] = append(chosen[g], c.ID)
		}
	}
	data["SSOServices"] = ticks

	groups, _ := s.Store.Groups(ctx)
	byUser := map[int64][]string{}
	var views []groupView
	for _, g := range groups {
		v := groupView{Group: g, Services: chosen[g.ID]}
		var names []string
		for _, sv := range v.Services {
			names = append(names, titles[sv])
		}
		switch {
		case len(names) > 0 && slices.ContainsFunc(ticks, func(t ssoServiceTick) bool { return t.Everyone }):
			v.ServiceTitles = joinWords(names) + ", and the services everyone can use"
		case len(names) > 0:
			v.ServiceTitles = joinWords(names)
		case len(ticks) > 0:
			v.ServiceTitles = "the services everyone can use"
		}
		views = append(views, v)
		for _, m := range g.Members {
			byUser[m] = append(byUser[m], g.Name)
		}
	}
	data["Groups"] = views
	data["UserGroups"] = byUser
}

// joinWords is "a", "a and b", "a, b and c".
func joinWords(w []string) string {
	switch len(w) {
	case 0:
		return ""
	case 1:
		return w[0]
	}
	return strings.Join(w[:len(w)-1], ", ") + " and " + w[len(w)-1]
}

// formIDs are a form's ticked IDs.
func formIDs(r *http.Request, key string) []int64 {
	r.ParseForm()
	var ids []int64
	for _, v := range r.Form[key] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// groupName checks a group's name: a few words.
func groupName(r *http.Request) (string, error) {
	name := strings.Join(strings.Fields(r.FormValue("name")), " ")
	if name == "" || len(name) > 60 || strings.ContainsAny(name, "<>\"") {
		return "", errors.New("Give the group a name of up to 60 characters, such as Family.")
	}
	return name, nil
}

func (s *Server) groupAddSubmit(w http.ResponseWriter, r *http.Request) {
	name, err := groupName(r)
	if err == nil {
		var id int64
		if id, err = s.Store.CreateGroup(r.Context(), name); err == nil {
			s.Store.Audit(r.Context(), currentUser(r), "group add", name)
			s.renderUsers(w, r, http.StatusOK, map[string]any{"OpenGroup": id})
			return
		}
	}
	if errors.Is(err, store.ErrGroupExists) {
		err = errors.New("There's already a group called " + name + ".")
	}
	s.groupError(w, r, err)
}

func (s *Server) groupError(w http.ResponseWriter, r *http.Request, err error) {
	if strings.HasSuffix(err.Error(), ".") && !strings.Contains(err.Error(), ": ") {
		s.renderUsers(w, r, http.StatusBadRequest, map[string]any{"GroupError": err.Error()})
		return
	}
	s.serverError(w, err)
}

// targetGroup is the group in the URL.
func (s *Server) targetGroup(w http.ResponseWriter, r *http.Request) (store.Group, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err == nil {
		groups, err := s.Store.Groups(r.Context())
		if err != nil {
			s.serverError(w, err)
			return store.Group{}, false
		}
		for _, g := range groups {
			if g.ID == id {
				return g, true
			}
		}
	}
	http.NotFound(w, r)
	return store.Group{}, false
}

// groupSaveSubmit saves a group's name, members, and the services chosen
// for it.
func (s *Server) groupSaveSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, ok := s.targetGroup(w, r)
	if !ok {
		return
	}
	name, err := groupName(r)
	if err != nil {
		s.groupError(w, r, err)
		return
	}
	if name != g.Name {
		if err := s.Store.RenameGroup(ctx, g.ID, name); errors.Is(err, store.ErrGroupExists) {
			s.groupError(w, r, errors.New("There's already a group called "+name+"."))
			return
		} else if err != nil {
			s.serverError(w, err)
			return
		}
	}
	if err := s.Store.SetGroupMembers(ctx, g.ID, formIDs(r, "member")); err != nil {
		s.serverError(w, err)
		return
	}
	// The services chosen for it: only those whose access is chosen (one
	// everyone can use stays so).
	if s.SSO != nil {
		ticked := r.Form["service"]
		for _, c := range s.SSO.SSOClients() {
			a, err := s.Store.ServiceAccess(ctx, c.ID)
			if err != nil || !a.Chosen {
				continue
			}
			has, want := slices.Contains(a.Groups, g.ID), slices.Contains(ticked, c.ID)
			if has == want {
				continue
			}
			if want {
				a.Groups = append(a.Groups, g.ID)
			} else {
				a.Groups = slices.DeleteFunc(a.Groups, func(id int64) bool { return id == g.ID })
			}
			if err := s.Store.SetServiceAccess(ctx, c.ID, a); err != nil {
				s.serverError(w, err)
				return
			}
		}
	}
	s.Store.Audit(ctx, currentUser(r), "group change", name)
	redirect(w, r, "/settings/people#groups")
}

func (s *Server) groupRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	g, ok := s.targetGroup(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteGroup(r.Context(), g.ID); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "group remove", g.Name)
	s.renderUsers(w, r, http.StatusOK, map[string]any{"Done": "The group " + g.Name + " has been deleted."})
}

// userDetailsSubmit saves a person's full name, email and groups (the
// Owner's own too).
func (s *Server) userDetailsSubmit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	u, err := s.Store.UserByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	name := strings.Join(strings.Fields(r.FormValue("name")), " ")
	email := strings.TrimSpace(r.FormValue("email"))
	if len(name) > 100 {
		s.renderUsers(w, r, http.StatusBadRequest, map[string]any{"Error": "A full name can be up to 100 characters.", "OpenPerson": u.ID})
		return
	}
	if email != "" {
		if msg := emailProblem(email); msg != "" {
			s.renderUsers(w, r, http.StatusBadRequest, map[string]any{"Error": msg, "OpenPerson": u.ID})
			return
		}
	}
	if err := s.Store.SetName(ctx, u.ID, name); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetEmail(ctx, u.ID, email); err != nil {
		s.serverError(w, err)
		return
	}
	if s.SignIn != nil {
		if err := s.Store.SetUserGroups(ctx, u.ID, formIDs(r, "group")); err != nil {
			s.serverError(w, err)
			return
		}
	}
	s.Store.Audit(ctx, currentUser(r), "user details", u.Username)
	s.renderUsers(w, r, http.StatusOK, map[string]any{"Done": "Saved " + u.Username + "'s details."})
}

// lookSubmit saves the sign-in page's look.
func (s *Server) lookSubmit(w http.ResponseWriter, r *http.Request) {
	if s.SignIn == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4*maxLogo+64<<10)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		s.renderUsers(w, r, http.StatusBadRequest, map[string]any{"LookError": "Those files are too big: each logo can be at most 200 KB."})
		return
	}
	if err := s.saveLook(r); err != nil {
		if strings.HasSuffix(err.Error(), ".") && !strings.Contains(err.Error(), ": ") {
			s.renderUsers(w, r, http.StatusBadRequest, map[string]any{"LookError": err.Error()})
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "settings", "the sign-in page's look")
	redirect(w, r, "/settings/people?look=saved#sign-in-page")
}

// lookPreview shows the logo's files on People (the UI's own address).
func (s *Server) lookPreview(w http.ResponseWriter, r *http.Request) {
	part := r.PathValue("part")
	kind := s.signInLook(r.Context()).Files[part]
	if kind == "" {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(s.lookPath(part, kind))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeImage(w, kind, data)
}

// saveSignInChoices saves the sign-in page's two choices from Signing in.
func (s *Server) saveSignInChoices(r *http.Request) string {
	if r.FormValue("sso") == "" {
		return ""
	}
	remember, twoFactor := "on", ""
	if r.FormValue("signin_remember") != "on" {
		remember = "off"
	}
	if r.FormValue("signin_two_factor") == "on" {
		twoFactor = "everyone"
	}
	s.Store.SetSetting(r.Context(), settingSignInRemember, remember)
	s.Store.SetSetting(r.Context(), settingSignInTwoFactor, twoFactor)
	return fmt.Sprintf("; sign-in page: keep signed in %s, two-factor for %s", remember, map[bool]string{true: "everyone", false: "those who need it"}[twoFactor != ""])
}
