package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
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
	// KeepsFirst: the first of a package's install-only settings, over
	// its data from before (left empty, the app keeps what it has).
	KeepsFirst bool
	// InstallOnly: used only by its first-time setup, so not asked for
	// when its data is brought in (SPEC.md §8.4).
	InstallOnly bool
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

// catalogReady is asked every few seconds by the catalog page while it
// waits for the registry's list: 204 while it's still waiting, then the
// page reloads (htmx's HX-Refresh) to show the list, or why there isn't
// one.
func (s *Server) catalogReady(w http.ResponseWriter, r *http.Request) {
	if s.Registry != nil {
		if st := s.Registry.Status(); st.Serial == 0 && (st.Fetching || st.Error == "") && !st.NoRootKey && s.registryOn() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) catalogPage(w http.ResponseWriter, r *http.Request) {
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
	u := currentUser(r)
	data := map[string]any{"User": u, "Registry": s.registryView(), "IsOwner": auth.Can(u.Role, auth.ManageHost)}
	items, err := s.Manager.Catalog()
	if err != nil {
		s.serverError(w, err)
		return
	}
	// Turned off, nothing from the registry is offered: the lab wouldn't
	// hear if a version were withdrawn (SPEC.md §8.3.3). The Owner's own
	// packages still are.
	on := s.registryOn()
	if !on {
		items = slices.DeleteFunc(items, func(it manager.CatalogItem) bool { return !it.Local })
	}
	var listing map[string]registry.Listed
	if s.Registry != nil && on {
		if listing, err = s.Registry.Listing(); err != nil {
			s.Log.Warn("reading the service registry's index", "err", err)
		}
	}
	data["Items"], data["Categories"] = catalogItems(items, listing)
	// Installed packages with a newer version: the service page has the
	// update (SPEC.md §7.1), by package.
	updates := map[string]*manager.Update{}
	avail := s.Manager.AvailableUpdates(r.Context())
	for _, u := range avail {
		if updates[u.Package] == nil || u.Service < updates[u.Package].Service {
			updates[u.Package] = u
		}
	}
	data["Updates"] = updates
	// Arrived from adding a package of their own.
	if added := r.URL.Query().Get("added"); added != "" {
		for _, it := range items {
			if it.Name == added && it.Local {
				data["Added"] = it
			}
		}
		// Services installed from an older version of it can update now
		// (SPEC.md §7.1).
		var using []*manager.Update
		for _, u := range avail {
			if u.Local && u.Package == added {
				using = append(using, u)
			}
		}
		slices.SortFunc(using, func(a, b *manager.Update) int { return strings.Compare(a.Title, b.Title) })
		data["AddedUpdates"] = using
	}
	// Arrived from installing one that needs a newer Roostack (pkg).
	if needs := r.URL.Query().Get("needs"); needs != "" {
		for _, it := range data["Items"].([]catalogItem) {
			if it.Name == needs && it.NeedsRoostack != "" {
				data["Needs"] = it
			}
		}
	}
	s.render(w, "catalog", data)
}

// catalogItem is one service on the catalog page (SPEC.md §8.3.6).
type catalogItem struct {
	manager.CatalogItem
	// Tier is the registry's (registry.TierOfficial or TierCommunity).
	Tier string
	// NeedsRoostack is the Roostack release it needs, when it can't be
	// installed on this one: it's listed, but not offered.
	NeedsRoostack string
	// IconURL is its logo's; DrawnIcon the icon shown without one.
	IconURL, DrawnIcon string
	// CategoryKeys (space-separated) and Search (lower case) are for the
	// filter; CategoryTitles is shown.
	CategoryKeys, CategoryTitles, Search string
}

// catalogItems lists the packages the lab can install, with what the
// registry's index says of them, and the ones needing a newer Roostack,
// by title; and the categories they're in, for the filter, in
// pkgspec.Categories' order.
func catalogItems(items []manager.CatalogItem, listing map[string]registry.Listed) ([]catalogItem, []pkgspec.Category) {
	var out []catalogItem
	offered := map[string]bool{}
	for _, it := range items {
		offered[it.Name] = true
		c := catalogItem{CatalogItem: it}
		if l, ok := listing[it.Name]; ok && !it.Local {
			c.Tier = l.Tier
		}
		out = append(out, c)
	}
	for name, l := range listing {
		if offered[name] || l.NeedsRoostack == "" {
			continue
		}
		out = append(out, catalogItem{Tier: l.Tier, NeedsRoostack: l.NeedsRoostack, CatalogItem: manager.CatalogItem{
			Name: name, Title: l.Title, Description: l.Description, Categories: l.Categories, Icon: l.Icon}})
	}
	used := map[string]bool{}
	for i := range out {
		c := &out[i]
		if c.Icon {
			c.IconURL = "/install/" + c.Name + "/icon.png"
		}
		c.DrawnIcon = categoryIcon(c.Categories)
		var keys, titles []string
		for _, key := range c.Categories {
			if title := pkgspec.CategoryTitle(key); title != "" {
				keys, titles = append(keys, key), append(titles, title)
				used[key] = true
			}
		}
		c.CategoryKeys, c.CategoryTitles = strings.Join(keys, " "), strings.Join(titles, ", ")
		words := append([]string{c.Name, c.Title, c.Description}, titles...)
		c.Search = strings.ToLower(strings.Join(append(words, c.Keywords...), " "))
	}
	slices.SortFunc(out, func(a, b catalogItem) int { return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)) })
	var categories []pkgspec.Category
	for _, c := range pkgspec.Categories {
		if used[c.Key] {
			categories = append(categories, c)
		}
	}
	return out, categories
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
	u := currentUser(r)
	req := s.installRequest(r, r.PostForm, p)
	// Only the Owner may accept what a package of their own asks for
	// (decided by the Owner, 2026-10-14).
	req.AcceptRisks = r.PostForm.Get("accept_risks") == "on" && auth.Can(u.Role, auth.ManageHost)
	if req.Public {
		// Only an Owner can make a service public (installRequest), so
		// only an Owner answers this lab-wide question.
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
			s.Store.Audit(r.Context(), u, "public access", "CrowdSec sharing: "+share)
		}
	}

	err := s.Manager.Install(r.Context(), p.Name, req)
	msg, isForm := usererr.Message(err)
	switch {
	case isForm:
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "install", s.installData(r, p, r.PostForm, msg))
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "install", s.installAudit(p, req))
	if req.BringData {
		// Its page says it waits for its data, with the way in.
		redirect(w, r, "/services/"+url.PathEscape(cmp.Or(req.Name, p.Name)))
		return
	}
	redirect(w, r, "/")
}

