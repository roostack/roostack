package web

import (
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
)

// TestInstallMixedProcessors: in a lab mixing processors, the install
// and move pages still offer a service, on the servers it can run on, and
// say why the others aren't offered (SPEC.md §8.3.3).
func TestInstallMixedProcessors(t *testing.T) {
	h, fm := loggedIn(t, auth.RoleOwner)
	// get reads a page with its whitespace collapsed.
	get := func(path string) string {
		_, body := h.get(path)
		return strings.Join(strings.Fields(body), " ")
	}
	fm.servers = []manager.Server{{ID: "n-0123456789", Name: "nuc", This: true, Controller: true},
		{ID: "n-9876543210", Name: "pi"}}
	fm.noBuild = []manager.NoBuild{{ID: "n-9876543210", Name: "pi", Arch: "arm64"}}
	body := get("/install/vaultwarden")
	for _, want := range []string{`<option value="n-0123456789" >nuc (this server)</option>`,
		`<option value="n-9876543210" disabled>pi: can't run it</option>`,
		"Vaultwarden can&#39;t run on pi: it has no version for its kind of processor (arm64)."} {
		if !strings.Contains(body, want) {
			t.Errorf("install page missing %q", want)
		}
	}
	// This server can't run it: the form starts on one that can.
	fm.noBuild = []manager.NoBuild{{ID: "n-0123456789", Name: "nuc", Arch: "amd64"}}
	body = get("/install/vaultwarden")
	if !strings.Contains(body, `<option value="n-9876543210" selected>pi</option>`) ||
		!strings.Contains(body, `<option value="n-0123456789" disabled>nuc (this server): can't run it</option>`) {
		t.Error("the form should start on the server it can run on")
	}
	// Moving it: the server it can't run on is there, but can't be chosen.
	fm.noBuild = []manager.NoBuild{{ID: "n-9876543210", Name: "pi", Arch: "arm64"}}
	body = get("/services/vaultwarden/move")
	if !strings.Contains(body, `value="n-9876543210" required disabled>`) ||
		!strings.Contains(body, "Vaultwarden can't run there: it has no version for its kind of processor (arm64).") {
		t.Error("move page: the server it can't run on should be there, not to be chosen, and say why")
	}
	// One lab, one processor: nothing said.
	fm.noBuild = nil
	if body = get("/install/vaultwarden"); strings.Contains(body, "kind of processor") || strings.Contains(body, "can't run it") {
		t.Error("nothing to say when it runs everywhere")
	}
}
