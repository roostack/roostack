package manager

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/testwait"
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
		pa, err := m.PublicPauses()
		if err != nil || len(pa.Services) != 1 || pa.Services[0].Paused() != want || len(pa.PausedServices()) != map[bool]int{true: 1}[want] {
			t.Errorf("%s: PublicPauses %+v %v", why, pa, err)
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
	pa, _ := m.PublicPauses()
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

// A pause doesn't wait behind an apply that's running (SPEC.md §6.16):
// while a new service's download holds it up, the controller's Caddy gets
// the pause at once, the change shows as waiting (its web addresses done
// already) behind the install, and the install's own Caddy files don't
// undo it.
func TestPauseDoesntWait(t *testing.T) {
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
	release := make(chan struct{})
	downloading := make(chan struct{}, 10)
	m.Node.(*node.Local).Reconciler.Pull = func(context.Context, string, io.Writer) error {
		downloading <- struct{}{}
		<-release
		return nil
	}
	if err := m.Install(ctx, "immich", InstallRequest{Settings: map[string]string{"machine_learning": "on"}}); err != nil {
		t.Fatal(err)
	}
	<-downloading
	site := func() string {
		b, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "vaultwarden.caddy"))
		return string(b)
	}
	if err := m.PausePublic("tom", PauseRequest{All: true}); err != nil {
		t.Fatal(err)
	}
	testwait.For(t, 5*time.Second, func() bool { return strings.Contains(site(), "error @vaultwarden-web-outside 503") })
	st := m.Status()
	if len(st.Doing) != 1 || st.Doing[0].Reason != "Installing immich" || len(st.Waiting) != 1 ||
		st.Waiting[0] != (node.Change{Reason: "Pausing public access to everything", Early: true}) {
		t.Errorf("while the download runs: doing %+v, waiting %+v", st.Doing, st.Waiting)
	}
	close(release)
	m.Wait()
	if !strings.Contains(site(), "error @vaultwarden-web-outside 503") {
		t.Errorf("the install's apply undid the pause:\n%s", site())
	}
	if st := m.Status(); st.Running || len(st.Doing)+len(st.Waiting) != 0 {
		t.Errorf("after: %+v", st)
	}
}

// Built-with names read as the registry's titles where its list has them
// (PHP for php), else as written: in what's paused, the apply's reason,
// and what a service is built with.
func TestBuiltWithTitles(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	m.RegistryIndex = func() (*registry.Index, error) {
		return &registry.Index{BuiltWith: []registry.BuiltWith{{Name: "rust", Title: "Rust"}}}, nil
	}
	if got := strings.Join(m.BuiltWith("vaultwarden"), ", "); got != "Rust, sqlite" {
		t.Errorf("BuiltWith = %q", got)
	}
	if err := m.PausePublic("tom", PauseRequest{BuiltWith: []string{"rust"}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	pp, _ := m.PublicPauses()
	if pp.Title("rust") != "Rust" || pp.Title("php") != "php" {
		t.Errorf("titles: %v", pp.Titles)
	}
	if st := m.Status(); st.LastErr != nil {
		t.Fatal(st.LastErr)
	}
	if got := PauseWhat(PauseRequest{BuiltWith: []string{"rust", "php"}}, nil, pp.Title); got != "services built with Rust and php" {
		t.Errorf("PauseWhat = %q", got)
	}
}

// The command line changes lab.yaml's pauses and asks the running
// Roostack to read them (ReloadPauses), which applies them at once.
func TestReloadPauses(t *testing.T) {
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
	m.ReloadPauses(config.PublicPause{All: &config.Pause{Since: time.Now()}})
	m.Wait()
	b, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "vaultwarden.caddy"))
	if !strings.Contains(string(b), "error @vaultwarden-web-outside 503") || m.labNow().Public.Pause.All == nil {
		t.Errorf("not paused:\n%s", b)
	}
	m.ReloadPauses(config.PublicPause{})
	m.Wait()
	if b, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "vaultwarden.caddy")); strings.Contains(string(b), "503") {
		t.Error("still paused")
	}
}
