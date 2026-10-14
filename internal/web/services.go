package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/instance"
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
	// A fresh list from the registry, unless there's one from the last
	// few minutes (SPEC.md §8.3.3). The page doesn't wait long: the
	// background check keeps the list current anyway.
	if s.Registry != nil {
		ctx, cancel := context.WithTimeout(r.Context(), catalogWait)
		if err := s.Registry.CheckIfOlder(ctx, catalogCheckAge); err != nil {
			s.Log.Warn("checking the service registry", "err", err)
		}
		cancel()
	}
	data := map[string]any{"User": currentUser(r), "Registry": s.registryView()}
	// Turned off, nothing is offered: the lab wouldn't hear if a version
	// were withdrawn (SPEC.md §8.3.3).
	if s.registryOn() {
		items, err := s.Manager.Catalog()
		if err != nil {
			s.serverError(w, err)
			return
		}
		data["Items"] = items
	}
	s.render(w, "catalog", data)
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
	name := p.Name
	if sfx := strings.ToLower(strings.TrimSpace(r.PostForm.Get("name_suffix"))); sfx != "" {
		name += "-" + sfx
	}
	req := manager.InstallRequest{
		Name:      name,
		Title:     r.PostForm.Get("title"),
		Subdomain: r.PostForm.Get("subdomain"),
		Host:      r.PostForm.Get("host"),
		Settings:  map[string]string{},
		Secrets:   map[string]string{},
		// On a lab on the internet, services start public (SPEC.md §6.12).
		Public: s.onInternet() && r.PostForm.Get("public") == "on",
	}
	if req.Public {
		// Asked the first time anything goes public (SPEC.md §6.11).
		if asked, _ := s.Manager.SignalSharing(); !asked {
			share := r.PostForm.Get("share")
			if share != "yes" && share != "no" {
				w.WriteHeader(http.StatusBadRequest)
				s.render(w, "install", s.installData(r, p, r.PostForm, "Choose whether CrowdSec shares attack reports."))
				return
			}
			if err := s.Manager.SetSignalSharing(share == "yes"); err != nil {
				s.serverError(w, err)
				return
			}
			s.Store.Audit(r.Context(), currentUser(r), "public access", "CrowdSec sharing: "+share)
		}
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

	// Another copy of something installed: it needs a name, title and
	// address of its own (SPEC.md §8.2).
	another := false
	if items, err := s.Manager.Catalog(); err == nil {
		for _, it := range items {
			another = another || (it.Name == p.Name && it.Installed)
		}
	}
	suffix, _ := get("name_suffix")
	title, _ := get("title")
	sub, _ := get("subdomain")
	base := ""
	if route := manager.MainRoute(p); route != "" {
		base = p.Routes[route].Subdomain
	}
	if submitted == nil {
		sub, title = base, p.Title
		if another {
			sub, title, suffix = base+"-2", p.Title+" (2)", "2"
		}
	}
	// Where to run it, once the lab has more than one server (SPEC.md §10.4).
	var servers []manager.Server
	if ms, err := s.Manager.Servers(); err == nil && len(ms) > 1 {
		servers = ms
	}
	host, _ := get("host")
	// On a lab on the internet: public from the start, unless the package
	// says its address isn't safe to be (SPEC.md §6.12).
	unsafe := ""
	if route := manager.MainRoute(p); route != "" {
		unsafe = p.Routes[route].Public.Unsafe
	}
	_, public := get("public")
	asked, _ := s.Manager.SignalSharing()
	return map[string]any{
		"Internet":      s.onInternet(),
		"Public":        submitted == nil || public,
		"Unsafe":        unsafe,
		"SharingAsked":  asked,
		"User":          currentUser(r),
		"Package":       p,
		"Fields":        fields,
		"HasRoute":      len(p.Routes) > 0,
		"Subdomain":     sub,
		"Domain":        s.Domain,
		"Error":         errMsg,
		"Servers":       servers,
		"Host":          host,
		"Another":       another,
		"Suffix":        suffix,
		"Title":         title,
		"BaseSubdomain": base,
		"MaxSuffix":     max(1, instance.MaxName-len(p.Name)-1),
	}
}

func (s *Server) removePage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.servicePkg(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	s.render(w, "remove", map[string]any{"User": currentUser(r), "Package": p, "Name": name, "DataDirs": s.Manager.DataDirs(name)})
}

func (s *Server) removeSubmit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.servicePkg(w, r); !ok {
		return
	}
	name := r.PathValue("name")
	err := s.Manager.Remove(r.Context(), name)
	if errors.Is(err, manager.ErrNotFound) {
		redirect(w, r, "/")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "remove", name)
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
	if p, err := s.Manager.ServicePackage(name); err == nil {
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

// servicePkg loads the package of the installed service named in the URL
// (titled as the service), writing a 404 if there is none.
func (s *Server) servicePkg(w http.ResponseWriter, r *http.Request) (*pkgspec.Package, bool) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return nil, false
	}
	p, err := s.Manager.ServicePackage(r.PathValue("name"))
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

// pkg loads the package named in the URL, writing a 404 if there is none.
func (s *Server) pkg(w http.ResponseWriter, r *http.Request) (*pkgspec.Package, bool) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return nil, false
	}
	if !s.registryOn() {
		redirect(w, r, "/catalog") // which says why
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

// titleSubmit changes what people call a service; its name stays
// (SPEC.md §8.2).
func (s *Server) titleSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	name, title := r.PathValue("name"), r.FormValue("title")
	err := s.Manager.SetTitle(r.Context(), name, title)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if msg, ok := userMessage(err); ok {
		s.renderServiceWith(w, r, map[string]any{"TitleError": msg}, true)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", name+" is now called "+strings.TrimSpace(title))
	redirect(w, r, "/services/"+name)
}
