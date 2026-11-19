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
	"roostack.cloud/roostack/internal/setup"
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
		"they can't use sudo: root", `<a href="#key-guide">Set up a key</a>`, `<h2>Set up an SSH key</h2>`,
		`value="password" checked`, "Takes passwords", "with a key only (prohibit-password)", "<code>/etc/ssh/sshd_config.d/10-roostack.conf</code> (not there now)"} {
		if !strings.Contains(body, want) {
			t.Errorf("remote access page missing %q", want)
		}
	}
	_, body = h.get("/partials/home")
	if !strings.Contains(body, "SSH on this server still accepts passwords: anyone on your home network can try to guess one.") ||
		!strings.Contains(body, `href="/settings/remote-access"`) {
		t.Error("the home page warns while SSH takes passwords")
	}
	if _, body := h.get("/settings"); !strings.Contains(body, "SSH from your home network only, passwords allowed") {
		t.Error("Settings sums it up")
	}
	code, body := h.post("/setup/ssh", url.Values{"ssh": {"lan"}, "login": {"key"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "hasn&#39;t seen anyone") || !strings.Contains(body, `value="key" checked`) ||
		!strings.Contains(body, `<a href="#key-guide">Set up a key</a>`) {
		t.Errorf("keys only without a key login: %d", code)
	}

	fs.sshd.KeyLogins = append([]agent.KeyLogin{{User: "tom", From: "192.168.1.20", At: time.Now(), Sudo: true}}, fs.sshd.KeyLogins...)
	fs.sshLogin = ""
	_, body = h.get("/settings/remote-access")
	if !strings.Contains(body, "Your SSH key works: tom logged in with one from 192.168.1.20 today") || !strings.Contains(body, `value="key" checked`) {
		t.Error("a key login seen: keys only is offered, and the default")
	}
	if strings.Contains(body, "Set up an SSH key") {
		t.Error("the key guide is shown once a key works")
	}
	if code, _ := h.post("/setup/ssh", url.Values{"ssh": {"any"}, "login": {"key"}}, h.srv.URL); code >= 400 {
		t.Fatalf("keys only: %d", code)
	}
	if fs.sshLogin != config.SSHLoginKey {
		t.Errorf("login = %q", fs.sshLogin)
	}
	if _, body := h.get("/partials/home"); strings.Contains(body, "accepts passwords") {
		t.Error("no warning once it's keys only")
	}
	if _, body := h.get("/settings"); !strings.Contains(body, "SSH from anywhere, keys only") {
		t.Error("Settings says keys only")
	}

	// From anywhere with passwords: a problem, not just a warning.
	fs.sshd.KeysOnly, fs.sshd.Passwords = false, true
	if _, body := h.get("/partials/home"); !strings.Contains(body, "SSH on this server accepts passwords from anywhere") {
		t.Error("anywhere with passwords")
	}
	// Once the agent has counted a whole day, the warning says how much
	// guessing there was.
	fs.sshd.Guesses, fs.sshd.GuessAddresses = 27511, 98
	if _, body := h.get("/partials/home"); strings.Contains(body, "guesses") {
		t.Error("a count shown before the day was counted")
	}
	fs.sshd.GuessesCounted = true
	if _, body := h.get("/partials/home"); !strings.Contains(body, "can try to guess one, and in the last day there were 27,511 guesses from 98 addresses.") {
		t.Error("the guesses aren't counted in the warning")
	}
	if _, body := h.get("/settings/remote-access"); !strings.Contains(body, "<dt>Password guesses</dt>") || !strings.Contains(body, "27,511 in the last day, from 98 addresses") {
		t.Error("the remote access page doesn't count the guesses")
	}
	// Blocked guessers: counted on Home, listed with Unblock.
	until := time.Now().Add(time.Hour)
	fs.sshd.Blocked = []agent.SSHBlock{{Address: "203.0.113.9", Until: until}, {Address: "2001:db8:1:2::/64", Until: until}}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "from 98 addresses, and Roostack has blocked 2 of them for now.") {
		t.Error("blocks aren't counted on Home")
	}
	_, body = h.get("/settings/remote-access")
	for _, want := range []string{"<h2>Blocked for now</h2>", "these addresses from SSH after too many wrong passwords",
		"<code>2001:db8:1:2::/64</code>", `action="/settings/remote-access/unblock"`, `name="address" value="203.0.113.9"`, "<dt>Blocking</dt>"} {
		if !strings.Contains(body, want) {
			t.Errorf("remote access page missing %q", want)
		}
	}
	// CrowdSec's community list at SSH: counted in the details.
	if strings.Contains(body, "<dt>Community's list</dt>") {
		t.Error("the community's list shown before any came")
	}
	fs.sshd.Community = 4172
	if _, body := h.get("/settings/remote-access"); !strings.Contains(body, "4,172 addresses on CrowdSec's community blocklist can't reach SSH either") {
		t.Error("the remote access page doesn't count the community's list")
	}
	if code, _ := h.post("/settings/remote-access/unblock", url.Values{"address": {"203.0.113.9"}}, h.srv.URL); code >= 400 {
		t.Fatalf("unblock: %d", code)
	}
	if len(fs.sshd.Blocked) != 1 || fs.sshd.Blocked[0].Address != "2001:db8:1:2::/64" {
		t.Errorf("after unblocking = %+v", fs.sshd.Blocked)
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "Roostack has blocked one of them for now.") {
		t.Error("one block")
	}
	fs.sshd.Blocked = nil
	fs.sshd.Guesses, fs.sshd.GuessAddresses = 1, 1
	if _, body := h.get("/partials/home"); !strings.Contains(body, "there was 1 guess from 1 address.") {
		t.Error("one guess")
	}
	// Nobody can reach SSH: nothing to warn about.
	fs.ssh = config.SSHOff
	if _, body := h.get("/partials/home"); strings.Contains(body, "accepts passwords") {
		t.Error("SSH off: no warning")
	}
}

