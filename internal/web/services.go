package web

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/mylab/mylab/internal/manager"
	"github.com/mylab/mylab/internal/pkgspec"
)

// formField is one input on the install form.
type formField struct {
	Key         string
	Title       string
	Description string
	Kind        string // checkbox, text, number, password
	Value       string
	Checked     bool
}

func (s *Server) catalogPage(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	items, err := s.Manager.Catalog()
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.render(w, "catalog", map[string]any{"User": currentUser(r), "Items": items})
}

func (s *Server) installPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pkg(w, r)
	if !ok {
		return
	}
	s.render(w, "install", s.installData(r, p, nil, ""))
}

func (s *Server) installSubmit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pkg(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form.", http.StatusBadRequest)
		return
	}
	req := manager.InstallRequest{
		Subdomain: r.PostForm.Get("subdomain"),
		Settings:  map[string]string{},
		Secrets:   map[string]string{},
	}
	for _, st := range p.Settings {
		if v, ok := r.PostForm["setting."+st.Key]; ok {
			if st.Type == pkgspec.SettingSecret {
				req.Secrets[st.Key] = v[0]
			} else {
				req.Settings[st.Key] = v[0]
			}
		}
	}

	err := s.Manager.Install(r.Context(), p.Name, req)
	var fe *manager.FormError
	switch {
	case errors.As(err, &fe):
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "install", s.installData(r, p, r.PostForm, fe.Msg))
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "install", p.Name)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// installData builds the form. submitted is nil on first display, so
// package defaults are shown; otherwise the user's values are kept (except
// secrets, which are never echoed back).
func (s *Server) installData(r *http.Request, p *pkgspec.Package, submitted map[string][]string, errMsg string) map[string]any {
	get := func(key string) (string, bool) {
		v, ok := submitted[key]
		if !ok || len(v) == 0 {
			return "", false
		}
		return v[0], true
	}
	var fields []formField
	for _, st := range p.Settings {
		if st.Type == pkgspec.SettingSecret && st.Generate {
			continue // MyLab creates these
		}
		f := formField{Key: "setting." + st.Key, Title: st.Title, Description: st.Description}
		if f.Title == "" {
			f.Title = st.Key
		}
		v, given := get(f.Key)
		switch st.Type {
		case pkgspec.SettingBool:
			f.Kind = "checkbox"
			if submitted != nil {
				f.Checked = given
			} else {
				f.Checked = st.Default == true || (st.Default == pkgspec.DefaultAuto && s.HasGPU)
			}
		case pkgspec.SettingInt, pkgspec.SettingString:
			f.Kind = "text"
			if st.Type == pkgspec.SettingInt {
				f.Kind = "number"
			}
			f.Value = v
			if submitted == nil && st.Default != nil {
				f.Value = fmt.Sprint(st.Default)
			}
		case pkgspec.SettingSecret:
			f.Kind = "password"
		}
		fields = append(fields, f)
	}

	sub, _ := get("subdomain")
	if route := manager.MainRoute(p); submitted == nil && route != "" {
		sub = p.Routes[route].Subdomain
	}
	return map[string]any{
		"User":      currentUser(r),
		"Package":   p,
		"Fields":    fields,
		"HasRoute":  len(p.Routes) > 0,
		"Subdomain": sub,
		"Domain":    s.Domain,
		"Error":     errMsg,
	}
}

func (s *Server) removePage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pkg(w, r)
	if !ok {
		return
	}
	s.render(w, "remove", map[string]any{"User": currentUser(r), "Package": p, "DataDirs": s.Manager.DataDirs(p.Name)})
}

func (s *Server) removeSubmit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.pkg(w, r)
	if !ok {
		return
	}
	err := s.Manager.Remove(r.Context(), p.Name)
	if errors.Is(err, manager.ErrNotFound) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "remove", p.Name)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// pkg loads the package named in the URL, writing a 404 if there is none.
func (s *Server) pkg(w http.ResponseWriter, r *http.Request) (*pkgspec.Package, bool) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return nil, false
	}
	p, err := s.Manager.Package(r.PathValue("name"))
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return nil, false
	}
	if err != nil {
		s.serverError(w, err)
		return nil, false
	}
	return p, true
}
