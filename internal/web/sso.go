package web

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

// A service's Single sign-on card (SPEC.md §5.2; docs/mockups/sso): the
// switch, who can sign in, and the details. Owners and Admins turn it on
// (it changes a service); only Owners choose who can sign in.

// ssoRoutes are single sign-on's pages in Roostack's own UI: the card
// on a service's page, and groups and the sign-in page's look on People.
func (s *Server) ssoRoutes(mux *http.ServeMux) {
	mux.Handle("POST /services/{name}/sso", s.require(auth.ManageServices, s.ssoSubmit))
	mux.Handle("POST /services/{name}/sso/access", s.requireOwner(s.ssoAccessSubmit))
	mux.Handle("POST /settings/people/groups", s.requireOwner(s.groupAddSubmit))
	mux.Handle("POST /settings/people/groups/{id}", s.requireOwner(s.groupSaveSubmit))
	mux.Handle("POST /settings/people/groups/{id}/remove", s.requireOwner(s.groupRemoveSubmit))
	mux.Handle("POST /users/{id}/details", s.requireOwner(s.userDetailsSubmit))
	mux.Handle("POST /settings/people/sign-in-page", s.requireOwner(s.lookSubmit))
	mux.Handle("GET /settings/people/look/{part}", s.requireOwner(s.lookPreview))
}

// ssoCard is the card's data.
type ssoCard struct {
	Name, Title string
	// Available: the lab can turn it on now; Why says why not otherwise.
	Available bool
	Why       string
	On        bool
	// Wall: behind forward auth; otherwise an OIDC app.
	Wall bool
	Note string
	// AppAdmins: the app makes the lab's Owners and Admins its admins;
	// Existing is what it does with an account it already has (the
	// package's sso.oidc.admins and existing_accounts). The card claims
	// neither unless the package says so.
	AppAdmins bool
	Existing  string
	// SignInHost is the sign-in page's address, and SignInPublic whether
	// it's public (it follows the services and Roostack's pages).
	SignInHost   string
	SignInPublic bool
	// PagesPublic: it's public because Roostack's own pages are.
	PagesPublic bool
	// Who can sign in: everyone, or the groups and people chosen.
	Access   store.Access
	Groups   []ssoChoice
	People   []ssoChoice
	Admins   []string
	Everyone int // how many people everyone is
	// CanChoose: an Owner, who chooses who can sign in.
	CanChoose bool
	// Details.
	ClientID  string
	Redirects []string
	EnvNames  []string
	Bypass    []pkgspec.PrivatePath
	Error     string
	// Open: the dialog choosing who can sign in shows (a mistake in it,
	// or ?sso=who, its button without scripts).
	Open bool
	// HasHook: the app is set by its sso hooks (Forgejo, Immich), whose
	// last run is Hook (SPEC.md §5.2, "Connecting the remaining apps").
	HasHook bool
	Hook    manager.SSOHookStatus
	// Connect is the action that makes a key the hooks need, while it's
	// missing, and ConnectTitle its button.
	Connect, ConnectTitle string
	// CanManage: the viewer may run actions and Try again.
	CanManage bool
}

// ssoChoice is a group or person to tick.
type ssoChoice struct {
	ID      int64
	Name    string
	Detail  string
	Checked bool
}

