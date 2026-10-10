package web

import (
	"bytes"
	"cmp"
	"errors"
	"hash/fnv"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/store"
)

// nav is the top navigation, shown to logged-in users.
type nav struct {
	Active   string
	Username string
	Backups  bool
	Settings bool
	// Theme is the user's mode (auto, light, dark); NextTheme is what the
	// toggle switches to.
	Theme, NextTheme string
	// Palette is the user's colour theme and Accent its accent colour, ""
	// for the theme's default (themes.go).
	Palette, Accent string
	// OOB marks a theme toggle sent to replace the page's (htmx).
	OOB bool
}

// navFor picks the links a role can use and marks the current section.
func navFor(u *store.User, page string) nav {
	active := navTab(page)
	next := map[string]string{"auto": "light", "light": "dark", "dark": "auto"}[u.Theme]
	if next == "" {
		u.Theme, next = "auto", "light"
	}
	return nav{Active: active, Username: u.Username, Theme: u.Theme, NextTheme: next, Palette: userPalette(u), Accent: userAccent(u),
		Backups: auth.Can(u.Role, auth.ManageServices), Settings: auth.Can(u.Role, auth.ManageHost)}
}

// navTab is the top bar's tab a page belongs to: by name for the few that
// don't follow a pattern, then by prefix, so new pages get it right
// (TestNavTabs checks every template).
func navTab(page string) string {
	if tab, ok := map[string]string{
		"home": "home", "catalog": "services", "install": "services", "remove": "services", "move": "services",
		"restore": "backups", "recovery-key": "settings", "users": "settings", "dns-takeover": "settings",
		"account": "account", "twofactor": "account", "account-appearance": "account", "account-notifications": "account",
		"cloud-open": "settings", "cloud-linked": "settings",
		"setup-restore": "", "setup-joined": "", // first run, before anyone is logged in
	}[page]; ok {
		return tab
	}
	for _, p := range []struct{ prefix, tab string }{
		{"settings", "settings"}, {"setup-", "settings"}, {"service", "services"}, {"backups", "backups"},
	} {
		if strings.HasPrefix(page, p.prefix) {
			return p.tab
		}
	}
	return ""
}

// pageData collects what most pages show, for the current user.
func (s *Server) pageData(r *http.Request) (map[string]any, error) {
	u := currentUser(r)
	services, err := s.Services(r.Context())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"User":      u,
		"Services":  services,
		"CanManage": s.Manager != nil && auth.Can(u.Role, auth.ManageServices),
		"IsOwner":   auth.Can(u.Role, auth.ManageHost),
		"Internet":  s.onInternet(),
		"Apply":     s.applyStatus(),
		"Server":    s.serverRefOf(r),
	}, nil
}

func (s *Server) homePage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Attention"] = s.attention(r)
	if data["CanManage"] == true {
		data["Servers"] = s.serverViews(r)
	}
	// What an apply is doing (downloading, building), or why it failed
	// lately: nothing should look like magic.
	if a := s.activityFor(r, activity.Apply); a != nil && (a.Running() || a.Err != "" && time.Since(a.Finished) < 24*time.Hour) {
		data["ApplyActivity"] = a
	}
	s.render(w, "home", data)
}

func (s *Server) homePartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Attention"] = s.attention(r)
	if data["CanManage"] == true {
		data["Servers"] = s.serverViews(r)
	}
	s.renderLive(w, r, "home", "home-live", data)
}

func (s *Server) servicesPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if a := s.activityFor(r, activity.Apply); a != nil {
		data["ApplyActivity"] = a
	}
	// Caddy's image build, while it runs or if it failed: new services
	// wait for it, and it takes minutes the first time.
	if a := s.activityFor(r, activity.Build); a != nil && (a.Running() || a.Err != "") {
		data["BuildActivity"] = a
	}
	s.addBuiltIns(r, data)
	s.render(w, "services", data)
}

func (s *Server) servicesPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.addBuiltIns(r, data)
	s.renderLive(w, r, "services", "services-live", data)
}

