package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/setup"
)

// allDone is a finished setup wizard.
var allDone = setup.Progress{TimezoneDone: true, SSHDone: true, DomainDone: true, BackupDone: true, RecoverySaved: true}

// The wizard's first step asks where the lab is, suggesting what the
// server's addresses say; on the internet, the SSH step then recommends
// "from anywhere" (SPEC.md §6.12).
func TestWizardAsksWhereTheLabIs(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{zone: "Etc/UTC"}
	h.setSetup(fs)
	h.server.DetectNetwork = func() string { return config.NetworkInternet }

	_, body := h.get("/setup/timezone")
	if !strings.Contains(body, `data-network="internet"`) || !strings.Contains(body, "looks like a cloud server") {
		t.Errorf("the first step should suggest the guess:\n%.2000s", body)
	}
	h.post("/setup/timezone", url.Values{"timezone": {"Etc/UTC"}, "network": {"internet"}, "ui": {"public"}}, h.srv.URL)
	if fs.network != config.NetworkInternet || fs.uiTunnelOnly {
		t.Fatalf("saved %q, tunnel only %v", fs.network, fs.uiTunnelOnly)
	}
	_, body = h.get("/setup/ssh")
	if !strings.Contains(body, `data-choice="any"`) || !strings.Contains(body, "From anywhere (recommended)") {
		t.Error("on the internet, SSH should default to from anywhere")
	}
}

// Settings → Where it is and DNS changes it (then Roostack restarts); a
// server that looks like a cloud server, with nothing saved, gets a
// reminder on Home.
func TestNetworkSetting(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: allDone}
	h.setSetup(fs)
	h.server.DetectNetwork = func() string { return config.NetworkInternet }
	if _, body := h.get("/"); !strings.Contains(body, "looks like a cloud server") {
		t.Error("Home should suggest saying where the lab is")
	}
	if _, body := h.get("/settings"); !strings.Contains(body, "Looks like a cloud server") {
		t.Error("the Settings overview should too")
	}
	_, body := h.get("/settings/home-network")
	if !strings.Contains(body, `action="/settings/network"`) || !strings.Contains(body, "Roostack treats this lab as <strong>at home</strong>") {
		t.Errorf("network settings:\n%.2000s", body)
	}
	if code, _ := h.post("/settings/network", url.Values{"network": {"moon"}}, h.srv.URL); code != http.StatusBadRequest {
		t.Errorf("a bad choice: %d", code)
	}
	code, body := h.post("/settings/network", url.Values{"network": {"internet"}, "ui": {"tunnel"}}, h.srv.URL)
	if fs.network != config.NetworkInternet || !fs.uiTunnelOnly || !fs.restarted || code != http.StatusOK ||
		!strings.Contains(body, "restart") {
		t.Errorf("saving: %q tunnel %v restarted %v, %d", fs.network, fs.uiTunnelOnly, fs.restarted, code)
	}
	if _, body := h.get("/"); strings.Contains(body, "looks like a cloud server") {
		t.Error("no reminder once it's saved")
	}
}

// On a lab on the internet: private means an SSH tunnel, the checklist
// asks about the provider's firewall and a check from any browser, and
// services start public (with the sharing question the first time).
func TestInternetLabPages(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fs := &fakeSetup{p: allDone, network: config.NetworkInternet}
	h.setSetup(fs)

	if _, body := h.get("/services/vaultwarden"); !strings.Contains(body, "Only through an SSH tunnel") {
		t.Error("a private service on the internet is reachable through a tunnel only")
	}
	_, body := h.get("/services/vaultwarden/public/web")
	for _, want := range []string{"Tunnel only", "only reachable through an SSH tunnel", "Roostack's own pages are public too, at <code>roostack.lab.example.com</code>"} {
		if !strings.Contains(body, want) {
			t.Errorf("public page (private) missing %q", want)
		}
	}
	h.post("/services/vaultwarden/public/web", url.Values{"public": {"on"}, "share": {"yes"}}, h.srv.URL)
	_, body = h.get("/services/vaultwarden/public/web")
	for _, want := range []string{"Your provider's firewall", "Check from a browser", "If the check link doesn't open",
		"Your provider's firewall allows those ports"} {
		if !strings.Contains(body, want) {
			t.Errorf("public page (public) missing %q", want)
		}
	}
	if strings.Contains(body, "Your router.") || strings.Contains(body, "Turn Wi-Fi off") {
		t.Error("no router or phone steps on the internet")
	}

	fm.sharingAsked = false
	_, body = h.get("/install/immich")
	if !strings.Contains(body, `name="public" checked`) || !strings.Contains(body, "Share attack reports") {
		t.Errorf("install form on the internet:\n%.3000s", body)
	}
	h.post("/install/immich", url.Values{"subdomain": {"photos"}, "public": {"on"}, "share": {"no"}}, h.srv.URL)
	if len(fm.installs) != 1 || !fm.installs[0].Public || !fm.sharingAsked || fm.share {
		t.Errorf("installed %+v, sharing asked %v share %v", fm.installs, fm.sharingAsked, fm.share)
	}
}

// At home, nothing changes: the router, a phone on mobile data, and
// services start private.
func TestHomeLabPages(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	h.setSetup(&fakeSetup{p: allDone, network: config.NetworkHome})
	fm.access = []manager.RouteAccess{{Route: "web", Host: "passwords.lab.example.com", Public: true}}
	_, body := h.get("/services/vaultwarden/public/web")
	for _, want := range []string{"Your router.", "Check from your phone.", "Your router forwards those ports", "CGNAT",
		"Roostack's own pages stay private."} {
		if !strings.Contains(body, want) {
			t.Errorf("home public page missing %q", want)
		}
	}
	if _, body := h.get("/install/immich"); strings.Contains(body, `name="public"`) {
		t.Error("no public choice when installing at home")
	}
	h.post("/install/immich", url.Values{"subdomain": {"photos"}, "public": {"on"}}, h.srv.URL)
	if len(fm.installs) != 1 || fm.installs[0].Public {
		t.Errorf("at home, installs start private: %+v", fm.installs)
	}
}
