package manager

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/state"
)

// TestRelayServers: while the lab comes in through Roostack Cloud's
// relay, a server set to serve directly does, at home too, and gets its
// own tunnel in its assignment; Cloud is told its key, then its public
// names once its tunnel has connected, which stay with it after; its
// direct names get no records of their own (they point at the relay's
// edges); and its card says how visitors reach it (SPEC.md §10.5).
func TestRelayServers(t *testing.T) {
	m, home, vps, _ := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	m.Lab.Domain = "smith.family"
	in, _ := instance.Load(state.InstanceFile(m.Lab, "forgejo"))
	in.SettingUp = false // its install hooks have run: a public route is private until then
	in.Save(state.InstanceFile(m.Lab, "forgejo"))
	if err := m.SetPublic(ctx, "forgejo", PublicRequest{Route: "web", Public: true, AcceptUnsafe: true}); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	m.Lab.Public.Relay = true
	m.Relay.Tunnel = func(id string) agent.WireGuard {
		return agent.WireGuard{Interface: agent.RelayInterface, Address: "100.81.2.1/16"}
	}
	assigned := func(f *movingServer) *agent.WireGuard {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.last.Relay
	}
	// The home server, where forgejo runs, set to serve directly.
	if err := m.SetServe(ctx, "n-0000000002", config.ServeDirect); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); !c.Public || !strings.Contains(c.Why, "isn't connected yet") {
		t.Errorf("the home server through the relay: %+v", c)
	}
	if wg := assigned(home); wg == nil || wg.Address != "100.81.2.1/16" {
		t.Errorf("the home server's tunnel: %+v", wg)
	}
	if wg := assigned(vps); wg == nil {
		t.Error("the cloud server, serving directly by default, has no tunnel")
	}
	if got := m.DirectAddrs(); len(got) != 0 {
		t.Errorf("direct records while relayed: %v", got)
	}

	// No key yet: Cloud isn't told of it. A key: told, with no names.
	if got := m.RelayServers(ctx); len(got) != 0 {
		t.Errorf("before any key: %+v", got)
	}
	home.fakeRemote.mu.Lock()
	home.relay = &agent.WireGuardStatus{Configured: true, PublicKey: "aG9tZWhvbWVob21laG9tZWhvbWVob21laG9tZWhvbWU=",
		Handshakes: map[string]int64{"edge": 0}}
	home.fakeRemote.mu.Unlock()
	got := m.RelayServers(ctx)
	if len(got) != 1 || got[0].ID != "n-0000000002" || got[0].PublicKey == "" || len(got[0].Hosts) != 0 {
		t.Fatalf("joining: %+v", got)
	}
	// Connected: its public names.
	home.fakeRemote.mu.Lock()
	home.relay.Handshakes["edge"] = time.Now().Unix()
	home.fakeRemote.mu.Unlock()
	got = m.RelayServers(ctx)
	if len(got) != 1 || len(got[0].Hosts) == 0 || !slices.ContainsFunc(got[0].Hosts, func(h string) bool { return strings.HasSuffix(h, ".smith.family") }) {
		t.Fatalf("connected: %+v", got)
	}
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); !strings.Contains(c.Why, "only through Roostack Cloud's relay") {
		t.Errorf("connected, the card: %q", c.Why)
	}
	// Quiet since: its names stay with it, and the card says they can't
	// be reached.
	home.fakeRemote.mu.Lock()
	home.relay.Handshakes["edge"] = time.Now().Add(-time.Hour).Unix()
	home.fakeRemote.mu.Unlock()
	if again := m.RelayServers(ctx); !slices.Equal(again[0].Hosts, got[0].Hosts) {
		t.Errorf("quiet: %+v", again)
	}
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); !strings.Contains(c.Why, "can't reach its public services") {
		t.Errorf("quiet, the card: %q", c.Why)
	}

	// The relay off: no tunnels.
	m.mu.Lock()
	m.saveLab(func(l *config.Lab) { l.Public.Relay = false })
	m.mu.Unlock()
	m.Apply()
	m.Wait()
	if wg := assigned(home); wg != nil {
		t.Errorf("without the relay: %+v", wg)
	}
	if got := m.RelayServers(ctx); len(got) != 0 {
		t.Errorf("without the relay, Cloud told of %+v", got)
	}
}