// addBuiltIns adds Roostack's own parts on every server (?builtin=1): its
// proxy, backup stores, the roostack service and agent, network links.
func (s *Server) addBuiltIns(r *http.Request, data map[string]any) {
	show := r.URL.Query().Get("builtin") == "1"
	data["ShowBuiltIn"] = show
	if show && s.Manager != nil && data["CanManage"] == true {
		data["BuiltIns"] = s.Manager.AllBuiltIns(r.Context())
	}
}

func (s *Server) servicePage(w http.ResponseWriter, r *http.Request) {
	s.renderServiceWith(w, r, nil, false)
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
// an action's result); failed sets a 400 status.
func (s *Server) renderServiceWith(w http.ResponseWriter, r *http.Request, extra map[string]any, failed bool) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	name := r.PathValue("name")
	for _, svc := range data["Services"].([]ServiceView) {
		if svc.Name == name {
			data["Service"] = svc
			if s.Manager != nil {
				if ms, err := s.Manager.Servers(); err == nil && len(ms) > 1 {
					data["MultiServer"] = true
				}
			}
		}
	}
	if data["Service"] == nil {
		http.NotFound(w, r)
		return
	}
	// Installing: its log, for the step list's Details. Once it's done, the
	// step list's polling reloads the page (status, Open link).
	if install := data["Service"].(ServiceView).Install; install != nil {
		if a := s.activityFor(r, install.Log); a != nil {
			data["InstallActivity"] = a
		}
	} else if r.URL.Query().Get("installing") == "1" && r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
	}
	if s.Manager != nil {
		if p, err := s.Manager.ServicePackage(name); err == nil {
			data["Package"] = p
			var actions []serviceAction
			for _, h := range p.HooksOn(pkgspec.HookAction) {
				actions = append(actions, serviceAction{Name: h.Name, Title: h.Title, Description: h.Description, Fields: inputFields(h)})
			}
			data["Actions"] = actions
			s.addSettingsData(data, name, p, nil)
			if access, err := s.Manager.Access(name); err == nil {
				data["Access"] = access
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
		ports, err := s.Manager.Ports(name)
		if err != nil && !errors.Is(err, manager.ErrNotFound) {
			s.serverError(w, err)
			return
		}
		data["Ports"] = ports
	}
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
	s.render(w, "service", data)
}

func (s *Server) backupsPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderBackups(w, r, data)
}

// renderBackups shows the Backups page; data may carry an error. The
// status, latest run and plan are the chosen server's (the switcher);
// restoring, storage at other labs and the recovery key are the whole
// lab's, the same whichever server is chosen.
func (s *Server) renderBackups(w http.ResponseWriter, r *http.Request, data map[string]any) {
	m := s.serverRefOf(r)
	data["Backup"] = s.backupView(r)
	if auth.Can(currentUser(r).Role, auth.ManageHost) {
		data["Switcher"] = s.serverTabs(r, "backups")
	}
	if m.This {
		data["BackupRepo"] = s.BackupRepo
	}
	owner := auth.Can(currentUser(r).Role, auth.ManageHost)
	data["CanRestore"] = owner && s.labHasBackups(r)
	// The recovery key is the lab's, kept by the controller (which serves
	// this page).
	data["RecoveryKey"] = s.Setup != nil && owner && s.hasRecoveryKey()
	if data["RecoveryKey"] == true && s.Manager != nil {
		if k, err := s.Manager.KeyChangeStatus(); err == nil {
			data["KeyChange"] = k
		}
	}
	if a := s.activityFor(r, activity.Backup); a != nil {
		data["BackupActivity"] = a
	}
	// Every service in the lab can be restored, from any backup of it
	// (SPEC.md §10.10), listed by the server it runs on.
	all, _ := data["Services"].([]ServiceView)
	data["RestoreGroups"] = s.servicesByServer(all)
	// Where backups go: the plan (SPEC.md §6.9).
	if s.Manager != nil && auth.Can(currentUser(r).Role, auth.ManageHost) {
		// Storage at other labs is the whole lab's, shown on every server's page.
		remotes := s.remoteViews(r)
		data["Remotes"] = remotes
		if p := s.backupPlan(r, m, false); p != nil {
			p.Error, _ = data["Error"].(string)
			p.addLabUsage(remotes)
			data["Plan"] = p
		}
	}
	if data["Error"] != nil {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "backups", data)
}

