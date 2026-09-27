package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mylab/mylab/internal/manager"
	"github.com/mylab/mylab/internal/pkgspec"
)

// serviceLink is a link into the app's own settings pages.
type serviceLink struct {
	Title, Description, URL string
}

// revealable is a secret the page can show on request.
type revealable struct {
	Key, Title, Description string
}

// addSettingsData adds the service page's Settings section: links into the
// app, secrets that can be shown, and the form for MyLab's own settings.
// submitted (form values after an error) overrides the current values.
func (s *Server) addSettingsData(data map[string]any, name string, p *pkgspec.Package, submitted map[string][]string) {
	values, sub, err := s.Manager.CurrentSettings(name)
	if err != nil {
		s.Log.Error("reading service settings", "service", name, "err", err)
		return
	}
	var links []serviceLink
	if svc, ok := data["Service"].(ServiceView); ok {
		for _, l := range p.Links {
			if base := routeURL(svc, l.Route, p); base != "" {
				links = append(links, serviceLink{Title: l.Title, Description: l.Description, URL: base + l.Path})
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
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
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
	err := s.Manager.UpdateSettings(r.Context(), name, req)
	var fe *manager.FormError
	switch {
	case errors.Is(err, manager.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.As(err, &fe):
		s.renderServiceWith(w, r, map[string]any{"SettingsError": fe.Msg, "Submitted": r.PostForm}, true)
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "settings", name)
	redirect(w, r, "/services/"+name)
}

func (s *Server) revealSubmit(w http.ResponseWriter, r *http.Request) {
	if s.serverOf(r) == nil {
		http.NotFound(w, r)
		return
	}
	name, key := r.PathValue("name"), r.PathValue("key")
	v, err := s.serverOf(r).RevealSecret(r.Context(), name, key)
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
