package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
)

// A service's settings page says what limit it has and why, and lets the
// Owner or an Admin choose one, a figure, or none (SPEC.md §6.1,
// "Memory").
func TestServiceMemory(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleAdmin)
	if _, body := h.get("/services/vaultwarden/settings"); strings.Contains(body, `id="memory"`) {
		t.Error("a Memory card without figures")
	}
	fm.memory = &manager.ServiceMemory{Figure: 50_000_000, Known: true, Server: "nuc", Total: 2_000_000_000}
	_, body := h.get("/services/vaultwarden/settings")
	for _, want := range []string{`id="memory"`, "Its package expects about 50.0 MB.",
		"Roostack isn't limiting it on its own, as nuc has room for what every service on it expects.",
		"All the services on nuc together may use 1.6 GB of its 2.0 GB", `value="auto"`, `placeholder="100.0 MB"`} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page is missing %q", want)
		}
	}
	fm.memory.Tight, fm.memory.Auto = true, 306_000_000
	if _, body := h.get("/services/vaultwarden/settings"); !strings.Contains(body, "Roostack is holding it to 306.0 MB, twice what its package expects or 256 MB more if that's larger") {
		t.Error("the automatic limit while tight isn't explained")
	}

	if code, body := h.post("/services/vaultwarden/memory", url.Values{"memory": {"limit"}, "memory_size": {"lots"}}, h.srv.URL); code != http.StatusBadRequest ||
		!strings.Contains(body, "Enter a size of at least 100 MB") || !strings.Contains(body, `value="lots"`) {
		t.Errorf("a size that isn't one: %d", code)
	}
	h.post("/services/vaultwarden/memory", url.Values{"memory": {"limit"}, "memory_size": {"1.5gb"}}, h.srv.URL)
	if fm.memoryLimits["vaultwarden"] != "1.5 GB" {
		t.Errorf("limit %q", fm.memoryLimits["vaultwarden"])
	}
	if _, body := h.get("/services/vaultwarden/settings"); !strings.Contains(body, "You've chosen a limit of 1.5 GB") {
		t.Error("the chosen limit isn't shown")
	}
	h.post("/services/vaultwarden/memory", url.Values{"memory": {"none"}}, h.srv.URL)
	if fm.memoryLimits["vaultwarden"] != "none" {
		t.Errorf("no limit: %q", fm.memoryLimits["vaultwarden"])
	}
	h.post("/services/vaultwarden/memory", url.Values{"memory": {"auto"}}, h.srv.URL)
	if fm.memoryLimits["vaultwarden"] != "" {
		t.Errorf("automatic: %q", fm.memoryLimits["vaultwarden"])
	}
}