// serviceGroup is the services on one server, or those waiting to be
// restored (their server is gone).
type serviceGroup struct {
	Server   string
	Waiting  bool
	Services []ServiceView
}

// servicesByServer groups services by the server they run on, in the
// lab's order of servers (one unnamed group with a single server).
func (s *Server) servicesByServer(services []ServiceView) []serviceGroup {
	var order []string
	if s.Manager != nil {
		if ms, err := s.Manager.Servers(); err == nil && len(ms) > 1 {
			for _, m := range ms {
				order = append(order, m.Name)
			}
		}
	}
	this := ""
	if s.Node != nil && len(order) > 0 {
		this = s.Node.Identity().Name
	}
	var groups []serviceGroup
	add := func(name string, sv ServiceView) {
		for i := range groups {
			if groups[i].Server == name {
				groups[i].Services = append(groups[i].Services, sv)
				return
			}
		}
		groups = append(groups, serviceGroup{Server: name, Services: []ServiceView{sv}})
	}
	var waiting []ServiceView
	for _, sv := range services {
		if sv.Status == StatusWaiting {
			waiting = append(waiting, sv)
			continue
		}
		name := ""
		if len(order) > 0 {
			name = cmp.Or(sv.Server, this)
		}
		add(name, sv)
	}
	slices.SortStableFunc(groups, func(a, b serviceGroup) int {
		return cmp.Compare(slices.Index(order, a.Server), slices.Index(order, b.Server))
	})
	if len(waiting) > 0 {
		// First: they're what needs doing.
		groups = slices.Insert(groups, 0, serviceGroup{Waiting: true, Services: waiting})
	}
	return groups
}

// labHasBackups reports whether any server in the lab has backups set
// up, so there's something to restore from.
func (s *Server) labHasBackups(r *http.Request) bool {
	if s.restores(r) != nil || (s.Node != nil && s.Node.Restores() != nil) {
		return true
	}
	if s.Manager == nil {
		return false
	}
	servers, err := s.Manager.Servers()
	if err != nil {
		return false
	}
	for _, m := range servers {
		if _, _, ok := s.Manager.BackupTargetOf(r.Context(), m.ID); ok {
			return true
		}
	}
	return false
}

func (s *Server) backupsPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Backup"] = s.backupView(r)
	s.renderLive(w, r, "backups", "backup-card", data)
}

func (s *Server) domainDone(r *http.Request) bool {
	p, err := s.Setup.Progress(r.Context())
	return err == nil && p.DomainDone
}

// dnsView is the Owner's "home network access" card.
func (s *Server) dnsView(r *http.Request) any {
	if s.DNS == nil || !auth.Can(currentUser(r).Role, auth.ManageHost) {
		return nil
	}
	st := s.DNS.Status()
	return &st
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, "account", "account", map[string]any{"User": currentUser(r)})
}

// appearanceSubmit saves the user's colour theme and light/dark mode.
func (s *Server) appearanceSubmit(w http.ResponseWriter, r *http.Request) {
	palette, mode := r.FormValue("palette"), r.FormValue("mode")
	if !validPalette(palette) || !slices.Contains(store.Themes, mode) {
		http.Error(w, "Unknown theme.", http.StatusBadRequest)
		return
	}
	// Each theme with accents has its own set of radio buttons.
	accent := r.FormValue("accent-" + palette)
	if !validAccent(palette, accent) {
		http.Error(w, "Unknown accent colour.", http.StatusBadRequest)
		return
	}
	u := currentUser(r)
	if err := s.Store.SetPalette(r.Context(), u.ID, palette); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetTheme(r.Context(), u.ID, mode); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetAccent(r.Context(), u.ID, accent); err != nil {
		s.serverError(w, err)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		// Saved in the background; the page already shows it. Send back the
		// top bar's toggle so it matches.
		u.Theme, u.Palette = mode, palette
		n := navFor(u, "account")
		s.renderPart(w, "account", "theme-toggle", map[string]any{"OOB": true, "NextTheme": n.NextTheme, "Theme": n.Theme})
		return
	}
	http.Redirect(w, r, "/account/appearance", http.StatusSeeOther)
}

