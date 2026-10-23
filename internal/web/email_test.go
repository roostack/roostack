package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
)

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
	if !strings.Contains(body, `value="587"`) || !strings.Contains(body, `name="security" value="starttls" checked`) || !strings.Contains(body, `class="field-row wide-first"`) ||
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

// Every Owner has an email address (SPEC.md §6.13): one without (made on
// the People page or the command line, or given the role later) is asked
// for it before anything else, and an Owner can't clear theirs. Others
// can have none.
func TestOwnerNeedsEmail(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	ctx := context.Background()
	u, _ := h.store.UserByUsername(ctx, "u")
	h.store.SetEmail(ctx, u.ID, "")
	landsOn := func(h *harness, path string) (string, string) {
		t.Helper()
		resp, err := h.client.Get(h.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.Request.URL.Path, string(body)
	}
	for _, path := range []string{"/", "/services", "/settings/people"} {
		if at, body := landsOn(h, path); at != "/account/notifications" || !strings.Contains(body, "Add yours below to continue") {
			t.Errorf("%s for an Owner without an address: ended at %s", path, at)
		}
	}
	if code, body := h.post("/account/notifications", url.Values{"email": {""}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "As the Owner, you need an email address") {
		t.Errorf("an Owner giving none: %d", code)
	}
	if _, body := h.post("/account/notifications", url.Values{"email": {"u@example.com"}, "notify": {"problems"}}, h.srv.URL); !strings.Contains(body, "<title>Home · Roostack</title>") {
		t.Error("with an address, the Owner carries on to Home")
	}
	// Clearing it later is refused too.
	if code, _ := h.post("/account/notifications", url.Values{"email": {""}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("an Owner clearing their address: %d, want 400", code)
	}
	if u, _ := h.store.UserByUsername(ctx, "u"); u.Email != "u@example.com" {
		t.Errorf("the Owner's address is now %q", u.Email)
	}

	// An Admin may have none.
	ha, _ := loggedIn(t, auth.RoleAdmin)
	if at, _ := landsOn(ha, "/"); at != "/" {
		t.Errorf("an Admin without an address: ended at %s", at)
	}
	if code, _ := ha.post("/account/notifications", url.Values{"email": {""}}, ha.srv.URL); code != http.StatusOK {
		t.Errorf("an Admin with no address: %d", code)
	}
}
