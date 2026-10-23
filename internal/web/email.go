package web

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	netmail "net/mail"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/cloud"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/mail"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

// Email (SPEC.md §6.13): the SMTP server Roostack sends through (Settings
// → Email, Owners), and each person's address (their account page).

// MailSender is what the UI needs from internal/mail.
type MailSender interface {
	Settings() (config.Mail, bool)
	Save(m config.Mail, password string, clearPassword bool) error
	Send(ctx context.Context, msg mail.Message) error
}

func (s *Server) emailPage(w http.ResponseWriter, r *http.Request) {
	s.renderEmail(w, r, nil)
}

func (s *Server) renderEmail(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	if s.Mail == nil {
		http.NotFound(w, r)
		return
	}
	data := s.pageData(r)
	m, hasPassword := s.Mail.Settings()
	if m.Port == 0 {
		m.Port, m.Security = 587, config.MailSTARTTLS
	}
	// Suggested sender: an address on the lab's own domain.
	if m.From == "" && s.Domain != "" && s.domainDone(r) {
		m.From = "no_reply@" + s.Domain
	}
	data["Mail"], data["HasPassword"] = m, hasPassword
	data["TestTo"], data["Saved"] = currentUser(r).Email, r.URL.Query().Get("saved") == "1"
	// Who gets what: each person's address and groups, and how those
	// getting problems stand with Roostack Cloud (SPEC.md §6.6).
	var cloudSt cloud.Status
	if s.Cloud != nil {
		cloudSt = s.Cloud.Status()
	}
	data["CloudLinked"] = cloudSt.Linked
	if users, err := s.Store.ListUsers(r.Context()); err == nil {
		type recipient struct {
			Name, Role, Email string
			Groups            []string
			// Cloud is how the address stands with Roostack Cloud;
			// CloudNext: Cloud hasn't been told it yet.
			Cloud     *cloudapi.RecipientStatus
			CloudNext bool
		}
		var list []recipient
		for i := range users {
			u := &users[i]
			chosen := attention.UserGroups(u)
			var groups []string
			for _, g := range attention.GroupsFor(u.Role) {
				if chosen[g.ID] {
					groups = append(groups, g.Title)
				}
			}
			rc := recipient{Name: u.Username, Role: u.Role, Email: u.Email, Groups: groups}
			if cloudSt.Linked && u.Email != "" && chosen[attention.GroupProblems] {
				if j := slices.IndexFunc(cloudSt.Recipients, func(c cloudapi.RecipientStatus) bool { return strings.EqualFold(c.Email, u.Email) }); j >= 0 {
					rc.Cloud = &cloudSt.Recipients[j]
				} else {
					rc.CloudNext = true
				}
			}
			list = append(list, rc)
		}
		data["Recipients"] = list
	}
	for k, v := range extra {
		data[k] = v
	}
	if extra["Error"] != nil || extra["TestError"] != nil {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.render(w, "settings-email", withArea(data, "email"))
}

// emailSubmit saves the SMTP server's settings.
func (s *Server) emailSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Mail == nil {
		http.NotFound(w, r)
		return
	}
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("port")))
	m := config.Mail{Host: strings.TrimSpace(r.FormValue("host")), Port: port, Security: r.FormValue("security"),
		Username: strings.TrimSpace(r.FormValue("username")), From: strings.TrimSpace(r.FormValue("from"))}
	err := s.Mail.Save(m, r.FormValue("password"), r.FormValue("clear_password") == "on")
	if msg, ok := usererr.Message(err); ok {
		s.renderEmail(w, r, map[string]any{"Error": msg, "Mail": m})
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "email", "SMTP server set to "+m.Host)
	redirect(w, r, "/settings/email?saved=1")
}

// emailTestSubmit sends a test email, so the Owner knows it works.
func (s *Server) emailTestSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Mail == nil {
		http.NotFound(w, r)
		return
	}
	to := strings.TrimSpace(r.FormValue("to"))
	name := s.labName()
	if name == "" {
		name = "your lab"
	}
	err := s.Mail.Send(r.Context(), mail.Message{To: []string{to}, Subject: "Roostack test email",
		Body: "This is a test email from Roostack, for " + name + ".\n\n" +
			"It worked: Roostack can send email through this server. Nothing else to do.\n"})
	if err != nil {
		msg := "Couldn't send it: " + err.Error()
		if m, ok := usererr.Message(err); ok {
			msg = m
		} else if errors.Is(err, mail.ErrNotConfigured) {
			msg = "Save the SMTP server's settings first."
		}
		s.renderEmail(w, r, map[string]any{"TestError": msg, "TestTo": to})
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "email", "test email sent to "+to)
	s.renderEmail(w, r, map[string]any{"TestSent": to, "TestTo": to})
}

