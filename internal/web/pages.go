package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/words"
)

// nav is the sidebar (docs/DESIGN.md "Layout"), shown to logged-in users.
type nav struct {
	Active   string
	Username string
	// Role is the person's role (auth.RoleOwner...), under their name.
	Role    string
	Backups bool
	// Monitoring: this server runs monitoring (the controller), shown to
	// those who see Backups (SPEC.md §6.15).
	Monitoring bool
	Settings   bool
	// SettingsAreas are Settings' pages, listed under Settings in the
	// sidebar while you're in Settings (for those who can change
	// settings). Area is the current one, if the page is one.
	SettingsAreas []settingsArea
	Area          string
	// Services lists the services under Services while you're there
	// (their pages, each with its logo or drawn icon; the current one
	// marked).
	Services []navLink
	// InService: the page is one service's (Services isn't the page itself);
	// Adding: it's adding one (the catalog, installing).
	InService, Adding bool
	// ServerIssues counts servers needing attention, shown beside Servers:
	// a change that didn't finish or not answering (the controller knows),
	// or what Home last found (Server.serverIssues).
	ServerIssues int
	// Landing is a main area's own page (Services, Settings...), whose
	// breadcrumbs are just its title.
	Landing bool
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
	landing := slices.Contains([]string{"home", "services", "backups", "settings", "settings-servers", "account", "monitoring"}, page)
	adding := page == "catalog" || page == "install" || page == "local-add" || page == "local-editor"
	return nav{Active: active, Landing: landing, Adding: adding, Username: u.Username, Role: u.Role, Theme: u.Theme, NextTheme: next, Palette: userPalette(u), Accent: userAccent(u),
		Backups: auth.Can(u.Role, auth.ManageServices), Settings: auth.Can(u.Role, auth.ManageHost)}
}

// navLink is a link in the sidebar: Logo is its image's address, if it
// has one, and Icon its drawn icon otherwise (icons.go).
type navLink struct {
	Href, Title string
	Logo, Icon  string
	Current     bool
}

// crumb is a link in the page header's breadcrumbs.
type crumb struct{ Href, Title string }

// Section is the main area a page belongs to (nav.Active), for the
// breadcrumbs: where the first crumb leads. Home has none.
func (n nav) Section() *crumb {
	switch n.Active {
	case "services":
		return &crumb{"/services", "Services"}
	case "backups":
		return &crumb{"/backups", "Backups"}
	case "servers":
		return &crumb{"/servers", "Servers"}
	case "monitoring":
		return &crumb{"/monitoring", "Monitoring"}
	case "settings":
		return &crumb{"/settings", "Settings"}
	case "account":
		return &crumb{"/account", "Your account"}
	}
	return nil
}

// navTab is the sidebar's main area a page belongs to: by name for the few that
// don't follow a pattern, then by prefix, so new pages get it right
// (TestNavTabs checks every template).
func navTab(page string) string {
	if tab, ok := map[string]string{
		"home": "home", "pause": "home", "pause-close": "home", "catalog": "services", "local-add": "services", "local-editor": "services", "install": "services", "remove": "services", "move": "services", "visitors": "services",
		"import": "services", "import-folders": "services",
		"restore": "backups", "recovery-key": "settings", "users": "settings", "dns-takeover": "settings",
		"account": "account", "twofactor": "account", "account-appearance": "account", "account-notifications": "account",
		"cloud-open": "settings", "cloud-linked": "settings",
		// Servers is a main area (docs/DESIGN.md "Layout"), though its pages
		// are still under /settings.
		"settings-servers": "servers", "settings-server-gone": "servers", "settings-server-unreachable": "servers",
		"setup-restore": "", "setup-joined": "", // first run, before anyone is logged in
		"error": "", // any page's error (errors.go)
	}[page]; ok {
		return tab
	}
	for _, p := range []struct{ prefix, tab string }{
		{"settings", "settings"}, {"setup-", "settings"}, {"service", "services"}, {"backups", "backups"}, {"monitoring", "monitoring"},
		{"photos", "services"},
	} {
		if strings.HasPrefix(page, p.prefix) {
			return p.tab
		}
	}
	return ""
}

// withTiers sets each service's Tier from the service registry's index
// (SPEC.md §8.3.4): the registry's say now, so a package that becomes
// Official shows as Official. The Owner's own packages have none.
func (s *Server) withTiers(services []ServiceView) {
	if s.Registry == nil {
		return
	}
	listing, err := s.Registry.Listing()
	if err != nil {
		s.Log.Warn("reading the service registry's index", "err", err)
		return
	}
	for i := range services {
		if l, ok := listing[services[i].Package]; ok && !services[i].Local && l.Package != nil {
			services[i].Tier = l.Tier
		}
	}
}

