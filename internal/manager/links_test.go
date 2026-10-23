package manager

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/store/storetest"
	"roostack.cloud/roostack/internal/usererr"
)

// linkKV is settings in memory.
type linkKV map[string]string

func (m linkKV) Setting(_ context.Context, k string) (string, error) { return m[k], nil }
func (m linkKV) SetSetting(_ context.Context, k, v string) error     { m[k] = v; return nil }

const (
	homeKey = "aG9tZWhvbWVob21laG9tZWhvbWVob21laG9tZWhvbWU="
	vpsKey  = "dnBzdnBzdnBzdnBzdnBzdnBzdnBzdnBzdnBzdnBzdnA="
)

// home (n-…02) backs up to vps's disk (n-…03); the controller is n-…01.
func setUpLinks(t *testing.T) (*Manager, *movingServer, *movingServer, linkKV) {
	t.Helper()
	m, home, vps := setUpMove(t)
	kv := linkKV{}
	st := storetest.Open(t)
	m.BackupStore, m.Settings = st, kv
	st.SetBackupTarget(context.Background(), store.BackupTarget{ServerID: "n-0000000002", Provider: "server:n-0000000003",
		Repository: "rest:http://100.96.0.3:8000/n-0000000002/lab", Created: time.Now()})
	// The controller's own backups don't need a link: every server dials it.
	st.SetBackupTarget(context.Background(), store.BackupTarget{ServerID: "n-0000000003", Provider: "server:n-0000000001",
		Repository: "rest:http://100.96.0.1:8000/n-0000000003/lab", Created: time.Now()})
	m.Registry = &fakeRegistry{peers: map[string]labnet.Peer{
		"n-0000000002": {PublicKey: homeKey, Addr: "100.96.0.2"},
		"n-0000000003": {PublicKey: vpsKey, Addr: "100.96.0.3", Seen: "203.0.113.7:43122"},
	}}
	return m, home, vps, kv
}

func TestDirectLinks(t *testing.T) {
	m, home, vps, kv := setUpLinks(t)
	ctx := context.Background()
	infos := func() map[string]linkInfo { return m.linkInfos(ctx) }

	// Locations unknown: nobody knows where to dial.
	if l := m.linksFor(ctx, infos(), "n-0000000002"); l == nil || len(l.Peers) != 0 || m.LinkPath(ctx, "n-0000000002", "n-0000000003") != PathHub {
		t.Errorf("unknown: %+v", l)
	}
	if m.linksFor(ctx, infos(), "n-0000000001") != nil {
		t.Error("the controller's links are left alone")
	}

	// Both at home, on the same network: each dials the other's address.
	kv[LocationKey+"n-0000000002"], kv[LocationKey+"n-0000000003"] = LocationHome, LocationHome
	home.home, vps.home = []string{"192.168.1.10"}, []string{"192.168.1.20"}
	l := m.linksFor(ctx, infos(), "n-0000000002")
	if len(l.Peers) != 1 || l.Peers[0].PublicKey != vpsKey || l.Peers[0].Endpoint != "192.168.1.20:51820" ||
		l.Peers[0].AllowedIPs[0] != "100.96.0.3/32" || !l.Listen || l.Peers[0].Keepalive != labnet.Keepalive {
		t.Errorf("home's links: %+v", l)
	}
	if l := m.linksFor(ctx, infos(), "n-0000000003"); len(l.Peers) != 1 || l.Peers[0].Endpoint != "192.168.1.10:51820" {
		t.Errorf("vps's links: %+v", l)
	}
	if m.LinkPath(ctx, "n-0000000002", "n-0000000003") != PathDirect {
		t.Error("they link directly")
	}

	// VMs behind the same NAT have the same address: never dial yourself.
	vps.home = []string{"192.168.1.10"}
	if l := m.linksFor(ctx, infos(), "n-0000000002"); len(l.Peers) != 0 {
		t.Errorf("the same address: %+v", l)
	}
	// vps somewhere else, on the internet: dialled at its own address.
	kv[LocationKey+"n-0000000003"] = LocationAway
	if l := m.linksFor(ctx, infos(), "n-0000000002"); len(l.Peers) != 1 || l.Peers[0].Endpoint != "203.0.113.7:51820" || l.Listen {
		t.Errorf("vps on the internet: %+v", l)
	}
	if l := m.linksFor(ctx, infos(), "n-0000000003"); len(l.Peers) != 1 || l.Peers[0].Endpoint != "" || !l.Listen {
		t.Errorf("vps waits for home to dial: %+v", l)
	}

	// An address typed by the Owner wins.
	if err := m.SetLinkAddress(ctx, "n-0000000003", "vps.example.com"); err == nil {
		t.Error("host:port only")
	}
	if err := m.SetLinkAddress(ctx, "n-0000000003", "10.0.2.2:61820"); err != nil {
		t.Fatal(err)
	}
	if l := m.linksFor(ctx, infos(), "n-0000000002"); len(l.Peers) != 1 || l.Peers[0].Endpoint != "10.0.2.2:61820" {
		t.Errorf("typed address: %+v", l)
	}
	m.Wait()
	home.mu.Lock()
	sent := home.last.Links
	home.mu.Unlock()
	if sent == nil || len(sent.Peers) != 1 || sent.Peers[0].PublicKey != vpsKey {
		t.Errorf("home's assignment carries its links: %+v", sent)
	}

	// No handshake within the grace time: through the controller, for a day.
	linkGrace = time.Millisecond
	defer func() { linkGrace = 3 * time.Minute }()
	time.Sleep(5 * time.Millisecond)
	vps.handshakes = map[string]int64{homeKey: time.Now().Unix()}
	m.CheckLinks(ctx)
	if m.LinkPath(ctx, "n-0000000002", "n-0000000003") != PathDirect {
		t.Error("a link that connected stays")
	}
	vps.handshakes = map[string]int64{homeKey: time.Now().Add(-time.Hour).Unix()}
	m.CheckLinks(ctx)
	if m.LinkPath(ctx, "n-0000000002", "n-0000000003") != PathHub || kv[linkFailedKey+pairKey("n-0000000002", "n-0000000003")] == "" {
		t.Error("a link that didn't connect falls back to the controller")
	}
	// A new address is a new chance.
	m.SetLinkAddress(ctx, "n-0000000003", "10.0.2.2:61821")
	if m.LinkPath(ctx, "n-0000000002", "n-0000000003") != PathDirect {
		t.Error("a new address is tried again")
	}
}