// Summary says who can sign in, in a few words.
func (c ssoCard) Summary() string {
	if !c.Access.Chosen {
		return "Everyone in the lab"
	}
	var names []string
	for _, g := range c.Groups {
		if g.Checked {
			names = append(names, g.Name)
		}
	}
	for _, p := range c.People {
		if p.Checked {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return "Nobody yet"
	}
	return strings.Join(names, ", ")
}

// ssoCardFor is a service's card, or nil if its package doesn't take
// single sign-on (or there's no sign-in page on this server).
func (s *Server) ssoCardFor(r *http.Request, name string, p *pkgspec.Package) *ssoCard {
	if s.SSO == nil || s.SignIn == nil || p == nil || p.SSO == nil {
		return nil
	}
	ctx := r.Context()
	u := currentUser(r)
	c := &ssoCard{Name: name, Title: p.Title, Note: p.SSO.Note, Wall: p.SSO.ForwardAuth != nil, SignInHost: s.SignInHost,
		CanChoose: u != nil && u.Role == auth.RoleOwner, ClientID: name}
	if o := p.SSO.OIDC; o != nil {
		c.AppAdmins, c.Existing = o.Admins, o.ExistingAccounts
	}
	c.Available, c.Why = s.SSO.SSOAvailable(name)
	c.HasHook = len(p.HooksOn(pkgspec.HookSSO)) > 0
	c.CanManage = u != nil && auth.Can(u.Role, auth.ManageServices)
	for _, cl := range s.SSO.SSOClients() {
		if cl.ID == name {
			c.On, c.Redirects = true, cl.RedirectURIs
		}
	}
	// The sign-in page is public while any service using it is, or
	// Roostack's own pages are (they sign in there too).
	nc := s.network()
	c.PagesPublic = nc.Saved == config.NetworkInternet && !nc.UITunnelOnly
	c.SignInPublic = c.PagesPublic
	for _, cl := range s.SSO.SSOClients() {
		c.SignInPublic = c.SignInPublic || cl.Public
	}
	if p.SSO.ForwardAuth != nil {
		c.Bypass = p.SSO.ForwardAuth.BypassPaths
	}
	for _, env := range p.SSO.Env {
		c.EnvNames = append(c.EnvNames, slices.Collect(maps.Keys(env))...)
	}
	for _, sec := range p.SSO.Secrets {
		c.EnvNames = append(c.EnvNames, slices.Collect(maps.Keys(sec))...)
	}
	slices.Sort(c.EnvNames)
	c.Access, _ = s.Store.ServiceAccess(ctx, name)
	users, _ := s.Store.ListUsers(ctx)
	c.Everyone = len(users)
	byID := map[int64]string{}
	for _, pu := range users {
		byID[pu.ID] = pu.Username
		if pu.Role == auth.RoleOwner || pu.Role == auth.RoleAdmin {
			c.Admins = append(c.Admins, pu.Username)
		}
		c.People = append(c.People, ssoChoice{ID: pu.ID, Name: pu.Username, Detail: pu.Name,
			Checked: slices.Contains(c.Access.Users, pu.ID)})
	}
	groups, _ := s.Store.Groups(ctx)
	for _, g := range groups {
		var members []string
		for _, m := range g.Members {
			members = append(members, byID[m])
		}
		c.Groups = append(c.Groups, ssoChoice{ID: g.ID, Name: g.Name, Detail: strings.Join(members, ", "),
			Checked: slices.Contains(c.Access.Groups, g.ID)})
	}
	return c
}

// usableBy leaves out, for a Member, the services with single sign-on
// they may not use (SPEC.md §5.2): Owners and Admins see every service.
func (s *Server) usableBy(ctx context.Context, u *store.User, services []ServiceView) []ServiceView {
	if s.SSO == nil || u == nil || u.Role != auth.RoleMember {
		return services
	}
	on := map[string]bool{}
	for _, c := range s.SSO.SSOClients() {
		on[c.ID] = true
	}
	return slices.DeleteFunc(services, func(sv ServiceView) bool {
		if !on[sv.Name] {
			return false
		}
		ok, err := s.Store.CanUse(ctx, sv.Name, u.ID)
		return err == nil && !ok
	})
}

// ssoSubmit turns single sign-on on or off for a service.
func (s *Server) ssoSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if s.SSO == nil {
		http.NotFound(w, r)
		return
	}
	on := r.FormValue("sso") == "on"
	if on {
		// Who can sign in comes with turning it on.
		if err := s.saveAccess(r, name); err != nil {
			s.renderServiceWith(w, r, map[string]any{"SSOError": err.Error()}, true)
			return
		}
	}
	err := s.SSO.SetSSO(r.Context(), name, on)
	if msg, ok := usererr.Message(err); ok {
		s.renderServiceWith(w, r, map[string]any{"SSOError": msg}, true)
		return
	}
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	what := "turned off single sign-on for "
	if on {
		what = "turned on single sign-on for "
	}
	s.Store.Audit(r.Context(), currentUser(r), "single sign-on", what+name)
	redirect(w, r, "/services/"+name+"#sso")
}

// ssoAccessSubmit changes who can sign in to a service (Owners).
func (s *Server) ssoAccessSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.saveAccess(r, name); err != nil {
		s.renderServiceWith(w, r, map[string]any{"SSOError": err.Error()}, true)
		return
	}
	redirect(w, r, "/services/"+name+"#sso")
}

// saveAccess saves who can sign in from the card's form: everyone, or the
// groups and people ticked. Only an Owner chooses; anyone else's form
// leaves it as it is.
func (s *Server) saveAccess(r *http.Request, name string) error {
	u := currentUser(r)
	if u == nil || u.Role != auth.RoleOwner || r.FormValue("who") == "" {
		return nil
	}
	a := store.Access{Chosen: r.FormValue("who") == "chosen"}
	if a.Chosen {
		r.ParseForm()
		for _, v := range r.Form["group"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				a.Groups = append(a.Groups, id)
			}
		}
		for _, v := range r.Form["person"] {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil {
				a.Users = append(a.Users, id)
			}
		}
	}
	if err := s.Store.SetServiceAccess(r.Context(), name, a); err != nil {
		return err
	}
	detail := "everyone in the lab can sign in to " + name
	if a.Chosen {
		detail = "chose who can sign in to " + name
	}
	s.Store.Audit(r.Context(), u, "single sign-on", detail)
	return nil
}