// installAudit is the audit entry for an install: the service, where it
// runs, whether it went public (or will once it's set up, SPEC.md §8.1),
// and any risks the Owner accepted for a package of their own (SPEC.md
// §6.11, §8.3.4).
func (s *Server) installAudit(p *pkgspec.Package, req manager.InstallRequest) string {
	name := cmp.Or(req.Name, p.Name)
	detail := name
	if req.Host != "" {
		detail += " on " + req.Host
	}
	if req.Public {
		access, _ := s.Manager.Access(name)
		if slices.ContainsFunc(access, func(a manager.RouteAccess) bool { return a.AfterSetup }) {
			detail += ", public once set up"
		} else {
			detail += ", public"
		}
	}
	if req.BringData {
		detail += ", waiting for its data"
	}
	if c := s.Manager.CheckPackage(p); req.AcceptRisks && c.Local && len(c.Problems) > 0 {
		detail += "; accepted risks: " + strings.Join(c.Problems, "; ")
	}
	return detail
}

// installRequest reads the install form (submitted, or as it stands for
// the summary).
func (s *Server) installRequest(r *http.Request, form url.Values, p *pkgspec.Package) manager.InstallRequest {
	name := p.Name
	if sfx := strings.ToLower(strings.TrimSpace(form.Get("name_suffix"))); sfx != "" {
		name += "-" + sfx
	}
	req := manager.InstallRequest{
		Name:      name,
		Title:     form.Get("title"),
		Subdomain: form.Get("subdomain"),
		Host:      form.Get("host"),
		Settings:  map[string]string{},
		Secrets:   map[string]string{},
		// On a lab on the internet, services start public (SPEC.md §6.12)
		// when an Owner installs them; an Admin's start private, since
		// only an Owner makes anything public (§6.11).
		Public: s.onInternet() && auth.Can(currentUser(r).Role, auth.ManageHost) && form.Get("public") == "on",
		// Bringing its data (SPEC.md §8.4): the Owner only.
		BringData: form.Get("data") == "bring" && auth.Can(currentUser(r).Role, auth.ManageHost),
	}
	// The media libraries ticked for the server chosen (SPEC.md §8.1).
	if form.Get("libraries_chosen") != "" {
		req.LibrariesChosen = true
		req.Libraries = form["library."+cmp.Or(req.Host, s.thisServerID())]
	}
	for _, st := range p.Settings {
		if v, ok := form["setting."+st.Key]; ok && len(v) > 0 {
			if st.Type == pkgspec.SettingSecret {
				req.Secrets[st.Key] = v[0]
			} else {
				req.Settings[st.Key] = v[0]
			}
		}
	}
	return req
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
		if st.Type == pkgspec.SettingSecret && (st.Generate || st.FromHook) {
			continue // Roostack creates these, or the app does
		}
		f := formField{Key: "setting." + st.Key, Title: st.Title, Description: st.Description,
			Required: st.Required || st.Type == pkgspec.SettingSecret, MinLength: st.MinLength, InstallOnly: st.InstallOnly}
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
		sub, title, suffix = manager.NewServiceNames(p, another)
	}
	// Where to run it, once the lab has more than one server (SPEC.md §10.4).
	var servers []manager.Server
	if ms, err := s.Manager.Servers(); err == nil && len(ms) > 1 {
		servers = ms
	}
	host, _ := get("host")
	// In a lab mixing processors, the servers it has no version for
	// aren't offered, and the form says why (SPEC.md §8.3.3); it starts
	// on one it can run on.
	noBuild, noBuildText := map[string]string{}, ""
	if len(servers) > 0 {
		var names, arches []string
		nbs := s.Manager.NoBuildOn(p)
		for _, nb := range nbs {
			noBuild[nb.ID] = nb.Arch
			names = append(names, nb.Name)
			if !slices.Contains(arches, nb.Arch) {
				arches = append(arches, nb.Arch)
			}
		}
		if len(names) > 0 {
			its := "its"
			if len(names) > 1 {
				its = "their"
			}
			noBuildText = fmt.Sprintf("%s can't run on %s: it has no version for %s kind of processor (%s).",
				p.Title, words.Or(names), its, words.And(arches))
		}
		if submitted == nil {
			host = manager.InstallServer(servers, nbs, host)
		}
	}
	// On a lab on the internet: public from the start, unless the package
	// says its address isn't safe to be (SPEC.md §6.12).
	unsafe := ""
	if route := manager.MainRoute(p); route != "" {
		unsafe = p.Routes[route].Public.Unsafe
	}
	_, public := get("public")
	asked, _ := s.Manager.SignalSharing()

	// The summary of what it sets up, for the form as it stands (SPEC.md
	// §8.3.4); the page asks for it again as the form changes.
	form := url.Values{"title": {title}, "name_suffix": {suffix}, "subdomain": {sub}, "host": {host}}
	if submitted == nil || public {
		form.Set("public", "on")
	}
	for _, f := range fields {
		switch {
		case f.Kind == "checkbox" && f.Checked:
			form.Set(f.Key, "on")
		case f.Kind != "checkbox" && f.Kind != "password":
			form.Set(f.Key, f.Value)
		}
	}
	summary := s.installSummary(p, s.installRequest(r, form, p))
	// Over its data from before, the first-time settings may be left
	// empty: the app keeps what it has (SPEC.md §8.1, "hooks"). Only when the
	// server says the data is there: one that can't say asks as before.
	if summary.InstallSummary != nil && len(summary.LeftData) > 0 {
		first := true
		for i, st := range p.Settings {
			if !st.InstallOnly || st.Generate {
				continue
			}
			j := slices.IndexFunc(fields, func(f formField) bool { return f.Key == "setting."+p.Settings[i].Key })
			fields[j].Required, fields[j].KeepsFirst, first = false, first, false
		}
	}
	// The package policy (SPEC.md §8.3.4): a registry package that breaks
	// it can't be installed; one of the Owner's own can, once the Owner
	// has read what it asks for.
	u := currentUser(r)
	policy := s.Manager.CheckPackage(p)
	owner := auth.Can(u.Role, auth.ManageHost)
	return map[string]any{
		"Policy":        policy,
		"IsOwner":       owner,
		"CanInstall":    len(policy.Problems) == 0 || (policy.Local && owner),
		"Summary":       summary,
		"Credit":        newProjectCredit(p),
		"BringData":     owner && submitted != nil && submitted["data"] != nil && submitted["data"][0] == "bring",
		"Internet":      s.onInternet(),
		"Public":        owner && (submitted == nil || public),
		"Unsafe":        unsafe,
		"SharingAsked":  asked,
		"User":          u,
		"Package":       p,
		"Fields":        fields,
		"HasRoute":      len(p.Routes) > 0,
		"Subdomain":     sub,
		"Domain":        s.Domain,
		"Error":         errMsg,
		"Servers":       servers,
		"Host":          host,
		"NoBuild":       noBuild,
		"NoBuildText":   noBuildText,
		"Another":       another,
		"Suffix":        suffix,
		"Title":         title,
		"BaseSubdomain": base,
		"MaxSuffix":     max(1, instance.MaxName-len(p.Name)-1),
		"LibraryGroups": s.libraryGroups(r, p, submitted),
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
	name, port := r.PathValue("name"), r.PathValue("port")
	enabled := r.FormValue("enabled") == "on"
	hostPort, err := strconv.Atoi(strings.TrimSpace(r.FormValue("host_port")))
	if err != nil {
		err = &usererr.Error{Msg: "Enter a port number, such as 2222."}
	} else {
		err = s.Manager.SetPort(r.Context(), name, port, enabled, hostPort)
	}
	msg, isForm := usererr.Message(err)
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
	if msg, ok := usererr.Message(err); ok {
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
	msg, isForm := usererr.Message(err)
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

// scheduledSubmit runs one of a service's scheduled tasks as soon as its
// server's maintenance allows: Run now, or Try again after it failed
// (SPEC.md §8.1).
func (s *Server) scheduledSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil {
		http.NotFound(w, r)
		return
	}
	name, hook := r.PathValue("name"), r.PathValue("hook")
	err := n.RunScheduled(r.Context(), name, hook)
	msg, isForm := usererr.Message(err)
	switch {
	case errors.Is(err, manager.ErrNotFound):
		http.NotFound(w, r)
		return
	case isForm:
		s.renderServiceWith(w, r, map[string]any{"ScheduledError": map[string]string{"name": hook, "msg": msg}}, true)
		return
	case err != nil:
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "scheduled task", name+" "+hook+": run now")
	redirect(w, r, "/services/"+name+"#scheduled")
}

// servicePkg loads the package of the installed service named in the URL
// (titled as the service), writing a 404 if there is none.
func (s *Server) servicePkg(w http.ResponseWriter, r *http.Request) (*pkgspec.Package, bool) {
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
	name := r.PathValue("name")
	p, err := s.Manager.Package(name)
	// With the registry turned off, only the Owner's own packages can be
	// installed (SPEC.md §8.3.3).
	if err == nil && !s.registryOn() && !s.Manager.CheckPackage(p).Local {
		redirect(w, r, "/catalog") // which says why
		return nil, false
	}
	if errors.Is(err, manager.ErrNotFound) {
		// Listed in the catalog, but it needs a newer Roostack: the
		// catalog says so (SPEC.md §8.3.6).
		if s.Registry != nil {
			if listing, _ := s.Registry.Listing(); listing[name].NeedsRoostack != "" {
				redirect(w, r, "/catalog?needs="+url.QueryEscape(name))
				return nil, false
			}
		}
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
	name, title := r.PathValue("name"), r.FormValue("title")
	err := s.Manager.SetTitle(r.Context(), name, title)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if msg, ok := usererr.Message(err); ok {
		s.renderServiceWith(w, r, map[string]any{"TitleError": msg, "EditSettings": true}, true)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", name+" is now called "+strings.TrimSpace(title))
	redirect(w, r, "/services/"+name)
}
