package web

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
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

// Of the lab's servers, only the controller reaches SSH over Roostack's
// network (SPEC.md §10.2, review M23): the page says so in a lab with more
// than one server, names who's connected, and asks before cutting off a
// session from another server, whatever the choice.
func TestSSHLabServers(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: allDone, ssh: config.SSHFromLAN, clients: []netip.Addr{netip.MustParseAddr("192.168.1.20"),
		netip.MustParseAddr("100.96.0.4")}}
	h.setSetup(fs)
	_, body := h.get("/settings/remote-access")
	if strings.Contains(body, "Not from this lab's other servers") {
		t.Error("a lab of one server: nothing to say about other servers")
	}
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", Controller: true, This: true},
		{ID: "n-1", Name: "vps"}}
	_, body = h.get("/settings/remote-access")
	for _, want := range []string{"From computers at home, or over a VPN. Not from this lab's other servers.</small>",
		"<code>192.168.1.20</code> (your home network)", "<code>100.96.0.4</code> (another server in this lab, over Roostack&#39;s network)",
		`|| choice === 'lan'`, `|| choice === 'any'`} {
		if !strings.Contains(body, want) {
			t.Errorf("remote access page missing %q", want)
		}
	}
}

func TestSSHClientWhere(t *testing.T) {
	ctrl := netip.MustParseAddr("100.96.0.1")
	for _, c := range []struct {
		addr     string
		internet bool
		want     string
	}{
		{"100.96.0.1", false, "the server that runs this lab, over Roostack's network"},
		{"100.96.0.7", false, "another server in this lab, over Roostack's network"},
		{"100.96.128.7", false, "your home network"},
		{"192.168.1.5", false, "your home network"},
		{"203.0.113.9", false, "outside your home network"},
		{"10.0.0.5", true, "a private network"},
		{"203.0.113.9", true, "the internet"},
	} {
		if got := sshClientWhere(netip.MustParseAddr(c.addr), ctrl, c.internet); got != c.want {
			t.Errorf("%s: %q, want %q", c.addr, got, c.want)
		}
	}
}
