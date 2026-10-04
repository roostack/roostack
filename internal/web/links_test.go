package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
)

// The Owner can say where another server is reached directly; a mistake
// is shown on its card.
func TestLinkAddress(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true, Reachable: true},
		{ID: "n-9876543210", Name: "nas", Reachable: true}}
	_, body := h.get("/settings/servers")
	if strings.Count(body, "/link-address") != 1 || !strings.Contains(body, `action="/settings/servers/n-9876543210/link-address"`) {
		t.Error("only other servers have an address for direct links")
	}
	if code, body := h.post("/settings/servers/n-9876543210/link-address", url.Values{"address": {"nas.local"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "Enter the address as host:port.") {
		t.Errorf("bad address: %d", code)
	}
	h.post("/settings/servers/n-9876543210/link-address", url.Values{"address": {"192.168.1.20:51820"}}, h.srv.URL)
	if fm.linkAddrs["n-9876543210"] != "192.168.1.20:51820" {
		t.Errorf("addresses %v", fm.linkAddrs)
	}
	if _, body := h.get("/settings/servers"); !strings.Contains(body, `value="192.168.1.20:51820"`) {
		t.Error("the address should show")
	}
}

// Another server's backup status says why it can't be shown: it's being
// set up, or the server doesn't answer (restarting with its backups).
func TestBackupCardStates(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true, Reachable: true},
		{ID: "n-9876543210", Name: "vps", Reachable: true}}
	if _, body := h.get("/backups?server=n-9876543210"); !strings.Contains(body, "Can't reach vps right now") {
		t.Error("a server that doesn't answer")
	}
	fm.settings = &fakeSetup{}
	h.acts().Start(activity.BackupSetupFor("n-9876543210"), "Setting up backups for vps").Step("Creating the repository")
	if _, body := h.get("/backups?server=n-9876543210"); !strings.Contains(body, "Setting up backups: creating the repository…") {
		t.Error("while it's set up")
	}
}

// A backup to another server's disk that goes through the controller says
// so on the plan, and how to link them directly.
func TestPlanThroughTheController(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true, Reachable: true},
		{ID: "n-9876543210", Name: "vps", Reachable: true}, {ID: "n-5555555555", Name: "nas", Reachable: true}}
	fm.settings, fm.destination = &fakeSetup{}, "server:n-5555555555"
	fm.linkPath = manager.PathHub
	_, body := h.get("/backups?server=n-9876543210")
	if !strings.Contains(body, "These backups go through nuc, using its connection twice") ||
		!strings.Contains(body, `href="/settings/servers#server-n-5555555555"`) {
		t.Error("through the controller: say how to link directly")
	}
	fm.linkFailed = true
	if _, body := h.get("/backups?server=n-9876543210"); !strings.Contains(body, "A direct link to") {
		t.Error("a link that didn't connect")
	}
	fm.linkPath, fm.linkFailed = manager.PathDirect, false
	if _, body := h.get("/backups?server=n-9876543210"); strings.Contains(body, "using its connection twice") || !strings.Contains(body, "linked directly") {
		t.Error("linked directly: no warning")
	}
}
