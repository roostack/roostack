package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/instance"
)

// A service's Environment variables card (SPEC.md §8.1): the Owner's only;
// a variable added, a secret added and shown (recorded), one changed,
// removed, and the package's own refused keeping the name but not the
// value; then as text: checked first, with secrets guessed and problems
// stopping the save, and saved with the ticks as left.
func TestServiceEnv(t *testing.T) {
	ha, _ := loggedIn(t, auth.RoleAdmin)
	h, fm := loggedIn(t, auth.RoleOwner)
	for _, x := range []*harness{h, ha} {
		x.server.Services = func(context.Context) ([]ServiceView, error) {
			return []ServiceView{{Name: "forgejo", Title: "Forgejo", Status: "running", URLs: []string{"https://git.lab.example.com"}}}, nil
		}
	}
	if _, body := ha.get("/services/forgejo/settings"); strings.Contains(body, `id="advanced"`) {
		t.Error("an Admin sees the card")
	}
	if code, _ := ha.post("/services/forgejo/env", url.Values{"do": {"add"}, "var": {"A"}, "value": {"1"}}, ha.srv.URL); code != http.StatusForbidden {
		t.Errorf("an Admin's change: %d", code)
	}

	post := func(path string, v url.Values) (int, string) { return h.post("/services/forgejo"+path, v, h.srv.URL) }
	_, body := h.get("/services/forgejo/settings")
	for _, want := range []string{`id="advanced"`, `href="https://forgejo.org/docs/env"`, "<code>GITEA_APP_INI</code>",
		"<code>/var/lib/roostack/services/forgejo.yaml</code>", `name="text.app"`} {
		if !strings.Contains(body, want) {
			t.Errorf("card missing %q", want)
		}
	}
	post("/env", url.Values{"do": {"add"}, "var": {"FORGEJO__ui__DEFAULT_THEME"}, "value": {"forgejo-dark"}})
	post("/env", url.Values{"do": {"add"}, "var": {"FORGEJO__mailer__PASSWD"}, "value": {"app-pass-7Kq2"}, "secret": {"on"}})
	env := fm.env["forgejo"]
	if len(env) != 2 || env[0].Value != "forgejo-dark" || !env[1].Secret || env[1].Value != "" ||
		fm.envValues["app/FORGEJO__mailer__PASSWD"] != "app-pass-7Kq2" {
		t.Fatalf("added: %+v %v", env, fm.envValues)
	}
	_, body = h.get("/services/forgejo/settings")
	if !strings.Contains(body, "<code>forgejo-dark</code>") || !strings.Contains(body, `<span class="hint">Hidden</span>`) ||
		strings.Contains(body, "app-pass-7Kq2") {
		t.Error("the rows")
	}
	_, body = post("/env/show", url.Values{"container": {"app"}, "var": {"FORGEJO__mailer__PASSWD"}})
	if !strings.Contains(body, "<code>app-pass-7Kq2</code>") || !strings.Contains(body, "noted in the audit log") {
		t.Error("shown")
	}
	if !h.audited(t, "secret shown", "forgejo FORGEJO__mailer__PASSWD") {
		t.Error("showing wasn't recorded")
	}
	post("/env", url.Values{"do": {"set"}, "container": {"app"}, "var": {"FORGEJO__ui__DEFAULT_THEME"}, "value": {"forgejo-light"}})
	if fm.env["forgejo"][0].Value != "forgejo-light" {
		t.Errorf("changed: %+v", fm.env["forgejo"])
	}
	code, body := post("/env", url.Values{"do": {"add"}, "var": {"GITEA_APP_INI"}, "value": {"hunter2"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "sets GITEA_APP_INI itself") || !strings.Contains(body, `value="GITEA_APP_INI"`) ||
		strings.Contains(body, "hunter2") {
		t.Errorf("refused: %d", code)
	}

	// As text: what it holds now, the secret hidden.
	_, body = h.get("/services/forgejo/settings")
	if !strings.Contains(body, "FORGEJO__ui__DEFAULT_THEME=forgejo-light\nFORGEJO__mailer__PASSWD=‹hidden›\n") {
		t.Errorf("text:\n%s", body)
	}
	text := "FORGEJO__ui__DEFAULT_THEME=forgejo-dark\nFORGEJO__mailer__PASSWD=‹hidden›\nFORGEJO__metrics__TOKEN=f3b1\nGITEA_APP_INI=/x\n"
	_, body = post("/env/check", url.Values{"text.app": {text}})
	for _, want := range []string{"Check before saving", "Line 4: Forgejo&#39;s package sets GITEA_APP_INI itself", "guessed from its name",
		`value="app/FORGEJO__metrics__TOKEN" x-model="secret" checked`, "was <code>forgejo-light</code>", "keeps its value", "disabled>Save and restart"} {
		if !strings.Contains(body, want) {
			t.Errorf("check missing %q", want)
		}
	}
	text = strings.Replace(text, "GITEA_APP_INI=/x\n", "", 1)
	_, body = post("/env/check", url.Values{"text.app": {text}})
	if strings.Contains(body, "disabled>Save") {
		t.Error("save stays off without problems")
	}
	// Saved with the token unticked: plain.
	post("/env/text", url.Values{"text.app": {text}})
	env = fm.env["forgejo"]
	if len(env) != 3 || env[0].Value != "forgejo-dark" || !env[1].Secret || env[2].Secret || env[2].Value != "f3b1" {
		t.Fatalf("saved: %+v", env)
	}
	// Back to the text keeps what was typed.
	_, body = post("/env/text", url.Values{"text.app": {"KEEP_ME=1\n"}, "back": {"1"}})
	if !strings.Contains(body, "KEEP_ME=1") || strings.Contains(body, "Check before saving") {
		t.Error("back to the text")
	}
	if len(fm.env["forgejo"]) != 3 {
		t.Error("back saved something")
	}
	_ = instance.MaxEnv
}

// audited says whether the audit log has an entry with this action and
// detail.
func (h *harness) audited(t *testing.T, action, detail string) bool {
	t.Helper()
	entries, err := h.store.AuditSince(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action && e.Detail == detail {
			return true
		}
	}
	return false
}
