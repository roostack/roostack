package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/store/storetest"
)

// Security advisories (SPEC.md §6.16) are worked out from the registry's
// index at every apply: a critical one being used in attacks pauses a
// public service under the default policy and lifts by itself when the
// advisory goes; one that isn't exploited only pauses under "any
// critical"; "tell" never pauses; Keep it public anyway ends a pause for
// that version; and each is recorded for the Owners' email.
func TestAdvisoriesPause(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	m.Settings = &memKV{m: map[string]string{}}
	var mu sync.Mutex
	var audit []string
	m.Audit = func(_ context.Context, action, detail string) {
		mu.Lock()
		defer mu.Unlock()
		audit = append(audit, action+": "+detail)
	}
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.SetPublic(ctx, "vaultwarden", PublicRequest{Route: "web", Public: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	in, _ := instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	file, _ := os.ReadFile(m.Lab.Paths.LabPackageFile(in.Package, in.Version))
	h := sha256.Sum256(file)
	adv := &registry.Advisory{ID: "2026-0007", Title: "Vaultwarden lets anyone read every vault", Severity: registry.SeverityCritical,
		Exploited: true, Published: "2026-10-22",
		Affects: map[string]*registry.Affected{"vaultwarden": {Versions: map[string]string{in.Version: hex.EncodeToString(h[:])}}}}
	var ix registry.Index
	m.RegistryIndex = func() (*registry.Index, error) { return &ix, nil }
	paused := func(want bool, why string) {
		t.Helper()
		m.Wait()
		b, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "vaultwarden.caddy"))
		if got := strings.Contains(string(b), "error @vaultwarden-web-outside 503"); got != want {
			t.Errorf("%s: paused %v, want %v", why, got, want)
		}
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

	ix.Advisories = []*registry.Advisory{adv}
	m.RegistryChanged()
	paused(true, "critical and exploited")
	took("public access paused by Roostack: Vaultwarden: Vaultwarden lets anyone read every vault (2026-0007, critical, being used in attacks)")
	advs := m.Advisories(ctx)["vaultwarden"]
	if len(advs) != 1 || !advs[0].Paused || advs[0].Since.IsZero() {
		t.Fatalf("advisories: %+v", advs)
	}
	if pp, _ := m.PublicPauses(); pp.Services[0].Advisory == nil || !pp.Services[0].Paused() {
		t.Errorf("PublicPauses: %+v", pp.Services)
	}

	if err := m.SetAdvisoryPolicy(config.AdvisoriesTell); err != nil {
		t.Fatal(err)
	}
	paused(false, "tell")
	took("public access opened by Roostack: Vaultwarden: Settings → Public access no longer pauses it for security advisory 2026-0007")
	adv.Exploited = false
	if err := m.SetAdvisoryPolicy(config.AdvisoriesExploited); err != nil {
		t.Fatal(err)
	}
	paused(false, "not exploited")
	if err := m.SetAdvisoryPolicy(config.AdvisoriesCritical); err != nil {
		t.Fatal(err)
	}
	paused(true, "any critical")
	took("public access paused by Roostack: Vaultwarden: Vaultwarden lets anyone read every vault (2026-0007, critical)")

	if err := m.KeepPublicAnyway(ctx, "vaultwarden", "2026-0007"); err != nil {
		t.Fatal(err)
	}
	paused(false, "kept public anyway")
	took("public access opened by Roostack: Vaultwarden: the Owner chose to keep it public despite security advisory 2026-0007")
	if in, _ := instance.Load(state.InstanceFile(m.Lab, "vaultwarden")); !in.KeptPublicFor("2026-0007") {
		t.Errorf("not kept: %+v", in.PublicAnyway)
	}

	// A new high advisory pauses nothing, but is recorded once; a removed
	// one lifts by itself.
	ix.Advisories = append(ix.Advisories, &registry.Advisory{ID: "2026-0008", Title: "Vaultwarden leaks a little", Severity: registry.SeverityHigh,
		Published: "2026-10-23", Affects: adv.Affects})
	m.ApplyBecause("test")
	m.Wait()
	m.ApplyBecause("test")
	paused(false, "high")
	took("security advisory: Vaultwarden: Vaultwarden leaks a little (2026-0008, high)")
}

// A service paused for an advisory updates straight away to a version the
// advisory doesn't affect, when its update policy takes it, and its pause
// lifts with the update; on notify, or with only an affected version on
// offer, it waits.
func TestUpdateAdvised(t *testing.T) {
	t.Parallel()
	m, _ := newManager(t)
	ctx := context.Background()
	backups, _ := updateSetup(t, m)
	var mu sync.Mutex
	var audit []string
	m.Audit = func(_ context.Context, action, detail string) {
		mu.Lock()
		defer mu.Unlock()
		audit = append(audit, action+": "+detail)
	}
	m.UpdateLab = func(func(*config.Lab)) error { return nil }
	if err := m.Install(ctx, "vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if err := m.SetPublic(ctx, "vaultwarden", PublicRequest{Route: "web", Public: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	st := storetest.Open(t)
	st.SetBackupTarget(ctx, store.BackupTarget{ServerID: m.Lab.NodeID, Provider: "local:usb", Repository: "/mnt/usb/backups"})
	m.BackupStore = st

	in, _ := instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	from := in.Version
	ix := withdrawIndex(t, m, nil)
	sum := ix.Packages["vaultwarden"].Versions[from].File.SHA256
	aff := &registry.Affected{Versions: map[string]string{from: sum}}
	ix.Advisories = []*registry.Advisory{{ID: "2026-0007", Title: "Vaultwarden lets anyone read every vault", Severity: registry.SeverityCritical,
		Exploited: true, Published: "2026-10-22", Affects: map[string]*registry.Affected{"vaultwarden": aff}}}
	m.RegistryIndex = func() (*registry.Index, error) { return ix, nil }
	m.ApplyBecause("test")
	m.Wait()
	if advs := m.Advisories(ctx)["vaultwarden"]; len(advs) != 1 || !advs[0].Paused {
		t.Fatalf("paused: %+v", advs)
	}

	// The version on offer is affected too: nothing to update to.
	offerVersion(t, m, "vaultwarden", from, "1.34.4-roostack.1")
	aff.Versions["1.34.4-roostack.1"] = "any"
	if err := m.SetUpdatePolicy(ctx, "vaultwarden", "patch"); err != nil {
		t.Fatal(err)
	}
	m.UpdateAdvised(ctx)
	if m.Updating("vaultwarden") != nil {
		t.Fatal("an affected version isn't a fix")
	}

	// A fix, on notify: only offered.
	m.Wait()
	delete(aff.Versions, "1.34.4-roostack.1")
	aff.Fixed = "1.34.4-roostack.1"
	if err := m.SetUpdatePolicy(ctx, "vaultwarden", "notify"); err != nil {
		t.Fatal(err)
	}
	m.UpdateAdvised(ctx)
	if m.Updating("vaultwarden") != nil {
		t.Fatal("notify only offers it")
	}

	// On patch: it updates at once, with a backup, and opens again.
	if err := m.SetUpdatePolicy(ctx, "vaultwarden", "patch"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	audit = nil
	mu.Unlock()
	m.UpdateAdvised(ctx)
	if m.Updating("vaultwarden") == nil {
		t.Fatal("the fix isn't being applied")
	}
	waitUpdated(t, m, "vaultwarden")
	in, _ = instance.Load(state.InstanceFile(m.Lab, "vaultwarden"))
	rec := m.LastUpdate(ctx, "vaultwarden")
	if in.Version != "1.34.4-roostack.1" || rec.Result != UpdateDone || !rec.Automatic || backups.runs != 1 {
		t.Fatalf("updated: %s %+v (backups %d)", in.Version, rec, backups.runs)
	}
	if len(m.Advisories(ctx)["vaultwarden"]) != 0 {
		t.Error("the new version isn't affected")
	}
	b, _ := os.ReadFile(filepath.Join(m.Lab.Paths.CaddySitesDir(), "vaultwarden.caddy"))
	if strings.Contains(string(b), "error @vaultwarden-web-outside 503") {
		t.Error("still paused after the update")
	}
	mu.Lock()
	defer mu.Unlock()
	want := "public access opened by Roostack: Vaultwarden: security advisory 2026-0007 no longer affects it: it was updated, or the registry changed the advisory"
	if !slices.Contains(audit, want) {
		t.Errorf("audit: %q", audit)
	}
}
