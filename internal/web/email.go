package web

import (
	"context"
	"errors"
	"net/http"
	netmail "net/mail"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/mail"
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
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	m, hasPassword := s.Mail.Settings()
	if m.Port == 0 {
		m.Port, m.Security = 587, config.MailSTARTTLS
	}
	data["Mail"], data["HasPassword"] = m, hasPassword
	data["TestTo"], data["Saved"] = currentUser(r).Email, r.URL.Query().Get("saved") == "1"
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
	var fe *mail.FormError
	if errors.As(err, &fe) {
		s.renderEmail(w, r, map[string]any{"Error": fe.Msg, "Mail": m})
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
	name := s.LabName
	if name == "" {
		name = "your lab"
	}
	err := s.Mail.Send(r.Context(), mail.Message{To: []string{to}, Subject: "Roostack test email",
		Body: "This is a test email from Roostack, for " + name + ".\n\n" +
			"It worked: Roostack can send email through this server. Nothing else to do.\n"})
	if err != nil {
		msg := "Couldn't send it: " + err.Error()
		var fe *mail.FormError
		switch {
		case errors.As(err, &fe):
			msg = fe.Msg
		case errors.Is(err, mail.ErrNotConfigured):
			msg = "Save the SMTP server's settings first."
		}
		s.renderEmail(w, r, map[string]any{"TestError": msg, "TestTo": to})
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "email", "test email sent to "+to)
	s.renderEmail(w, r, map[string]any{"TestSent": to, "TestTo": to})
}

// accountEmailSubmit sets where Roostack emails this person.
func (s *Server) accountEmailSubmit(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	email := strings.TrimSpace(r.FormValue("email"))
	if email != "" {
		if a, err := netmail.ParseAddress(email); err != nil || a.Address != email {
			w.WriteHeader(http.StatusBadRequest)
			s.render(w, "account", map[string]any{"User": u, "EmailError": "Enter an email address, such as sam@example.com.", "EmailValue": email})
			return
		}
	}
	if err := s.Store.SetEmail(r.Context(), u.ID, email); err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/account?email=saved#email")
}
