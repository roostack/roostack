package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/state"
)

// TestSetWAF: the Owner turns blocking known exploits off for a service,
// which its instance file keeps and its Caddy site follows, and on again
// (SPEC.md §6.10 item 4).
func TestSetWAF(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.SetPublic(ctx, "vaultwarden", PublicRequest{Route: "web", Public: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	site := func() string {
		b, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "vaultwarden.caddy"))
		return string(b)
	}
	if ra, _ := m.Access("vaultwarden"); !ra[0].WAF || !strings.Contains(site(), "appsec @vaultwarden-web-outside") {
		t.Fatalf("on by default: %+v\n%s", ra, site())
	}
	if err := m.SetWAF(ctx, "vaultwarden", false); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	in, err := instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	if err != nil || !in.NoWAF {
		t.Fatalf("instance: %+v %v", in, err)
	}
	if ra, _ := m.Access("vaultwarden"); ra[0].WAF || strings.Contains(site(), "appsec") {
		t.Errorf("turned off: %+v\n%s", ra, site())
	}
	if err := m.SetWAF(ctx, "vaultwarden", true); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if !strings.Contains(site(), "appsec @vaultwarden-web-outside") {
		t.Errorf("on again:\n%s", site())
	}
	if err := m.SetWAF(ctx, "nothing", false); err != ErrNotFound {
		t.Errorf("an unknown service: %v", err)
	}
}