// withIcons sets each service's logo from its package (if it has one and
// the person may fetch it: the logo's address is for those who manage
// services) and the drawn icon of its first category otherwise.
func (s *Server) withIcons(services []ServiceView, logos bool) {
	items := map[string]manager.CatalogItem{}
	if len(services) > 0 {
		if catalog, err := s.Manager.Catalog(); err == nil {
			for _, it := range catalog {
				items[it.Name] = it
			}
		}
	}
	for i := range services {
		it := items[services[i].Package]
		services[i].DrawnIcon = categoryIcon(it.Categories)
		if logos && it.Icon {
			services[i].IconURL = "/install/" + it.Name + "/icon.png"
		}
	}
}

// pageData collects what most pages show, for the current user. The
// lab's services aren't among them: listing them asks every server, so a
// page that shows them adds them (withServices), and the sidebar fetches
// them for its list under Services (render).
func (s *Server) pageData(r *http.Request) map[string]any {
	u := currentUser(r)
	return map[string]any{
		"User":          u,
		pageServicesKey: &pageServices{fetch: func() ([]ServiceView, error) { return s.servicesFor(r) }},
		"CanManage":     auth.Can(u.Role, auth.ManageServices),
		"IsOwner":       auth.Can(u.Role, auth.ManageHost),
		"Internet":      s.onInternet(),
		"Apply":         s.applyStatus(),
		"Server":        s.serverRefOf(r),
	}
}

// pageServicesKey is where pageData keeps the lab's services for a page,
// until they're asked for.
const pageServicesKey = "pageServices"

// pageServices are the lab's services for one page, fetched the first
// time they're asked for.
type pageServices struct {
	once  sync.Once
	fetch func() ([]ServiceView, error)
	list  []ServiceView
	err   error
}

func (p *pageServices) get() ([]ServiceView, error) {
	p.once.Do(func() { p.list, p.err = p.fetch() })
	return p.list, p.err
}

// pageDataWithServices is pageData with the lab's services (as
// Services), for a page that shows them; on an error it has sent the
// error page.
func (s *Server) pageDataWithServices(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	data := s.pageData(r)
	if _, err := withServices(data); err != nil {
		s.serverError(w, err)
		return nil, false
	}
	return data, true
}

// withServices adds the lab's services to a page's data (as Services), for
// a page that shows them, and returns them.
func withServices(data map[string]any) ([]ServiceView, error) {
	p, ok := data[pageServicesKey].(*pageServices)
	if !ok {
		return nil, errors.New("page data without its services")
	}
	services, err := p.get()
	if err != nil {
		return nil, err
	}
	data["Services"] = services
	return services, nil
}

// servicesFor are the lab's services the person r comes from can use,
// with their registry tiers and icons.
func (s *Server) servicesFor(r *http.Request) ([]ServiceView, error) {
	u := currentUser(r)
	services, err := s.Services(r.Context())
	if err != nil {
		return nil, err
	}
	s.withTiers(services)
	s.withIcons(services, auth.Can(u.Role, auth.ManageServices))
	return s.usableBy(r.Context(), u, services), nil
}

// serviceByName is the lab's service called name, as the dashboard lists
// it, and whether there is one.
func (s *Server) serviceByName(ctx context.Context, name string) (ServiceView, bool, error) {
	services, err := s.Services(ctx)
	if err != nil {
		return ServiceView{}, false, err
	}
	for _, svc := range services {
		if svc.Name == name {
			return svc, true, nil
		}
	}
	return ServiceView{}, false, nil
}

func (s *Server) homePage(w http.ResponseWriter, r *http.Request) {
	data, ok := s.pageDataWithServices(w, r)
	if !ok {
		return
	}
	s.homeData(r, data)
	// What an apply is doing (downloading, building), or why it failed
	// lately: nothing should look like magic.
	if a := s.activityFor(r, activity.Apply); a != nil && a.Recent(24*time.Hour) && (a.Running() || a.Err != "") {
		data["ApplyActivity"] = a
	}
	s.render(w, "home", data)
}

func (s *Server) homePartial(w http.ResponseWriter, r *http.Request) {
	data, ok := s.pageDataWithServices(w, r)
	if !ok {
		return
	}
	s.homeData(r, data)
	s.renderLive(w, r, "home", "home-live", data)
}

