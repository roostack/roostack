package web

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
)

// A service's page, and its settings page.

func (s *Server) servicePage(w http.ResponseWriter, r *http.Request) {
	s.renderServiceWith(w, r, nil, false)
}

// serviceSettingsPage is where a service's settings change (its page
// shows them as a summary): web address and package settings, updates,
// its name.
func (s *Server) serviceSettingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderServiceWith(w, r, map[string]any{"EditSettings": true}, false)
}

// renderService shows a service's page; portErr is a form error for one of
// its ports ({"port": name, "msg": text}).
func (s *Server) renderService(w http.ResponseWriter, r *http.Request, portErr map[string]string) {
	s.renderServiceWith(w, r, map[string]any{"PortError": portErr}, portErr != nil)
}

// serviceAction is an action button on the service page.
type serviceAction struct {
	Name, Title, Description string
	Fields                   []formField
}

// renderServiceWith shows a service's page with extra data (form errors,
// an action's result), or its settings page with "EditSettings"; failed
// sets a 400 status.
func (s *Server) renderServiceWith(w http.ResponseWriter, r *http.Request, extra map[string]any, failed bool) {
	data, ok := s.pageDataWithServices(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	for _, svc := range data["Services"].([]ServiceView) {
		if svc.Name == name {
			data["Service"] = svc
			if ms, err := s.Manager.Servers(); err == nil && len(ms) > 1 {
				data["MultiServer"] = true
			}
		}
	}
	if data["Service"] == nil {
		http.NotFound(w, r)
		return
	}
	// Its public access, if it has any, and whether that's paused (SPEC.md
	// §6.16).
	if pp, err := s.Manager.PublicPauses(); err == nil {
		for _, ps := range pp.Services {
			if ps.Name == name {
				data["PublicService"] = ps
				var titles []string
				for _, b := range ps.ByBuiltWith {
					titles = append(titles, pp.Title(b))
				}
				data["PausedByTitles"] = titles
			}
		}
	}
	data["BuiltWith"] = s.Manager.BuiltWith(name)
	// The registry's security advisories affecting it (SPEC.md §6.16).
	data["Advisories"] = s.Manager.Advisories(r.Context())[name]
	// Installing: its log, for the step list's Details. Once it's done, the
	// step list's polling reloads the page (status, Open link).
	if install := data["Service"].(ServiceView).Install; install != nil {
		if a := s.activityFor(r, install.Log); a != nil {
			data["InstallActivity"] = a
		}
	} else if r.URL.Query().Get("installing") == "1" && r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
	}
	if p, err := s.Manager.ServicePackage(name); err == nil {
		data["Package"] = p
		// Who makes it, for the About card (the logo is the page's own).
		if c := newProjectCredit(p); c != nil {
			c.IconURL = ""
			data["Credit"] = c
		}
		var actions []serviceAction
		for _, h := range p.HooksOn(pkgspec.HookAction) {
			actions = append(actions, serviceAction{Name: h.Name, Title: h.Title, Description: h.Description, Fields: inputFields(h)})
		}
		data["Actions"] = actions
		// ?action=connect opens that action: single sign-on's Connect
		// prompt links to it.
		data["OpenAction"] = r.URL.Query().Get("action")
		s.addSettingsData(data, name, p, nil)
		if access, err := s.Manager.Access(name); err == nil {
			data["Access"] = access
			// Its addresses wait for its setup (SPEC.md §8.1).
			data["PublicAfterSetup"] = slices.ContainsFunc(access, func(a manager.RouteAccess) bool { return a.AfterSetup })
			// Kept private because Roostack Cloud doesn't allow its public
			// access through roost.ac (SPEC.md §6.11).
			if s.CloudLimit != nil {
				if c := s.CloudLimit(name); c != nil && c.State != "" {
					data["CloudKept"] = c
				}
			}
			if v := s.wafCard(r.Context(), access); v != nil {
				data["WAF"] = v
			}
		}
		// Migrating photos into Immich (SPEC.md §8.5): the call to
		// action at the top and the card.
		if c := s.photoCardFor(r.Context(), currentUser(r), name); c != nil {
			c.Saved = photoSaved[r.URL.Query().Get("photos")]
			data["PhotoCard"] = c
		}
		// The registry withdrew its version (SPEC.md §8.3.4).
		if wd := s.Manager.Withdrawals(r.Context())[name]; wd != nil {
			data["Withdrawal"] = wd
		}
		// Held after a restore that didn't finish (SPEC.md §6.9).
		if v := s.heldViewOf(data["Service"].(ServiceView)); v != nil {
			data["Held"] = v
		}
		// A newer version (SPEC.md §7).
		s.addUpdateData(r, data, name)
		if data["UpdateDone"] == true {
			w.Header().Set("HX-Refresh", "true")
		}
	}
	if n := s.serverOf(r); n != nil {
		st := n.InstallStatus(r.Context(), name)
		data["Setup"] = st
		if st.Failed != "" && auth.Can(currentUser(r).Role, auth.ManageServices) {
			if p, ok := data["Package"].(*pkgspec.Package); ok {
				data["InstallFields"] = s.installFields(name, p)
			}
		}
	}
	// The latest setup log, if it's this service's.
	if a := s.activityFor(r, activity.Setup); a != nil && a.Title == "Setting up "+data["Service"].(ServiceView).Title {
		data["SetupActivity"] = a
	}
	data["DataDirs"] = s.Manager.DataDirs(name)
	if files, err := s.Manager.ServiceFiles(name); err == nil {
		data["Files"] = files
	}
	ports, err := s.Manager.Ports(name)
	if err != nil && !errors.Is(err, manager.ErrNotFound) {
		s.serverError(w, err)
		return
	}
	data["Ports"] = ports
	if urls := data["Service"].(ServiceView).URLs; len(urls) > 0 {
		data["Host"] = strings.TrimPrefix(urls[0], "https://")
	}
	for k, v := range extra {
		data[k] = v
	}
	if sub, ok := extra["Submitted"].(url.Values); ok {
		if p, ok := data["Package"].(*pkgspec.Package); ok {
			s.addSettingsData(data, r.PathValue("name"), p, sub)
		}
	}
	if failed {
		w.WriteHeader(http.StatusBadRequest)
	}
	if o := s.oldAddress(); o != nil {
		data["Old"] = o
	}
	page := "service"
	if u := s.serviceUptime(r.Context(), name); u != nil && data["CanManage"] == true {
		data["Uptime"] = u
	}
	if p, ok := data["Package"].(*pkgspec.Package); ok {
		if c := s.ssoCardFor(r, name, p); c != nil {
			if msg, ok := data["SSOError"].(string); ok {
				c.Error = msg
			}
			c.Open = c.On && c.CanChoose && (c.Error != "" || r.URL.Query().Get("sso") == "who")
			if st, ok := data["Setup"].(manager.HookStatus); ok {
				c.Hook = st.SSO
				if a := p.Action(st.SSO.NeedsKey); a != nil {
					c.Connect, c.ConnectTitle = a.Name, a.Title
				}
			}
			data["SSO"] = c
		}
	}
	if s.Libraries != nil {
		if c, err := s.Libraries.ServiceLibraries(r.Context(), name); err == nil && c.Reads {
			data["Libraries"] = c
		}
	}
	// Bringing its data in, under way (SPEC.md §8.4): the Owner's.
	if data["IsOwner"] == true {
		if x, err := s.Manager.ImportsOf(name); err == nil {
			if v, err := x.Import(r.Context(), name); err == nil && v.Source != nil && v.Switched.IsZero() {
				data["ImportUnderway"] = importUnderway(v)
			}
		}
	}
	if data["EditSettings"] == true {
		page = "service-settings"
		// A form that came back keeps what was chosen.
		if sub, ok := data["Submitted"].(url.Values); ok {
			if c, ok := data["Libraries"].(manager.ServiceLibraryChoice); ok && sub.Get("libraries_chosen") != "" {
				c.Chosen = sub["library"]
				data["Libraries"] = c
			}
			if p := sub.Get("updates"); slices.Contains(updatePolicies, p) {
				data["UpdatePolicy"] = p
			}
		}
	}
	s.render(w, page, data)
}
