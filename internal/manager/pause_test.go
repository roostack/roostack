package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
)

// isUserErr says whether err is a sentence for the person (usererr).
func isUserErr(err error) bool { _, ok := usererr.Message(err); return ok }

// TestPausePublic: pausing public access (SPEC.md §6.16) for everything,
// by what services are built with, or for a service, reaches its Caddy
// site, and lifting each opens it again; a service another pause still
// covers stays paused, and a private service has nothing to pause.
func TestPausePublic(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.PausePublic("tom", PauseRequest{Services: []string{"vaultwarden"}}); !isUserErr(err) {
		t.Errorf("pausing a private service: %v", err)
	}
	if err := m.SetPublic(ctx, "vaultwarden", PublicRequest{Route: "web", Public: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	paused := func(want bool, why string) {
		t.Helper()
		b, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "vaultwarden.caddy"))
		if got := strings.Contains(string(b), "error @vaultwarden-web-outside 503"); got != want {
			t.Errorf("%s: paused %v, want %v:\n%s", why, got, want, b)
		}
		pa, err := m.PublicAccess()
		if err != nil || len(pa.Services) != 1 || pa.Services[0].Paused() != want || len(pa.PausedServices()) != map[bool]int{true: 1}[want] {
			t.Errorf("%s: PublicAccess %+v %v", why, pa, err)
		}
	}
	paused(false, "public")
	if err := m.PausePublic("tom", PauseRequest{}); !isUserErr(err) {
		t.Errorf("nothing chosen: %v", err)
	}
	if err := m.PausePublic("tom", PauseRequest{BuiltWith: []string{"Rust!"}}); !isUserErr(err) {
		t.Errorf("a bad name: %v", err)
	}

	if err := m.PausePublic("tom", PauseRequest{All: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	paused(true, "everything")
	if p := m.labNow().Public.Pause.All; p == nil || p.By != "tom" || p.Since.IsZero() {
		t.Errorf("the lab's pause: %+v", p)
	}
	if err := m.PausePublic("sam", PauseRequest{BuiltWith: []string{"rust", "php"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.LiftPause(PauseRequest{All: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	paused(true, "still built with rust")
	pa, _ := m.PublicAccess()
	if s := pa.Services[0]; s.ByAll || strings.Join(s.ByBuiltWith, " ") != "rust" || pa.BuiltWith["php"].By != "sam" {
		t.Errorf("by what it's built with: %+v", pa)
	}
	if err := m.LiftPause(PauseRequest{BuiltWith: []string{"rust", "php"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	paused(false, "lifted")
	if !m.labNow().Public.Pause.IsZero() {
		t.Errorf("left behind: %+v", m.labNow().Public.Pause)
	}

	if err := m.PausePublic("tom", PauseRequest{Services: []string{"vaultwarden"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	paused(true, "its own")
	if in, err := instance.Load(state.InstanceFile(m.Lab, "vaultwarden")); err != nil || in.PublicPause == nil || in.PublicPause.By != "tom" {
		t.Errorf("instance: %+v %v", in, err)
	}
	if err := m.LiftPause(PauseRequest{Services: []string{"vaultwarden"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	paused(false, "its own lifted")
	if err := m.LiftPause(PauseRequest{Services: []string{"nothing"}}); err != ErrNotFound {
		t.Errorf("an unknown service: %v", err)
	}
}