// The key guide: it shows how to make a key, offers a new account named
// after the Owner or one in the sudo group already, keeps what was typed
// (never the password) when something's wrong, and once the key is added
// waits for a login with it, then says it works.
func TestSSHKeyGuide(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: allDone, ssh: config.SSHFromAny, sshLogin: config.SSHLoginPassword,
		sshd: agent.SSHStatus{Installed: true, Passwords: true, Sudoers: []string{"debian"}}}
	h.setSetup(fs)
	_, body := h.get("/settings/remote-access")
	for _, want := range []string{"ssh-keygen -t ed25519", `type $env:USERPROFILE\.ssh\id_ed25519.pub`, "pbcopy &lt; ~/.ssh/id_ed25519.pub",
		`action="/settings/remote-access/key"`, `data-account="new"`, `name="user" value="u"`, "<option selected>debian</option>",
		"useradd --create-home --groups sudo"} {
		if !strings.Contains(body, want) {
			t.Errorf("key guide missing %q", want)
		}
	}

	form := url.Values{"key": {"ssh-ed25519 AAAA tom@laptop"}, "account": {"new"}, "user": {"tom"}, "password": {"a long password"}, "confirm": {"another"}}
	code, body := h.post("/settings/remote-access/key", form, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(body, "The passwords don&#39;t match.") || !strings.Contains(body, "ssh-ed25519 AAAA tom@laptop</textarea>") ||
		!strings.Contains(body, `name="user" value="tom"`) || strings.Contains(body, "a long password") || len(fs.sshKeys) != 0 {
		t.Errorf("passwords that don't match: %d", code)
	}
	form.Set("key", "not a key")
	form.Set("confirm", "a long password")
	if code, body := h.post("/settings/remote-access/key", form, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "That isn&#39;t an SSH public key.") {
		t.Errorf("not a key: %d", code)
	}
	form.Set("key", "ssh-ed25519 AAAA tom@laptop")
	if code, loc := h.sent(http.MethodPost, "/settings/remote-access/key", form); loc != "/settings/remote-access?key=tom#key-guide" {
		t.Errorf("after adding = %d %q", code, loc)
	}
	want := agent.SSHKeyRequest{Key: "ssh-ed25519 AAAA tom@laptop", User: "tom", New: true, Password: "a long password"}
	if len(fs.sshKeys) != 1 || fs.sshKeys[0] != want {
		t.Errorf("requests = %+v", fs.sshKeys)
	}
	// An account it has already: no password.
	h.post("/settings/remote-access/key", url.Values{"key": {"ssh-ed25519 BBBB"}, "account": {"existing"}, "existing": {"debian"}, "user": {"x"}}, h.srv.URL)
	if len(fs.sshKeys) != 2 || fs.sshKeys[1] != (agent.SSHKeyRequest{Key: "ssh-ed25519 BBBB", User: "debian"}) {
		t.Errorf("existing account = %+v", fs.sshKeys)
	}

	_, body = h.get("/settings/remote-access?key=tom")
	for _, want := range []string{"Roostack has added your key to tom.", `hx-get="/settings/remote-access?key=tom"`, "ssh tom@127.0.0.1",
		"Roostack is watching for that login"} {
		if !strings.Contains(body, want) {
			t.Errorf("after adding, page missing %q", want)
		}
	}
	fs.sshd.KeyLogins = []agent.KeyLogin{{User: "tom", From: "203.0.113.5", At: time.Now(), Sudo: true}}
	_, body = h.get("/settings/remote-access?key=tom")
	if !strings.Contains(body, "Your SSH key works: tom logged in with one from 203.0.113.5 today") || !strings.Contains(body, ". You can now choose “With an SSH key only” above and save.") || strings.Contains(body, "hx-get=\"/settings/remote-access?key") {
		t.Error("the guide doesn't tick off the login")
	}
	if _, body = h.get("/settings/remote-access"); strings.Contains(body, "Set up an SSH key") {
		t.Error("the guide is still shown once a key works")
	}
	// Sent anyway (from a page loaded before): what's wrong is still shown.
	form.Set("key", "not a key")
	if code, body := h.post("/settings/remote-access/key", form, h.srv.URL); code != http.StatusBadRequest || !strings.Contains(body, "That isn&#39;t an SSH public key.") {
		t.Errorf("not a key, once a key works: %d", code)
	}
}