// homeData adds what Home shows besides the services: what needs
// attention, and for those who manage services, the servers and the
// figures across the top (docs/DESIGN.md "Layout").
func (s *Server) homeData(r *http.Request, data map[string]any) {
	data["Attention"] = s.AttentionLab().For(r.Context(), currentUser(r))
	// Members see a link to migrate their photos into each Immich they may
	// (SPEC.md §8.5).
	if u := currentUser(r); !isAdmin(u) {
		m := map[string]bool{}
		for _, ps := range s.migratable(r.Context(), u) {
			m[ps.Name] = true
		}
		data["Migratable"] = m
	}
	if data["CanManage"] == true {
		s.pauseData(data)
		servers := s.serverViews(r)
		data["Servers"] = servers
		f := homeFiguresOf(data["Services"].([]ServiceView), servers)
		nodes := s.serverNodes()
		disks := s.homeDisks(r, nodes)
		f.addDisks(disks)
		// A server with a disk running low needs attention too.
		low := map[string]bool{}
		for _, sd := range disks {
			if sd != nil && sd.Summary().Level != "" {
				low[sd.ServerID] = true
			}
		}
		f.ServerIssues = 0
		for _, v := range servers {
			if !v.Reachable || v.Status.LastErr != nil || v.RestartNeeded || v.UpdatesFailed || low[v.ID] {
				f.ServerIssues++
			}
		}
		s.issues.mu.Lock()
		s.issues.n, s.issues.at = f.ServerIssues, time.Now()
		s.issues.mu.Unlock()
		data["Figures"] = f
		// Each server's disks together, for its row in the table.
		perServer := map[string]diskSummary{}
		for _, sd := range disks {
			if sd != nil {
				perServer[sd.ServerID] = sd.Summary()
			}
		}
		data["ServerDisks"] = perServer
		// What Roostack did lately on the servers, for the Owner (as
		// History shows it; what people did stays there).
		if data["IsOwner"] == true {
			entries, _ := s.historyEntries(r, nodes, "", time.Time{})
			var recent []historyEntry
			for _, e := range entries {
				if e.Kind != "people" && len(recent) < 5 {
					recent = append(recent, e)
				}
			}
			data["Recent"] = recent
		}
	}
	data["Greeting"] = greeting(time.Now())
}

// serverIssuesFresh is how long Home's count of servers needing attention
// shows in the sidebar.
const serverIssuesFresh = 10 * time.Minute

// serverIssues is how many servers needed attention when Home last asked
// them all (a restart due, an OS update or change that didn't finish, a
// disk running low, not answering), for the sidebar's count beside
// Servers without asking each server on every page.
type serverIssues struct {
	mu sync.Mutex
	n  int
	at time.Time
}

// diskSummary is a server's disks together: used of total, and the
// worst level among them.
type diskSummary struct {
	Used, Total int64
	Percent     int
	Level       string
}

// Summary is a server's disks together, for a row in a table.
func (sd *serverDisks) Summary() diskSummary {
	var sum diskSummary
	for _, d := range sd.Disks {
		sum.Total += d.Total
		sum.Used += d.Used
		if d.Level != "" {
			sum.Level = d.Level
		}
	}
	sum.Percent = percentOf(sum.Used, sum.Total)
	return sum
}

// greeting says hello for the time of day (the server's, like every
// time Roostack shows).
func greeting(t time.Time) string {
	switch h := t.Hour(); {
	case h >= 5 && h < 12:
		return "Good morning."
	case h >= 12 && h < 18:
		return "Good afternoon."
	}
	return "Good evening."
}

// homeDisks asks every server for its disks at once (nil for one that
// doesn't answer in time).
func (s *Server) homeDisks(r *http.Request, nodes []serverNode) []*serverDisks {
	out := make([]*serverDisks, len(nodes))
	var wg sync.WaitGroup
	for i, mn := range nodes {
		wg.Go(func() { out[i] = s.serverSpaces(r.Context(), mn.view, mn.node) })
	}
	wg.Wait()
	return out
}

// addDisks adds the lab's disk space to the figures: every disk of every
// server that answered, and the first one running low.
func (f *homeFigures) addDisks(servers []*serverDisks) {
	for _, sd := range servers {
		if sd == nil {
			continue
		}
		for _, d := range sd.Disks {
			f.Disks++
			f.DiskTotal += d.Total
			f.DiskUsed += d.Used
			if d.Level != "" && f.DiskWarning == "" {
				f.DiskWarning = d.Warning
			}
		}
	}
}

