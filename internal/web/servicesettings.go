package web

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/usererr"
)

// serviceLink is a link into the app's own settings pages.
type serviceLink struct {
	Title, Description, URL string
	// Private says who can open it when not everyone can: a page the
	// package keeps private, or the whole address (SPEC.md §6.11, §6.12).
	// Route is its address's route, for the link to change that.
	Private, Route string
}

// revealable is a secret the page can show on request.
type revealable struct {
	Key, Title, Description string
}

// linkPrivate says who can open a link into the app, if not everyone
// could: on a lab on the internet only through an SSH tunnel, at home
// only from the home network (said only when the rest of the address is
// public, since otherwise the whole service is home-only).
func linkPrivate(access []manager.RouteAccess, l pkgspec.Link, internet bool) string {
	for _, ra := range access {
		if ra.Route != l.Route {
			continue
		}
		closed := !ra.Public
		for _, pp := range ra.PrivatePaths {
			if !pp.Open && fsutil.Within(l.Path, pp.Path) {
				closed = true
			}
		}
		switch {
		case closed && internet:
			return "Only through an SSH tunnel"
		case closed && ra.Public:
			return "From your home network only"
		}
	}
	return ""
}

// addSettingsData adds the service page's Settings section: links into the
// app, secrets that can be shown, and the form for Roostack's own settings.
// submitted (form values after an error) overrides the current values.
func (s *Server) addSettingsData(data map[string]any, name string, p *pkgspec.Package, submitted map[string][]string) {
	values, sub, err := s.Manager.CurrentSettings(name)
	if err != nil {
		s.Log.Error("reading service settings", "service", name, "err", err)
		return
	}
	var links []serviceLink
	if svc, ok := data["Service"].(ServiceView); ok {
		access, _ := s.Manager.Access(name)
		internet := s.onInternet()
		for _, l := range p.Links {
			if base := routeURL(svc, l.Route, p); base != "" {
				links = append(links, serviceLink{Title: l.Title, Description: l.Description, URL: base + l.Path,
					Private: linkPrivate(access, l, internet), Route: l.Route})
			}
		}
	}
	var fields []formField
	var secrets []revealable
	for _, st := range p.Settings {
		if st.Type == pkgspec.SettingSecret && st.Reveal {
			secrets = append(secrets, revealable{Key: st.Key, Title: st.Title, Description: st.Description})
		}
		if !manager.Changeable(st) {
			continue
		}
		f := formField{Key: "setting." + st.Key, Title: st.Title, Description: st.Description,
			Required: st.Required, MinLength: st.MinLength}
		if f.Title == "" {
			f.Title = st.Key
		}
		v := values[st.Key]
		if submitted != nil {
			raw, given := submitted[f.Key]
			v = given
			if st.Type != pkgspec.SettingBool && given && len(raw) > 0 {
				v = raw[0]
			} else if st.Type != pkgspec.SettingBool {
				v = ""
			}
		}
		switch st.Type {
		case pkgspec.SettingBool:
			f.Kind, f.Checked = "checkbox", v == true
		case pkgspec.SettingInt:
			f.Kind, f.Value = "number", fmt.Sprint(v)
		default:
			f.Kind, f.Value = "text", fmt.Sprint(v)
		}
		fields = append(fields, f)
	}
	if submitted != nil {
		if raw := submitted["subdomain"]; len(raw) > 0 {
			sub = raw[0]
		}
	}
	data["Links"], data["Secrets"], data["SettingFields"] = links, secrets, fields
	data["Subdomain"], data["HasRoute"], data["Domain"] = sub, manager.MainRoute(p) != "", s.Domain
}

// routeURL is a route's address: the service's first URL for the main
// route (the one shown on the page).
func routeURL(svc ServiceView, route string, p *pkgspec.Package) string {
	if route == manager.MainRoute(p) && len(svc.URLs) > 0 {
		return strings.TrimSuffix(svc.URLs[0], "/")
	}
	return ""
}

func (s *Server) settingsSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form.", http.StatusBadRequest)
		return
	}
	req := manager.SettingsRequest{Subdomain: r.PostForm.Get("subdomain"), Settings: map[string]string{}}
	for k, v := range r.PostForm {
		if key, ok := strings.CutPrefix(k, "setting."); ok && len(v) > 0 {
			req.Settings[key] = v[0]
		}
	}
	// One form (service-settings.html): its address and settings, if it
	// has any, then how it updates and the libraries it reads. A mistake
	// in the settings saves nothing.
	var err error
	if r.PostForm.Get("settings_shown") != "" || r.PostForm.Has("subdomain") || len(req.Settings) > 0 {
		err = s.Manager.UpdateSettings(r.Context(), name, req)
	}
	if policy := r.PostForm.Get("updates"); err == nil && policy != "" {
		if !slices.Contains(updatePolicies, policy) {
			http.Error(w, "unknown update policy", http.StatusBadRequest)
			return
		}
		if policy != s.Manager.UpdatePolicy(name) {
			if err = s.Manager.SetUpdatePolicy(r.Context(), name, policy); err == nil {
				s.Store.Audit(r.Context(), currentUser(r), "services", name+" updates: "+policy)
			}
		}
	}
	if err == nil && s.Libraries != nil && r.PostForm.Get("libraries_chosen") != "" {
		chosen := r.PostForm["library"]
		// Where each is inside it, if a place of its own (SPEC.md §8.4).
		var paths map[string]string
		if r.PostForm.Get("library_paths_shown") != "" {
			paths = map[string]string{}
			for k, v := range r.PostForm {
				if l, ok := strings.CutPrefix(k, "library_path."); ok && strings.TrimSpace(v[0]) != "" {
					paths[l] = strings.TrimSpace(v[0])
				}
			}
		}
		c, cerr := s.Libraries.ServiceLibraries(r.Context(), name)
		if cerr != nil || !sameSet(c.Chosen, chosen) || (paths != nil && !maps.Equal(paths, c.Paths) && (len(paths) > 0 || len(c.Paths) > 0)) {
			err = s.Libraries.SetServiceLibraries(r.Context(), name, chosen, paths)
		}
	}
	msg, isForm := usererr.Message(err)
	switch {
	case errors.Is(err, manager.ErrNotFound):
		http.NotFound(w, r)
		return
	case isForm:
		s.renderServiceWith(w, r, map[string]any{"SettingsError": msg, "Submitted": r.PostForm, "EditSettings": true}, true)
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "settings", name)
	redirect(w, r, "/services/"+name)
}

func (s *Server) revealSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil {
		http.NotFound(w, r)
		return
	}
	name, key := r.PathValue("name"), r.PathValue("key")
	v, err := n.RevealSecret(r.Context(), name, key)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "secret shown", name+" "+key)
	w.Header().Set("Cache-Control", "no-store")
	s.renderServiceWith(w, r, map[string]any{"Revealed": map[string]string{key: v}}, false)
}

// sameSet says whether a and b hold the same strings, in any order.
func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