// TestEdge: a server with a public address can be the lab's edge; its
// assignment then says so, and every server's names the edge's address
// for the controller's Caddy (SPEC.md §10.5).
func TestEdge(t *testing.T) {
	m, home, vps, _ := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	if es := m.EdgeServers(ctx); len(es) != 1 || es[0].ID != "n-0000000003" || es[0].Address != "203.0.113.7" {
		t.Fatalf("edge servers: %+v", es)
	}
	if _, ok := m.Edge(ctx); ok {
		t.Error("an edge before choosing one")
	}
	var fe *usererr.Error
	if err := m.SetEdge(ctx, "n-0000000002"); !errors.As(err, &fe) {
		t.Errorf("a server without a public address: %v", err)
	}
	if err := m.SetEdge(ctx, "n-0000000003"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if e, ok := m.Edge(ctx); !ok || e.Address != "203.0.113.7" || m.Lab.Public.Edge != "n-0000000003" {
		t.Errorf("the edge: %+v, saved %q", e, m.Lab.Public.Edge)
	}
	vps.mu.Lock()
	edge, vpsLab := vps.last.Edge, vps.last.Lab
	vps.mu.Unlock()
	home.mu.Lock()
	homeEdge, homeLab := home.last.Edge, home.last.Lab
	home.mu.Unlock()
	if edge == nil || edge.Upstream != "100.96.0.1:443" || homeEdge != nil {
		t.Errorf("edge config: vps %+v, home %+v", edge, homeEdge)
	}
	for _, l := range []node.LabContext{vpsLab, homeLab} {
		if len(l.EdgeAddrs) != 1 || l.EdgeAddrs[0] != "100.96.0.3/32" {
			t.Errorf("edge addresses: %+v", l.EdgeAddrs)
		}
	}
	// Where the controller dials it counts too.
	m.Registry = &fakeRegistry{peers: map[string]labnet.Peer{"n-0000000003": {PublicKey: vpsKey, Addr: "100.96.0.3", Dial: "198.51.100.5:51820"}}}
	if es := m.EdgeServers(ctx); len(es) != 1 || es[0].Address != "198.51.100.5" {
		t.Errorf("dialled: %+v", es)
	}
	// Back to port forwarding: nothing about edges is sent.
	if err := m.SetEdge(ctx, ""); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	vps.mu.Lock()
	defer vps.mu.Unlock()
	if vps.last.Edge != nil || len(vps.last.Lab.EdgeAddrs) != 0 {
		t.Errorf("after: %+v %+v", vps.last.Edge, vps.last.Lab.EdgeAddrs)
	}
}

// TestUnreachableSince: the first time a server is seen not answering is
// kept until it answers again.
func TestUnreachableSince(t *testing.T) {
	m := &Manager{Settings: linkKV{}}
	ctx := context.Background()
	first := m.unreachableSince(ctx, "n-0000000002", false)
	if first.IsZero() {
		t.Fatal("not recorded")
	}
	m.Settings.SetSetting(ctx, unreachableKey+"n-0000000002", "1000")
	if again := m.unreachableSince(ctx, "n-0000000002", false); again.Unix() != 1000 {
		t.Errorf("kept: %v", again)
	}
	if !m.unreachableSince(ctx, "n-0000000002", true).IsZero() {
		t.Error("answering: zero")
	}
	if v, _ := m.Settings.Setting(ctx, unreachableKey+"n-0000000002"); v != "" {
		t.Error("answering forgets it")
	}
}

// TestRelayWayIn: Roostack Cloud's relay as the way in: Cloud is asked
// first (a refusal changes nothing), then the relay's edges are the
// addresses every assignment names for Caddy; it and an edge server
// exclude each other.
func TestRelayWayIn(t *testing.T) {
	m, home, _, _ := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	refuse := errors.New("Roostack Cloud's relay isn't open for this account yet.")
	var asked [][]string
	m.Relay = Relay{
		Set: func(_ context.Context, hosts []string, _ config.Countries, _, _ string) error {
			asked = append(asked, hosts)
			return refuse
		},
		Stop:  func(context.Context) error { return nil },
		Edges: func() ([]string, []string) { return []string{"100.81.0.1"}, []string{"198.51.100.10"} },
	}
	if err := m.SetRelay(ctx, true); !errors.Is(err, refuse) || m.RelayOn() || m.Lab.Public.Relay {
		t.Errorf("refused: %v, on %v", err, m.RelayOn())
	}
	refuse = nil
	var countries config.Countries
	m.Relay.Set = func(_ context.Context, hosts []string, c config.Countries, _, _ string) error {
		asked, countries = append(asked, hosts), c
		return nil
	}
	if err := m.SetEdge(ctx, "n-0000000003"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetRelay(ctx, true); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if !m.RelayOn() || m.Lab.Public.Edge != "" {
		t.Errorf("on %v, edge %q", m.RelayOn(), m.Lab.Public.Edge)
	}
	home.mu.Lock()
	addrs := home.last.Lab.EdgeAddrs
	home.mu.Unlock()
	if len(addrs) != 1 || addrs[0] != "100.81.0.1/32" {
		t.Errorf("edge addresses: %v", addrs)
	}
	// Countries: Cloud is told, then they're saved; both at once is refused.
	nz := config.Countries{Allow: []string{"NZ"}}
	if err := m.SetCountries(ctx, nz); err != nil || !slices.Equal(countries.Allow, nz.Allow) || !slices.Equal(m.Lab.Public.Countries.Allow, nz.Allow) {
		t.Errorf("countries: %v, Cloud told %+v, saved %+v", err, countries, m.Lab.Public.Countries)
	}
	if err := m.SetCountries(ctx, config.Countries{Allow: []string{"NZ"}, Block: []string{"RU"}}); err == nil {
		t.Error("both: no error")
	}
	m.Relay.Set = func(context.Context, []string, config.Countries, string, string) error { return refuse }
	refuse = errors.New("no")
	if err := m.SetCountries(ctx, config.Countries{Block: []string{"RU"}}); err == nil || len(m.Countries().Block) != 0 {
		t.Errorf("refused by Cloud, yet saved: %v %+v", err, m.Countries())
	}
	if err := m.SetRelay(ctx, false); err != nil || m.RelayOn() {
		t.Errorf("off: %v", err)
	}
	// Off: kept for when it's on again, Cloud not asked.
	if err := m.SetCountries(ctx, config.Countries{Block: []string{"RU"}}); err != nil || len(m.Countries().Block) != 1 {
		t.Errorf("while off: %v %+v", err, m.Countries())
	}
	m.Wait() // the applies above, before the test's directory goes
}

// TestRelayUpdatesInOrder: an apply's update to the relay waits its turn
// and then sends the countries as they are, or nothing once the relay is
// off, so it never undoes a newer change.
func TestRelayUpdatesInOrder(t *testing.T) {
	m, _, _, _ := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	var mu sync.Mutex
	var sent []config.Countries
	m.Relay = Relay{
		Set: func(_ context.Context, _ []string, c config.Countries, _, _ string) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, c)
			return nil
		},
		Stop:  func(context.Context) error { return nil },
		Edges: func() ([]string, []string) { return []string{"100.81.0.1"}, nil },
	}
	if err := m.SetRelay(ctx, true); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	sends := func() []config.Countries {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(sent)
	}
	if got := sends(); len(got) != 2 { // SetRelay's own, then the apply's
		t.Fatalf("turning on: %d updates", len(got))
	}

	// An apply's update held back while the countries change: it sends the new ones.
	nz := config.Countries{Allow: []string{"NZ"}}
	m.relayMu.Lock()
	m.mu.Lock()
	m.startApplyLocked("")
	m.mu.Unlock()
	local(m).Wait()
	m.mu.Lock()
	m.saveLab(func(l *config.Lab) { l.Public.Countries = nz })
	m.mu.Unlock()
	m.relayMu.Unlock()
	m.Wait()
	if got := sends(); len(got) != 3 || !slices.Equal(got[2].Allow, nz.Allow) {
		t.Errorf("held back, then countries changed: sent %+v", got)
	}

	// Held back while the relay is turned off: nothing is sent.
	m.relayMu.Lock()
	m.mu.Lock()
	m.startApplyLocked("")
	m.mu.Unlock()
	local(m).Wait()
	m.mu.Lock()
	m.saveLab(func(l *config.Lab) { l.Public.Relay = false })
	m.mu.Unlock()
	m.relayMu.Unlock()
	m.Wait()
	if got := sends(); len(got) != 3 {
		t.Errorf("held back, then turned off: sent %+v", got[3:])
	}
}
