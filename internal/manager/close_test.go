package manager

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/testwait"
	"roostack.cloud/roostack/internal/trust"
)

// Closing public access completely (SPEC.md §6.16) reaches the firewall
// at once, even while a download holds an apply up, and that apply
// doesn't undo it: Caddy's web ports for home only, and everything public
// paused too. Roostack Cloud's relay is asked to close its edges, and
// opening it again undoes all of it.
func TestCloseDoesntWait(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	fw := &fakeFirewall{}
	local(m).Firewall = fw
	m.Lab.Firewall.SSH = config.SSHFromLAN
	local(m).Lab = m.Lab
	var relayMu sync.Mutex
	var relayAsked []bool
	m.Relay = Relay{Pause: func(_ context.Context, closed bool) error {
		relayMu.Lock()
		defer relayMu.Unlock()
		relayAsked = append(relayAsked, closed)
		return nil
	}}
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.SetPublic(ctx, "vaultwarden", PublicRequest{Route: "web", Public: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.saveLab(func(l *config.Lab) { l.Public.Relay = true }); err != nil {
		t.Fatal(err)
	}
	lastFW := func() (closed bool, home bool) {
		fw.mu.Lock()
		defer fw.mu.Unlock()
		if len(fw.applied) == 0 {
			return false, false
		}
		last := fw.applied[len(fw.applied)-1]
		home = slices.ContainsFunc(last.Allow, func(r agent.Rule) bool { return r.Port == 443 && r.For == trust.Home })
		return last.Closed, home
	}
	if closed, _ := lastFW(); closed {
		t.Fatal("closed before anyone closed it")
	}

	release := make(chan struct{})
	downloading := make(chan struct{}, 10)
	local(m).Reconciler.Pull = func(context.Context, string, io.Writer) error {
		downloading <- struct{}{}
		<-release
		return nil
	}
	if err := m.Install(ctx, "immich", InstallRequest{Settings: map[string]string{"machine_learning": "on"}}); err != nil {
		t.Fatal(err)
	}
	<-downloading
	if err := m.PausePublic("tom", PauseRequest{Closed: true}); err != nil {
		t.Fatal(err)
	}
	testwait.For(t, 5*time.Second, func() bool { closed, home := lastFW(); return closed && home })
	site := func() string {
		b, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "vaultwarden.caddy"))
		return string(b)
	}
	testwait.For(t, 5*time.Second, func() bool { return strings.Contains(site(), "error @vaultwarden-web-outside 503") })
	close(release)
	m.Wait()
	if closed, home := lastFW(); !closed || !home {
		t.Error("the install's apply undid the closing at the firewall")
	}
	if c := m.labNow().Public.Pause.Closed; c == nil || c.By != "tom" {
		t.Errorf("lab.yaml: %+v", c)
	}
	if pp, _ := m.PublicPauses(); pp.Closed == nil || !pp.Services[0].ByClosed || !pp.Services[0].Paused() {
		t.Errorf("PublicPauses: %+v", pp)
	}

	if err := m.LiftPause(PauseRequest{Closed: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	testwait.For(t, 5*time.Second, func() bool { closed, home := lastFW(); return !closed && !home })
	if strings.Contains(site(), "error @vaultwarden-web-outside 503") {
		t.Error("still paused after opening it again")
	}
	testwait.For(t, 5*time.Second, func() bool {
		relayMu.Lock()
		defer relayMu.Unlock()
		return slices.Contains(relayAsked, true) && relayAsked[len(relayAsked)-1] == false
	})
	if err := (PauseRequest{Closed: true, All: true}).Check(); err == nil {
		t.Error("closing goes on its own")
	}
}

// An advisory about Roostack itself (SPEC.md §6.16) is recorded for the
// Owners' email once, and never closes anything by itself; a closing for
// it opens again by itself once it no longer affects the Roostack the lab
// runs, while one for no advisory, or another, stays.
func TestClosedForRoostackAdvisory(t *testing.T) {
	m, _ := newManager(t)
	m.Version = "0.4.1"
	m.Settings = &memKV{m: map[string]string{}}
	var mu sync.Mutex
	var audit []string
	m.Audit = func(_ context.Context, action, detail string) {
		mu.Lock()
		defer mu.Unlock()
		audit = append(audit, action+": "+detail)
	}
	took := func(want ...string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if strings.Join(audit, "|") != strings.Join(want, "|") {
			t.Errorf("audit:\n%q\nwant\n%q", audit, want)
		}
		audit = nil
	}
	// The index is swapped below while RegistryChanged's goroutine (which
	// m.Wait doesn't wait for) may still read it.
	var ix atomic.Pointer[registry.Index]
	m.RegistryIndex = func() (*registry.Index, error) { return ix.Load(), nil }
	ix.Store(&registry.Index{})
	ix.Load().Advisories = []*registry.Advisory{{ID: "2026-0009", Title: "Caddy lets anyone read any file.", Severity: registry.SeverityCritical,
		Exploited: true, Published: "2026-10-23", Roostack: &registry.RoostackAffected{Versions: []string{"0.4.0", "0.4.1"}, Fixed: "0.4.2"}}}
	m.RegistryChanged()
	m.Wait()
	took("security advisory: Roostack 0.4.1: Caddy lets anyone read any file (2026-0009, critical, being used in attacks)")
	advs := m.RoostackAdvisories(context.Background())
	if len(advs) != 1 || advs[0].Fixed != "0.4.2" || advs[0].Since.IsZero() || m.labNow().Public.Pause.Closed != nil {
		t.Fatalf("Roostack's advisories: %+v, closed %+v", advs, m.labNow().Public.Pause.Closed)
	}
	m.ApplyBecause("test")
	m.Wait()
	took() // said once

	if err := m.PausePublic("tom", PauseRequest{Closed: true, Advisory: "2026-0001"}); err != nil {
		t.Fatal(err)
	}
	if c := m.labNow().Public.Pause.Closed; c == nil || c.Advisory != "" {
		t.Errorf("closed for an advisory that doesn't affect it: %+v", c)
	}
	m.LiftPause(PauseRequest{Closed: true})
	if err := m.PausePublic("tom", PauseRequest{Closed: true, Advisory: "2026-0009"}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if c := m.labNow().Public.Pause.Closed; c == nil || c.Advisory != "2026-0009" {
		t.Fatalf("closed for it: %+v", c)
	}
	// The registry withdraws it (or Roostack was updated): open again.
	ix.Store(&registry.Index{})
	m.RegistryChanged()
	m.Wait()
	testwait.For(t, 5*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.labNow().Public.Pause.Closed == nil
	})
	took("public access opened by Roostack: everything, closed completely: security advisory 2026-0009 no longer affects Roostack 0.4.1: " +
		"it was updated, or the registry changed the advisory")
}