// The page has one name in Settings (its breadcrumbs, heading and help
// link) and its question in the wizard; short commands in its Details
// don't break at their hyphens on a phone.
func TestSSHPageNames(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: allDone, ssh: config.SSHFromLAN, sshd: agent.SSHStatus{Installed: true}}
	h.setSetup(fs)
	_, body := h.get("/settings/remote-access")
	for _, want := range []string{`<strong aria-current="page">Remote access</strong>`, "<h1>Remote access</h1>",
		`<span class="sr-only">Help: </span>Remote access`, `<code class="nowrap">sshd -T</code>`} {
		if !strings.Contains(body, want) {
			t.Errorf("Settings' remote access page missing %q", want)
		}
	}
	if strings.Contains(body, "Who can log in to this server?") || strings.Contains(body, "Change remote access") {
		t.Error("Settings' remote access page names itself otherwise")
	}

	fs.p = setup.Progress{TimezoneDone: true}
	if _, body := h.get("/setup/ssh"); !strings.Contains(body, "<h1>Who can log in to this server?</h1>") ||
		!strings.Contains(body, `<span class="sr-only">Help: </span>Who can log in to this server?`) {
		t.Error("the wizard's step asks its question")
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
		{"100.96.128.7", false, "a device on the lab's VPN"},
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

// A Match block that turns passwords back on is warned about (SPEC.md
// §6.10 item 3): named on the page, and on Home while it lets in someone
// who can reach SSH.
func TestSSHMatchPasswords(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: allDone, ssh: config.SSHFromLAN, sshLogin: config.SSHLoginKey,
		sshd: agent.SSHStatus{Installed: true, KeysOnly: true, Passwords: true, PasswordsMatch: "Match Address 192.168.7.0/24",
			RootLogin: "no"}}
	h.setSetup(fs)
	_, body := h.get("/settings/remote-access")
	for _, want := range []string{"SSH still takes passwords from some addresses: “Match Address 192.168.7.0/24” in its settings",
		"Takes passwords (Match Address 192.168.7.0/24)"} {
		if !strings.Contains(body, want) {
			t.Errorf("remote access page missing %q", want)
		}
	}
	if _, body := h.get("/partials/home"); !strings.Contains(body, "SSH on this server still accepts passwords") {
		t.Error("Home warns about a Match block for home addresses")
	}
	// Only for addresses outside home: nothing to warn about until SSH is
	// open to them.
	fs.sshd.Passwords, fs.sshd.PasswordsOutside, fs.sshd.PasswordsMatch = false, true, "Match Address 203.0.113.0/24"
	if _, body := h.get("/partials/home"); strings.Contains(body, "accepts passwords") {
		t.Error("SSH from home only: a Match block for the internet can't be reached")
	}
	_, body = h.get("/settings/remote-access")
	if strings.Contains(body, "SSH still takes passwords from some addresses") ||
		!strings.Contains(body, "Takes passwords from some addresses outside your home\n      network (Match Address 203.0.113.0/24)") {
		t.Error("the details still say what SSH does")
	}
	fs.ssh = config.SSHFromAny
	if _, body := h.get("/partials/home"); !strings.Contains(body, "SSH on this server accepts passwords from anywhere") {
		t.Error("SSH from anywhere: the Match block is reached")
	}
}