// The account's areas (a sidebar like Settings', tabs on phones).
var accountAreas = []settingsArea{
	{"account", "", "Your account", "/account"},
	{"2fa", "", "Two-factor", "/account/2fa"},
	{"notifications", "", "Email and notifications", "/account/notifications"},
	{"appearance", "", "Appearance", "/account/appearance"},
}

// renderAccount renders one of the account's pages, with its sidebar.
func (s *Server) renderAccount(w http.ResponseWriter, page, area string, data map[string]any) {
	data["AreaNav"] = areaNav{Label: "Your account", Groups: []settingsGroup{{Areas: accountAreas}}, Active: area, Tabs: true}
	s.render(w, page, data)
}

func (s *Server) appearancePage(w http.ResponseWriter, r *http.Request) {
	s.renderAccount(w, "account-appearance", "appearance", map[string]any{"User": currentUser(r)})
}

// emailProblem says what's wrong with an email address someone typed, or
// "" if nothing is.
func emailProblem(email string) string {
	if a, err := netmail.ParseAddress(email); err != nil || a.Address != email {
		return "Enter an email address, such as sam@example.com."
	}
	return ""
}

// ownerNeedsEmail reports whether u must give an email address before
// anything else: every Owner has one, so the lab can reach them when it
// needs them (SPEC.md §6.13).
func ownerNeedsEmail(u *store.User) bool {
	return u.Role == auth.RoleOwner && u.Email == ""
}

func (s *Server) notificationsPage(w http.ResponseWriter, r *http.Request) {
	s.renderNotifications(w, r, map[string]any{"Saved": r.URL.Query().Get("saved") == "1"})
}

func (s *Server) renderNotifications(w http.ResponseWriter, r *http.Request, extra map[string]any) {
	u := currentUser(r)
	ready := false
	if s.Mail != nil {
		m, _ := s.Mail.Settings()
		ready = m.Configured()
	}
	data := map[string]any{"User": u, "Groups": attention.GroupsFor(u.Role), "Chosen": attention.UserGroups(u), "MailReady": ready,
		"Owner": auth.Can(u.Role, auth.ManageHost), "NeedEmail": ownerNeedsEmail(u),
		"Action": withQuery("/account/notifications", nextQuery(r))}
	for k, v := range extra {
		data[k] = v
	}
	if extra["EmailError"] != nil {
		w.WriteHeader(http.StatusBadRequest)
	}
	s.renderAccount(w, "account-notifications", "notifications", data)
}

// notificationsSubmit saves where Roostack emails this person, and which
// groups of notifications they get (SPEC.md §6.13).
func (s *Server) notificationsSubmit(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	r.ParseForm()
	email := strings.TrimSpace(r.FormValue("email"))
	msg := ""
	switch {
	case email == "" && u.Role == auth.RoleOwner:
		msg = "As the Owner, you need an email address, so Roostack can tell you when the lab needs you."
	case email != "":
		msg = emailProblem(email)
	}
	if msg != "" {
		s.renderNotifications(w, r, map[string]any{"EmailError": msg, "EmailValue": email})
		return
	}
	var chosen []string
	for _, g := range attention.GroupsFor(u.Role) {
		if slices.Contains(r.Form["notify"], g.ID) {
			chosen = append(chosen, g.ID)
		}
	}
	groups := strings.Join(chosen, ",")
	if groups == "" {
		groups = "none"
	}
	if err := s.Store.SetEmail(r.Context(), u.ID, email); err != nil {
		s.serverError(w, err)
		return
	}
	// Audited: a changed address also redirects later security emails.
	if email != u.Email {
		s.Store.Audit(r.Context(), u, "account", "notification email changed to "+cmp.Or(email, "none"))
	}
	if err := s.Store.SetNotify(r.Context(), u.ID, groups); err != nil {
		s.serverError(w, err)
		return
	}
	if ownerNeedsEmail(u) { // asked for it before anything else: carry on
		redirect(w, r, nextOf(r))
		return
	}
	redirect(w, r, "/account/notifications?saved=1")
}
