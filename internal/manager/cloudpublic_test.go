package manager

import (
	"context"
	"slices"
	"sync"
	"testing"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
)

// fakeVerdicts plays Roostack Cloud's say on the lab's public addresses:
// what it was told, and what it answers.
type fakeVerdicts struct {
	mu      sync.Mutex
	sent    int
	known   map[string]bool
	refused map[string]string
}

func (f *fakeVerdicts) cloudPublic() CloudPublic {
	return CloudPublic{
		Send: func(context.Context) error { f.mu.Lock(); f.sent++; f.mu.Unlock(); return nil },
		Verdict: func(h cloudapi.PublicHost) (*cloudapi.NameStatus, bool) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if st := f.refused[h.Host]; st != "" {
				return &cloudapi.NameStatus{Host: h.Host, State: st}, true
			}
			return nil, f.known[h.Host]
		},
	}
}

// TestCloudPublic: on a roost.ac name, a service from a package the
// registry flags (or one of the Owner's own) stays private until Roostack
// Cloud has answered, and while it says it won't carry it; one from the
// registry goes public at once; Cloud is told what serves each.
func TestCloudPublic(t *testing.T) {
	m, _ := newManager(t)
	ctx := context.Background()
	m.Lab.Domain, m.Lab.DNS = "smiths.roost.test", config.DNS{Provider: config.ProviderCloud}
	local(m).Lab = m.Lab
	ix := &registry.Index{Packages: map[string]*registry.Package{
		"vaultwarden": {RoostAc: &registry.RoostAc{Approval: "It hosts anything uploaded."}},
	}}
	m.RegistryIndex = func() (*registry.Index, error) { return ix, nil }
	f := &fakeVerdicts{known: map[string]bool{}, refused: map[string]string{}}
	m.CloudPublic = f.cloudPublic()
	for _, name := range []string{"vaultwarden", "immich"} {
		if err := m.Install(ctx, name, InstallRequest{Settings: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
		m.Wait()
		if err := m.SetPublic(ctx, name, PublicRequest{Route: "web", Public: true, AcceptUnsafe: true}); err != nil {
			t.Fatal(err)
		}
		m.Wait()
	}
	pw, photos := "vault.smiths.roost.test", "photos.smiths.roost.test"
	told := m.CloudPublicHosts()
	if len(told) != 2 || told[1] != (cloudapi.PublicHost{Host: pw, Kind: cloudapi.HostRegistry, Package: "vaultwarden", Approval: true}) ||
		told[0] != (cloudapi.PublicHost{Host: photos, Kind: cloudapi.HostRegistry, Package: "immich"}) {
		t.Fatalf("Cloud is told %+v", told)
	}
	refusedNow := func() map[string]string {
		t.Helper()
		lab, _ := local(m).Assigned()
		return lab.Public.Refused
	}
	// Before Cloud has answered: Vaultwarden waits, Immich doesn't.
	if r := refusedNow(); r["vaultwarden"] != config.RefusedChecking || len(r) != 1 {
		t.Errorf("refused before an answer: %v", r)
	}
	if hosts := m.PublicHosts(); !slices.Equal(hosts, []string{photos}) {
		t.Errorf("public names before an answer: %v", hosts)
	}
	f.mu.Lock()
	sent := f.sent
	f.refused[pw] = cloudapi.NameNeedsApproval
	f.mu.Unlock()
	if sent == 0 {
		t.Error("Cloud wasn't told")
	}
	m.Apply()
	m.Wait()
	if r := refusedNow(); r["vaultwarden"] != cloudapi.NameNeedsApproval {
		t.Errorf("refused by Cloud: %v", r)
	}
	// Approved: it goes public.
	f.mu.Lock()
	delete(f.refused, pw)
	f.known[pw] = true
	f.mu.Unlock()
	m.Apply()
	m.Wait()
	if r := refusedNow(); len(r) != 0 {
		t.Errorf("approved, still refused: %v", r)
	}
	if hosts := m.PublicHosts(); !slices.Equal(hosts, []string{photos, pw}) {
		t.Errorf("public names once approved: %v", hosts)
	}

	// The Owner's own package is never named; on their own domain without
	// the relay, Cloud isn't told anything, and nothing is limited.
	_, in, err := m.installedService("immich")
	if err != nil {
		t.Fatal(err)
	}
	in.Local = true
	if err := in.Save(state.InstanceFile(m.Lab, "immich")); err != nil {
		t.Fatal(err)
	}
	if told := m.CloudPublicHosts(); told[0] != (cloudapi.PublicHost{Host: photos, Kind: cloudapi.HostOwn}) {
		t.Errorf("the Owner's own: %+v", told[0])
	}
	m.Lab.Domain, m.Lab.DNS = "lab.example.com", config.DNS{Provider: "cloudflare"}
	if told := m.CloudPublicHosts(); len(told) != 0 {
		t.Errorf("own domain, no relay: told %+v", told)
	}
	m.Apply()
	m.Wait()
	if r := refusedNow(); len(r) != 0 {
		t.Errorf("own domain, no relay: refused %v", r)
	}
}
