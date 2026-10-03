package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/mail"
)

// fakeMail keeps settings and records messages.
type fakeMail struct {
	m        config.Mail
	password string
	sent     []mail.Message
	fail     error
}

func (f *fakeMail) Settings() (config.Mail, bool) { return f.m, f.password != "" }
func (f *fakeMail) Save(m config.Mail, password string, clear bool) error {
	if m.From == "" {
		return &mail.FormError{Msg: "Enter the address emails come from."}
	}
	f.m = m
	if clear {
		f.password = ""
	} else if password != "" {
		f.password = password
	}
	return nil
}
func (f *fakeMail) Send(_ context.Context, msg mail.Message) error {
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, msg)
	return nil
}

// Owners set the SMTP server (the password is never shown again) and send
// a test email; everyone sets their own address.
func TestEmailSettings(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fm := &fakeMail{}
	h.server.Mail = fm
	h.setSetup(&fakeSetup{p: allDone})
	if _, body := h.get("/settings"); !strings.Contains(body, "Not set up: Roostack can&#39;t send email") {
		t.Error("the overview says email isn't set up")
	}
	_, body := h.get("/settings/email")
	if !strings.Contains(body, `value="587"`) || !strings.Contains(body, `value="starttls" checked`) ||
		!strings.Contains(body, `name="from" value="no_reply@lab.example.com"`) {
		t.Errorf("defaults: %s", body)
	}
	form := url.Values{"host": {"smtp.example.com"}, "port": {"587"}, "security": {"starttls"}, "username": {"sam@example.com"},
		"password": {"app-password"}, "from": {"roostack@lab.example.com"}}
	h.post("/settings/email", form, h.srv.URL)
	if fm.m.Host != "smtp.example.com" || fm.password != "app-password" {
		t.Fatalf("saved %+v", fm.m)
	}
	if _, body = h.get("/settings/email"); strings.Contains(body, "app-password") || !strings.Contains(body, "Saved: leave empty to keep it") {
		t.Error("the password is never shown again")
	}
	form.Set("from", "")
	if code, body := h.post("/settings/email", form, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "come from") {
		t.Errorf("a bad setting: %d", code)
	}

	h.post("/account/notifications", url.Values{"email": {"sam@example.com"}, "notify": {"problems", "security"}}, h.srv.URL)
	if _, body = h.get("/settings/email"); !strings.Contains(body, `name="to" type="email" value="sam@example.com"`) {
		t.Error("the test goes to your own address by default")
	}
	_, body = h.post("/settings/email/test", url.Values{"to": {"sam@example.com"}}, h.srv.URL)
	if len(fm.sent) != 1 || fm.sent[0].To[0] != "sam@example.com" || !strings.Contains(body, "Sent to sam@example.com") {
		t.Errorf("test email: %+v", fm.sent)
	}
	fm.fail = errors.New("couldn't reach smtp.example.com:587: connection refused")
	if code, body := h.post("/settings/email/test", url.Values{"to": {"sam@example.com"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "Couldn&#39;t send it: couldn&#39;t reach smtp.example.com:587") {
		t.Errorf("failure: %d", code)
	}
	if _, body = h.get("/settings"); !strings.Contains(body, "Sends from roostack@lab.example.com") {
		t.Error("the overview says where email comes from")
	}

	if code, _ := h.post("/account/notifications", url.Values{"email": {"not an address"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("a bad address: %d", code)
	}
	ha, _ := loggedIn(t, auth.RoleAdmin)
	ha.server.Mail = fm
	if code, _ := ha.get("/settings/email"); code != http.StatusForbidden {
		t.Errorf("admins can't change the mail server: %d", code)
	}
}