// homeFigures are Home's figures strip: how many services run, how many
// servers answer, and the oldest of the servers' last backups (so "last
// backup" is true of every server).
type homeFigures struct {
	Services, Running, Busy, Problems int
	// ServerIssues counts servers needing attention: one that didn't
	// answer, a change or OS update that didn't finish, a restart due, a
	// disk running low.
	Servers, Online, ServerIssues int
	LastBackup                    time.Time
	// NotBackedUp counts servers with no backup yet.
	NotBackedUp int
	// Disks, DiskTotal and DiskUsed are every server's disks together;
	// DiskWarning says the first one running low.
	Disks               int
	DiskTotal, DiskUsed int64
	DiskWarning         string
}

func homeFiguresOf(services []ServiceView, servers []serverView) homeFigures {
	f := homeFigures{Services: len(services), Servers: len(servers)}
	for _, sv := range services {
		switch sv.Status {
		case "running":
			f.Running++
		case "installing", "updating", "starting":
			f.Busy++
		default:
			f.Problems++
		}
	}
	for _, v := range servers {
		if v.Reachable {
			f.Online++
		}
		if !v.Reachable || v.Status.LastErr != nil || v.RestartNeeded || v.UpdatesFailed {
			f.ServerIssues++
		}
		switch {
		case v.LastBackup.IsZero():
			f.NotBackedUp++
		case f.LastBackup.IsZero() || v.LastBackup.Before(f.LastBackup):
			f.LastBackup = v.LastBackup
		}
	}
	return f
}

func (s *Server) servicesPage(w http.ResponseWriter, r *http.Request) {
	data, ok := s.pageDataWithServices(w, r)
	if !ok {
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
	s.pauseData(data)
	// Visitors through the relay, for those who manage services.
	data["Relayed"] = s.Manager.RelayOn() && auth.Can(currentUser(r).Role, auth.ManageServices)
	s.render(w, "services", data)
}

func (s *Server) servicesPartial(w http.ResponseWriter, r *http.Request) {
	data, ok := s.pageDataWithServices(w, r)
	if !ok {
		return
	}
	s.addBuiltIns(r, data)
	s.pauseData(data)
	s.renderLive(w, r, "services", "services-live", data)
}

// addBuiltIns adds Roostack's own parts on every server (?builtin=1): its
// proxy, backup stores, the roostack service and agent, network links.
func (s *Server) addBuiltIns(r *http.Request, data map[string]any) {
	show := r.URL.Query().Get("builtin") == "1"
	data["ShowBuiltIn"] = show
	if show && data["CanManage"] == true {
		data["BuiltIns"] = s.Manager.AllBuiltIns(r.Context())
	}
}

func (s *Server) backupsPage(w http.ResponseWriter, r *http.Request) {
	data := s.pageData(r)
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
	data["RecoveryKey"] = owner && s.hasRecoveryKey()
	if data["RecoveryKey"] == true {
		if k, err := s.Keys.KeyChangeStatus(); err == nil {
			data["KeyChange"] = k
		}
	}
	if a := s.activityFor(r, activity.Backup); a != nil {
		data["BackupActivity"] = a
	}
	// Every service in the lab can be restored, from any backup of it
	// (SPEC.md §10.10), listed by the server it runs on.
	all, err := withServices(data)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["RestoreGroups"] = s.servicesByServer(all)
	// Where backups go: the plan (SPEC.md §6.9).
	if auth.Can(currentUser(r).Role, auth.ManageHost) {
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
	if ms, err := s.Manager.Servers(); err == nil && len(ms) > 1 {
		for _, m := range ms {
			order = append(order, m.Name)
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
	data := s.pageData(r)
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
	s.renderAccount(w, "account", "account", map[string]any{"User": currentUser(r), "Action": withQuery("/account/password", nextQuery(r))})
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
		s.renderAccount(w, "account", "account", map[string]any{"User": u, "Error": msg, "Action": withQuery("/account/password", nextQuery(r))})
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
		fail(words.Capitalise(err.Error()) + ".")
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
		redirect(w, r, nextOf(r))
		return
	}
	s.renderAccount(w, "account", "account", map[string]any{"User": u, "Action": "/account/password",
		"Done": "Password changed. Other devices you were logged in on will need to log in again."})
}

func (s *Server) applyStatus() *manager.ApplyStatus {
	st := s.Manager.Status()
	return &st
}
