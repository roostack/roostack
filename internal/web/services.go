package web

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
)

// formField is one input on the install form.
type formField struct {
	Key         string
	Title       string
	Description string
	Kind        string // checkbox, text, number, password
	Value       string
	Checked     bool
	Required    bool
	MinLength   int
}

// inputFields builds the form fields for an action's inputs.
func inputFields(h *pkgspec.Hook) []formField {
	var fields []formField
	for _, in := range h.Inputs {
		kind := "text"
		if in.Type == pkgspec.SettingSecret {
			kind = "password"
		}
		fields = append(fields, formField{Key: "input." + in.Key, Title: cmp.Or(in.Title, in.Key),
			Description: in.Description, Kind: kind, Required: true, MinLength: in.MinLength})
	}
	return fields
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
		Host:      r.PostForm.Get("host"),
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
	msg, isForm := userMessage(err)
	switch {
	case isForm:
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "install", s.installData(r, p, r.PostForm, msg))
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "install", p.Name)
	redirect(w, r, "/")
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
			continue // Roostack creates these
		}
		f := formField{Key: "setting." + st.Key, Title: st.Title, Description: st.Description,
			Required: st.Required || st.Type == pkgspec.SettingSecret, MinLength: st.MinLength}
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
	// Where to run it, once the lab has more than one server (SPEC.md §10.4).
	var servers []manager.Server
	if ms, err := s.Manager.Servers(); err == nil && len(ms) > 1 {
		servers = ms
	}
	host, _ := get("host")
	return map[string]any{
		"User":      currentUser(r),
		"Package":   p,
		"Fields":    fields,
		"HasRoute":  len(p.Routes) > 0,
		"Subdomain": sub,
		"Domain":    s.Domain,
		"Error":     errMsg,
		"Servers":   servers,
		"Host":      host,
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
		redirect(w, r, "/")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "remove", p.Name)
	redirect(w, r, "/")
}

func (s *Server) portSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	name, port := r.PathValue("name"), r.PathValue("port")
	enabled := r.FormValue("enabled") == "on"
	hostPort, err := strconv.Atoi(strings.TrimSpace(r.FormValue("host_port")))
	if err != nil {
		err = &manager.FormError{Msg: "Enter a port number, such as 2222."}
	} else {
		err = s.Manager.SetPort(r.Context(), name, port, enabled, hostPort)
	}
	msg, isForm := userMessage(err)
	switch {
	case errors.Is(err, manager.ErrNotFound):
		http.NotFound(w, r)
		return
	case isForm:
		s.renderService(w, r, map[string]string{"port": port, "msg": msg})
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	state := "off"
	if enabled {
		state = fmt.Sprintf("on (port %d)", hostPort)
	}
	s.Store.Audit(r.Context(), currentUser(r), "port", fmt.Sprintf("%s %s %s", name, port, state))
	redirect(w, r, "/services/"+name)
}

func (s *Server) retrySetupSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if err := n.RetryInstall(r.Context(), name); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "retry "+name)
	redirect(w, r, "/services/"+name)
}

// installFields are a failed install's settings, to fix and try again
// (manager.InstallSettings); secrets are left empty to keep them.
func (s *Server) installFields(name string, p *pkgspec.Package) []formField {
	values, _, err := s.Manager.CurrentSettings(name)
	if err != nil {
		return nil
	}
	var out []formField
	for _, st := range manager.InstallSettings(p) {
		f := formField{Key: "setting." + st.Key, Title: cmp.Or(st.Title, st.Key), Description: st.Description,
			Required: st.Required, MinLength: st.MinLength}
		switch st.Type {
		case pkgspec.SettingSecret:
			f.Kind, f.Required, f.MinLength = "password", false, 0
			f.Description = "Leave empty to keep what you typed before."
		case pkgspec.SettingBool:
			f.Kind, f.Checked = "checkbox", values[st.Key] == true
		case pkgspec.SettingInt:
			f.Kind, f.Value = "number", fmt.Sprint(values[st.Key])
		default:
			f.Kind = "text"
			if v, ok := values[st.Key].(string); ok {
				f.Value = v
			}
		}
		out = append(out, f)
	}
	return out
}

// retryWithSubmit runs a failed install again with changed settings.
func (s *Server) retryWithSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form.", http.StatusBadRequest)
		return
	}
	settings, secrets := map[string]string{}, map[string]string{}
	if p, err := s.Manager.Package(name); err == nil {
		for _, st := range manager.InstallSettings(p) {
			v, given := r.PostForm["setting."+st.Key]
			if !given || len(v) == 0 {
				continue
			}
			if st.Type == pkgspec.SettingSecret {
				secrets[st.Key] = v[0]
			} else {
				settings[st.Key] = v[0]
			}
		}
	}
	err := s.Manager.RetryInstallWith(r.Context(), name, settings, secrets)
	if msg, ok := userMessage(err); ok {
		s.renderServiceWith(w, r, map[string]any{"RetryError": msg}, true)
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
	s.Store.Audit(r.Context(), currentUser(r), "setup", "retry "+name+" with changed settings")
	redirect(w, r, "/services/"+name)
}

func (s *Server) actionSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil {
		http.NotFound(w, r)
		return
	}
	name, action := r.PathValue("name"), r.PathValue("action")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad form.", http.StatusBadRequest)
		return
	}
	inputs := map[string]string{}
	for k, v := range r.PostForm {
		if key, ok := strings.CutPrefix(k, "input."); ok && len(v) > 0 {
			inputs[key] = v[0]
		}
	}
	out, err := n.RunAction(r.Context(), name, action, inputs)
	msg, isForm := userMessage(err)
	var ae *manager.ActionError
	result := map[string]string{"action": action}
	switch {
	case errors.Is(err, manager.ErrNotFound):
		http.NotFound(w, r)
		return
	case isForm:
		result["error"] = msg
	case errors.As(err, &ae):
		result["error"] = ae.Title + " didn't work."
		result["output"] = ae.Output
	case err != nil:
		s.serverError(w, err)
		return
	default:
		result["output"] = out
	}
	status := "ok"
	if result["error"] != "" {
		status = "failed"
	}
	// Only the action's name: inputs may be sensitive.
	s.Store.Audit(r.Context(), currentUser(r), "action", fmt.Sprintf("%s %s: %s", name, action, status))
	s.renderServiceWith(w, r, map[string]any{"ActionResult": result}, result["error"] != "")
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
