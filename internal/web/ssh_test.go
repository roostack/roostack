package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
)

// SSH keys only (SPEC.md §6.10 item 3): offered once a key login by an
// account that can use sudo has been seen, with a lasting warning while
// passwords are taken.
func TestSSHKeysOnlyPage(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: allDone, ssh: config.SSHFromLAN, sshLogin: config.SSHLoginPassword,
		sshd: agent.SSHStatus{Installed: true, Passwords: true, RootLogin: "prohibit-password",
			KeyLogins: []agent.KeyLogin{{User: "root", From: "192.168.1.30", At: time.Now()}}}}
	h.setSetup(fs)

	_, body := h.get("/settings/remote-access")
	for _, want := range []string{"How do they log in?", "hasn&#39;t seen anyone log in with an SSH key and an account that can use sudo",
		"they can't use sudo: root", `href="/help/start/first-run/#set-up-an-ssh-key"`, `<a href="/settings/remote-access">check again</a>`,
		`value="password" checked`, "Takes passwords", "with a key only (prohibit-password)", "<code>/etc/ssh/sshd_config.d/10-roostack.conf</code> (not there now)"} {
		if !strings.Contains(body, want) {
			t.Errorf("remote access page missing %q", want)
		}
	}
	_, body = h.get("/")
	if !strings.Contains(body, "SSH on this server still accepts passwords: anyone on your home network can try to guess one.") ||
		!strings.Contains(body, `href="/settings/remote-access"`) {
		t.Error("the home page warns while SSH takes passwords")
	}
	if _, body := h.get("/settings"); !strings.Contains(body, "SSH from your home network only, passwords allowed") {
		t.Error("Settings sums it up")
	}
	code, body := h.post("/setup/ssh", url.Values{"ssh": {"lan"}, "login": {"key"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "hasn&#39;t seen anyone") || !strings.Contains(body, `value="key" checked`) ||
		!strings.Contains(body, `<a href="/settings/remote-access">check again</a>`) {
		t.Errorf("keys only without a key login: %d", code)
	}

	fs.sshd.KeyLogins = append([]agent.KeyLogin{{User: "tom", From: "192.168.1.20", At: time.Now(), Sudo: true}}, fs.sshd.KeyLogins...)
	fs.sshLogin = ""
	_, body = h.get("/settings/remote-access")
	if !strings.Contains(body, "Your SSH key works: tom logged in with one from 192.168.1.20 today") || !strings.Contains(body, `value="key" checked`) {
		t.Error("a key login seen: keys only is offered, and the default")
	}
	if code, _ := h.post("/setup/ssh", url.Values{"ssh": {"any"}, "login": {"key"}}, h.srv.URL); code >= 400 {
		t.Fatalf("keys only: %d", code)
	}
	if fs.sshLogin != config.SSHLoginKey {
		t.Errorf("login = %q", fs.sshLogin)
	}
	if _, body := h.get("/"); strings.Contains(body, "accepts passwords") {
		t.Error("no warning once it's keys only")
	}
	if _, body := h.get("/settings"); !strings.Contains(body, "SSH from anywhere, keys only") {
		t.Error("Settings says keys only")
	}

	// From anywhere with passwords: a problem, not just a warning.
	fs.sshd.KeysOnly, fs.sshd.Passwords = false, true
	if _, body := h.get("/"); !strings.Contains(body, "SSH on this server accepts passwords from anywhere") {
		t.Error("anywhere with passwords")
	}
	// Nobody can reach SSH: nothing to warn about.
	fs.ssh = config.SSHOff
	if _, body := h.get("/"); strings.Contains(body, "accepts passwords") {
		t.Error("SSH off: no warning")
	}
}

// A server with no SSH server has nothing to choose about logging in.
func TestSSHNoServer(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: allDone, ssh: config.SSHFromLAN})
	if _, body := h.get("/settings/remote-access"); strings.Contains(body, "How do they log in?") {
		t.Error("no SSH server: no login choice")
	}
}