// themeSubmit saves the user's theme and returns to the page they were
// on (same origin only: Referrer-Policy is same-origin).
func (s *Server) themeSubmit(w http.ResponseWriter, r *http.Request) {
	theme := r.FormValue("theme")
	if !slices.Contains(store.Themes, theme) {
		http.Error(w, "Unknown theme.", http.StatusBadRequest)
		return
	}
	if err := s.Store.SetTheme(r.Context(), currentUser(r).ID, theme); err != nil {
		s.serverError(w, err)
		return
	}
	back := "/"
	if ref, err := url.Parse(r.Referer()); err == nil && ref.Host == r.Host && strings.HasPrefix(ref.Path, "/") {
		back = ref.Path
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// passwordSubmit changes the user's own password. All their sessions are
// ended (other devices must log in again); this one gets a new session.
func (s *Server) passwordSubmit(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		s.renderAccount(w, "account", "account", map[string]any{"User": u, "Error": msg})
	}
	ok, err := authkit.VerifyPassword(u.PasswordHash, r.FormValue("current"))
	if err != nil || !ok {
		fail("Your current password is wrong.")
		return
	}
	pw := r.FormValue("password")
	if pw != r.FormValue("confirm") {
		fail("The new passwords don't match.")
		return
	}
	if err := authkit.CheckPasswordPolicy(pw); err != nil {
		fail(capitalize(err.Error()) + ".")
		return
	}
	hash, err := authkit.HashPassword(pw)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.Username, hash, false); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.startSession(w, r, u, sessionOf(r).Remember); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "password changed", "")
	if u.MustChangePassword { // done with the temporary password: carry on
		redirect(w, r, "/")
		return
	}
	s.renderAccount(w, "account", "account", map[string]any{"User": u,
		"Done": "Password changed. Other devices you were logged in on need to log in again."})
}

// renderPart renders one named template from a page (htmx partials).
func (s *Server) renderPart(w http.ResponseWriter, page, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages[page].ExecuteTemplate(w, name, data); err != nil {
		s.Log.Error("render partial", "page", page, "part", name, "err", strings.TrimSpace(err.Error()))
	}
}

// Parts of pages that refresh themselves send a fingerprint of what they
// show (static/app.js); when it's still the same, the answer is 204 No
// Content and htmx leaves the page alone: nothing to download or redraw,
// and open panels, selections and scroll positions are untouched.
const (
	haveHeader        = "X-Roostack-Have"
	fingerprintHeader = "X-Roostack-Fingerprint"
)

// renderLive renders a self-refreshing part, or 204 if the page already
// shows exactly this.
func (s *Server) renderLive(w http.ResponseWriter, r *http.Request, page, name string, data any) {
	var buf bytes.Buffer
	if err := s.pages[page].ExecuteTemplate(&buf, name, data); err != nil {
		s.Log.Error("render partial", "page", page, "part", name, "err", strings.TrimSpace(err.Error()))
		return
	}
	h := fnv.New64a()
	h.Write(buf.Bytes())
	fp := strconv.FormatUint(h.Sum64(), 36)
	w.Header().Set(fingerprintHeader, fp)
	w.Header().Set("Cache-Control", "no-store")
	if r.Header.Get(haveHeader) == fp {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func (s *Server) applyStatus() *manager.ApplyStatus {
	if s.Manager == nil {
		return nil
	}
	st := s.Manager.Status()
	return &st
}
