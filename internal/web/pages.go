package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/dns"
	"github.com/mylab/mylab/internal/hosttime"
	"github.com/mylab/mylab/internal/manager"
	"github.com/mylab/mylab/internal/pkgspec"
	"github.com/mylab/mylab/internal/store"
)

// nav is the top navigation, shown to logged-in users.
type nav struct {
	Active   string
	Username string
	Backups  bool
	Settings bool
}

// navFor picks the links a role can use and marks the current section.
func navFor(u *store.User, page string) nav {
	active := map[string]string{
		"home": "home", "services": "services", "service": "services", "catalog": "services",
		"install": "services", "remove": "services", "backups": "backups",
		"settings": "settings", "setup-timezone": "settings", "setup-ssh": "settings", "setup-domain": "settings", "setup-backup": "settings",
		"recovery-key": "settings", "restore": "backups", "dns-takeover": "settings", "account": "account", "twofactor": "account",
	}[page]
	return nav{Active: active, Username: u.Username,
		Backups: auth.Can(u.Role, auth.ManageServices), Settings: auth.Can(u.Role, auth.ManageHost)}
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
		"Apply":     s.applyStatus(),
	}, nil
}

func (s *Server) homePage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Attention"] = s.attention(r)
	s.render(w, "home", data)
}

func (s *Server) homePartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Attention"] = s.attention(r)
	s.renderPart(w, "home", "home-live", data)
}

func (s *Server) servicesPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if a := s.activityFor(r, activity.Apply); a != nil {
		data["ApplyActivity"] = map[string]any{"Activity": a}
	}
	s.render(w, "services", data)
}

func (s *Server) servicesPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPart(w, "services", "services-live", data)
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
		}
	}
	if data["Service"] == nil {
		http.NotFound(w, r)
		return
	}
	if s.Manager != nil {
		if p, err := s.Manager.Package(name); err == nil {
			data["Package"] = p
			var actions []serviceAction
			for _, h := range p.HooksOn(pkgspec.HookAction) {
				actions = append(actions, serviceAction{Name: h.Name, Title: h.Title, Description: h.Description, Fields: inputFields(h)})
			}
			data["Actions"] = actions
		}
		data["Setup"] = s.Manager.InstallStatus(r.Context(), name)
		// The latest setup log, if it's this service's.
		if a := s.activityFor(r, activity.Setup); a != nil && a.Title == "Setting up "+data["Service"].(ServiceView).Title {
			data["SetupActivity"] = map[string]any{"Activity": a}
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
	if failed {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "service", data)
}

func (s *Server) backupsPage(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Backup"] = s.backupView(r)
	data["BackupRepo"] = s.BackupRepo
	data["CanRestore"] = s.Restores != nil && auth.Can(currentUser(r).Role, auth.ManageHost)
	if a := s.activityFor(r, activity.Backup); a != nil {
		data["BackupActivity"] = map[string]any{"Activity": a}
	}
	s.render(w, "backups", data)
}

func (s *Server) backupsPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Backup"] = s.backupView(r)
	s.renderPart(w, "backups", "backup-card", data)
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	s.renderSettings(w, r, "")
}

// renderSettings shows the Settings page; updatesErr is shown on the
// updates card (with a 400 status).
func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, updatesErr string) {
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["DNS"] = s.dnsView(r)
	data["HostClock"] = hosttime.Now()
	data["Updates"] = s.updatesView(r, updatesErr)
	if updatesErr != "" {
		w.WriteHeader(http.StatusBadRequest)
	}
	if s.Setup != nil {
		if a, err := s.Setup.SSHAccess(); err == nil {
			data["SSH"] = a.Choice
		}
	}
	data["LabDomain"] = s.Domain
	data["DomainSet"] = s.Domain != "" && s.Setup != nil && s.domainDone(r)
	if p := dns.Get(s.DNSProvider); p != nil {
		data["ProviderTitle"] = p.Title
	}
	s.render(w, "settings", data)
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
	s.render(w, "account", map[string]any{"User": currentUser(r)})
}

// passwordSubmit changes the user's own password. All their sessions are
// ended (other devices must log in again); this one gets a new session.
func (s *Server) passwordSubmit(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	fail := func(msg string) {
		w.WriteHeader(http.StatusBadRequest)
		s.render(w, "account", map[string]any{"User": u, "Error": msg})
	}
	ok, err := auth.VerifyPassword(u.PasswordHash, r.FormValue("current"))
	if err != nil || !ok {
		fail("Your current password is wrong.")
		return
	}
	pw := r.FormValue("password")
	if pw != r.FormValue("confirm") {
		fail("The new passwords don't match.")
		return
	}
	if err := auth.CheckPasswordPolicy(pw); err != nil {
		fail(capitalize(err.Error()) + ".")
		return
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.Username, hash); err != nil {
		s.serverError(w, err)
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), u, "password changed", "")
	s.render(w, "account", map[string]any{"User": u,
		"Done": "Password changed. Other devices you were logged in on need to log in again."})
}

// renderPart renders one named template from a page (htmx partials).
func (s *Server) renderPart(w http.ResponseWriter, page, name string, data map[string]any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.pages[page].ExecuteTemplate(w, name, data); err != nil {
		s.Log.Error("render partial", "page", page, "part", name, "err", strings.TrimSpace(err.Error()))
	}
}

func (s *Server) applyStatus() *manager.ApplyStatus {
	if s.Manager == nil {
		return nil
	}
	st := s.Manager.Status()
	return &st
}
