package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

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
