package manager

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/registry"
)

// TestMixedProcessors: in a lab with an amd64 and an arm64 server, a
// service is checked against the processor of the server it's placed on,
// not the controller's: installing, moving and updating say plainly where
// it can't run, and it's still offered where it can (SPEC.md §8.3.3).
func TestMixedProcessors(t *testing.T) {
	m, _ := newManager(t)
	m.Lab.NodeID = "n-0000000001"
	local(m).ID = nodeid.Identity{ID: "n-0000000001", Name: "nuc"}
	here, other := runtime.GOARCH, "arm64"
	if here == "arm64" {
		other = "amd64"
	}
	vps := &fakeRemote{arch: other}
	m.Others = func() []RemoteServer { return []RemoteServer{vps} }
	kv := linkKV{}
	m.Settings = kv
	ctx := context.Background()

	want := []string{here, other}
	slices.Sort(want)
	if got := m.LabArches(); !slices.Equal(got, want) {
		t.Errorf("the lab's processors: %v, want %v", got, want)
	}
	if kv[serverArchKey+"n-0000000002"] != other {
		t.Errorf("vps's processor isn't remembered: %v", kv)
	}

	// Vaultwarden has a build for nuc's processor only.
	p, err := m.Package("vaultwarden")
	if err != nil {
		t.Fatal(err)
	}
	ix := &registry.Index{Packages: map[string]*registry.Package{"vaultwarden": {
		Versions: map[string]*registry.Version{p.Version: {Architectures: []string{here}}}}}}
	m.RegistryIndex = func() (*registry.Index, error) { return ix, nil }
	if nb := m.NoBuildOn(p); len(nb) != 1 || nb[0].ID != "n-0000000002" || nb[0].Name != "vps" || nb[0].Arch != other {
		t.Errorf("NoBuildOn: %+v", nb)
	}
	refused := "Vaultwarden has no version for vps's kind of processor (" + other + "), so it can't run there. It can run on nuc."
	onVPS := InstallRequest{Host: "n-0000000002", Settings: map[string]string{}}
	var fe *FormError
	if _, err := m.InstallSummary("vaultwarden", onVPS); !errors.As(err, &fe) || fe.Msg != refused {
		t.Errorf("summary on vps: %v", err)
	}
	if err := m.Install(ctx, "vaultwarden", onVPS); !errors.As(err, &fe) || fe.Msg != refused {
		t.Errorf("install on vps: %v", err)
	}
	// vps stops answering: what it said last still counts.
	vps.mu.Lock()
	vps.arch = ""
	vps.mu.Unlock()
	if err := m.Install(ctx, "vaultwarden", onVPS); !errors.As(err, &fe) || fe.Msg != refused {
		t.Errorf("install on vps while it doesn't answer: %v", err)
	}
	if _, err := m.InstallSummary("vaultwarden", InstallRequest{Settings: map[string]string{}}); err != nil {
		t.Errorf("on nuc: %v", err)
	}

	// Built for both: it installs on vps.
	ix.Packages["vaultwarden"].Versions[p.Version].Architectures = []string{"amd64", "arm64"}
	if nb := m.NoBuildOn(p); len(nb) != 0 {
		t.Errorf("built for both: %+v", nb)
	}
	if err := m.Install(ctx, "vaultwarden", onVPS); err != nil {
		t.Fatal(err)
	}
	m.Wait()

	// Moving it to a server it has no build for is refused.
	ix.Packages["vaultwarden"].Versions[p.Version].Architectures = []string{other}
	err = m.MoveService(ctx, "vaultwarden", "n-0000000001", MoveCopy)
	if !errors.As(err, &fe) || !strings.Contains(fe.Msg, "nuc's kind of processor ("+here+"), so it can't run there. It can run on vps.") {
		t.Errorf("move to nuc: %v", err)
	}

	// A newer version built for nuc's processor only isn't an update vps
	// can take: it says why.
	from := p.Version
	offerVersion(t, m, "vaultwarden", from, "1.34.4-roostack.1")
	ix = withdrawIndex(t, m, nil)
	ix.Packages["vaultwarden"].Versions[from].Architectures = []string{"amd64", "arm64"}
	ix.Packages["vaultwarden"].Versions["1.34.4-roostack.1"] = &registry.Version{Architectures: []string{here}}
	u := m.AvailableUpdates(ctx)["vaultwarden"]
	if u == nil || u.Auto || !strings.Contains(u.Blocked, "This version has no build for vps's kind of processor ("+other+")") {
		t.Errorf("update on vps: %+v", u)
	}
	ix.Packages["vaultwarden"].Versions["1.34.4-roostack.1"].Architectures = nil // the index doesn't say
	if u := m.AvailableUpdates(ctx)["vaultwarden"]; u == nil || u.Blocked != "" {
		t.Errorf("architectures unknown: %+v", u)
	}
}
