package manager

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
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
	m.waits.linkGrace = time.Millisecond
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

// TestEdgeForwards: the edge sends a public port on to the controller,
// which sends it on to the server at home that runs the service; a port
// that's home only, paused, or one the edge uses itself, isn't (SPEC.md
// §10.5).
func TestEdgeForwards(t *testing.T) {
	m, _, _, _ := setUpLinks(t)
	ctx := context.Background()
	services, err := state.LoadServices(m.Lab)
	if err != nil || len(services) != 1 {
		t.Fatal(services, err)
	}
	on := true
	infos := m.linkInfos(ctx)
	forwards := func() ([]agent.PortForward, []agent.PortForward) {
		return m.edgeForwards(m.Lab, services, infos, "n-0000000003")
	}
	s := services[0]             // Forgejo, on n-…02
	s.Instance.SettingUp = false // its install hooks have run
	s.Instance.Ports = map[string]instance.PortOverride{"ssh": {Enabled: &on}}
	if e, c := forwards(); len(e)+len(c) != 0 {
		t.Errorf("home only: %v %v", e, c)
	}
	s.Instance.Ports["ssh"] = instance.PortOverride{Enabled: &on, Exposure: "public"}
	e, c := forwards()
	if want := []agent.PortForward{{Port: 2222, Proto: "tcp", To: "100.96.0.1", Reason: "forgejo ssh"}}; !slices.Equal(e, want) {
		t.Errorf("at the edge: %v", e)
	}
	if want := []agent.PortForward{{Port: 2222, Proto: "tcp", To: "100.96.0.2", FromEdge: true, Reason: "forgejo ssh"}}; !slices.Equal(c, want) {
		t.Errorf("at the controller: %v", c)
	}
	// On the controller, the edge sends it there and that's all.
	s.Instance.Host = m.Lab.NodeID
	if e, c := forwards(); len(e) != 1 || len(c) != 0 {
		t.Errorf("on the controller: %v %v", e, c)
	}
	if e, c := m.edgeForwards(m.Lab, services, infos, ""); e != nil || c != nil {
		t.Errorf("no edge: %v %v", e, c)
	}
	paused := m.Lab
	paused.Public.Pause.All = &config.Pause{Since: time.Now()}
	if e, _ := m.edgeForwards(paused, services, infos, "n-0000000003"); len(e) != 0 {
		t.Errorf("paused: %v", e)
	}
	// On the edge itself it's public there already; and another service
	// can't take the edge's own port.
	other, inst := s, *s.Instance
	other.Instance = &inst
	other.Instance.Host = "n-0000000003"
	other.Instance.Name = "forgejo-2"
	services = append(services, other)
	if e, c := forwards(); len(e)+len(c) != 0 {
		t.Errorf("taken at the edge: %v %v", e, c)
	}
}

// TestServeDirect: a server with a public address serves its public
// routes itself unless the Owner chose otherwise, and every server's
// assignment names it; with an edge, everything goes through the
// controller (SPEC.md §10.5, "Serving directly").
func TestServeDirect(t *testing.T) {
	m, home, vps, _ := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	m.Lab.Domain = "smith.family"
	sent := func(f *movingServer) []string {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.last.Lab.Direct
	}
	if c := m.ServeChoiceOf(ctx, "n-0000000003"); c.Mode != config.ServeDirect || c.Chosen || !c.Public || c.Private {
		t.Errorf("the cloud server by default: %+v", c)
	}
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); c.Mode != config.ServeController || c.Chosen {
		t.Errorf("the home server by default: %+v", c)
	}
	m.Apply()
	m.Wait()
	if d := sent(home); !slices.Equal(d, []string{"n-0000000003"}) || !slices.Equal(sent(vps), d) || !slices.Equal(m.Direct(), d) {
		t.Errorf("direct: home %v, vps %v, manager %v", d, sent(vps), m.Direct())
	}
	// The Owner's choice wins.
	if err := m.SetServe(ctx, "n-0000000003", config.ServeController); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if d := sent(vps); len(d) != 0 || !m.ServeChoiceOf(ctx, "n-0000000003").Chosen {
		t.Errorf("chosen through the controller: %v", d)
	}
	// A home server set to serve directly can't: it has no public address.
	if err := m.SetServe(ctx, "n-0000000002", config.ServeDirect); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); c.Public || c.Why == "" || len(sent(home)) != 0 {
		t.Errorf("a home server set to direct: %+v, sent %v", c, sent(home))
	}
	// The edge itself passes visitors on rather than serving directly.
	m.SetServe(ctx, "n-0000000003", config.ServeDirect)
	m.SetEdge(ctx, "n-0000000003")
	m.Wait()
	if c := m.ServeChoiceOf(ctx, "n-0000000003"); c.Public || c.Why == "" || slices.Contains(sent(vps), "n-0000000003") {
		t.Errorf("the edge itself: %+v, sent %v", c, sent(vps))
	}
	if err := m.SetServe(ctx, m.Lab.NodeID, config.ServeDirect); !errors.Is(err, ErrNotFound) {
		t.Errorf("the controller itself: %v", err)
	}
	if err := m.SetServe(ctx, "n-0000000003", "sideways"); err == nil {
		t.Error("an unknown mode")
	}
}

// TestServeAtHome: a server set to serve directly, at home on the
// controller's network, serves its private routes too; elsewhere, or with
// records Roostack doesn't manage, it doesn't, and says why (SPEC.md
// §10.5, "Serving directly").
func TestServeAtHome(t *testing.T) {
	m, home, _, kv := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	m.Lab.Domain, m.Lab.DNS.ManageRecords = "smith.family", true
	was := homeAddrs
	homeAddrs = func() []string { return []string{"192.168.1.5"} }
	t.Cleanup(func() { homeAddrs = was })
	if err := m.SetServe(ctx, "n-0000000002", config.ServeDirect); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); c.Private || !strings.Contains(c.Why, "set Where to At home") {
		t.Errorf("where it is unknown: %+v", c)
	}
	kv[LocationKey+"n-0000000002"] = LocationHome
	home.home = []string{"10.0.0.7"}
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); c.Private || !strings.Contains(c.Why, "same home network") {
		t.Errorf("on another network: %+v", c)
	}
	home.home = []string{"192.168.1.5", "192.168.1.10"}
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); !c.Private || c.Public || !strings.Contains(c.Why, "public services will go through") {
		t.Errorf("at home: %+v", c)
	}
	m.Apply()
	m.Wait()
	home.mu.Lock()
	sent := home.last.Lab.DirectHome
	home.mu.Unlock()
	if !slices.Equal(sent, []string{"n-0000000002"}) || m.DirectHome()["n-0000000002"] != "192.168.1.10" {
		t.Errorf("sent %v, addresses %v", sent, m.DirectHome())
	}
	m.mu.Lock()
	m.saveLab(func(l *config.Lab) { l.DNS.ManageRecords = false })
	m.mu.Unlock()
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); c.Private || !strings.Contains(c.Why, "DNS records") {
		t.Errorf("records not managed: %+v", c)
	}
}

func TestHomeAddrIn(t *testing.T) {
	for _, tc := range []struct {
		addrs, own []string
		want       string
	}{
		{[]string{"192.168.1.10"}, []string{"192.168.1.5"}, "192.168.1.10"},
		{[]string{"10.0.2.15"}, []string{"10.0.2.15"}, ""},
		{[]string{"192.168.2.10"}, []string{"192.168.1.5"}, ""},
		{[]string{"fe80::1", "192.168.1.9"}, []string{"192.168.1.5"}, "192.168.1.9"},
	} {
		if got := homeAddrIn(tc.addrs, tc.own); got != tc.want {
			t.Errorf("homeAddrIn(%v, %v) = %q, want %q", tc.addrs, tc.own, got, tc.want)
		}
	}
}

// TestServeThroughEdge: with the lab's edge as the way in, a cloud server
// still serves its public names itself, the edge itself doesn't, and a
// home server set to serve directly gets a link of its own with the edge,
// which passes its names straight to it (SPEC.md §10.5).
func TestServeThroughEdge(t *testing.T) {
	m, home, vps, _ := setUpLinks(t)
	ctx := context.Background()
	m.UpdateLab = func(change func(*config.Lab)) error { change(&m.Lab); return nil }
	m.Lab.Domain = "smith.family"
	m.Registry = &fakeRegistry{peers: map[string]labnet.Peer{
		"n-0000000002": {PublicKey: homeKey, Addr: "100.96.0.2"},
		"n-0000000003": {PublicKey: vpsKey, Addr: "100.96.0.3", Seen: "203.0.113.7:43122"},
		"n-0000000004": {PublicKey: "cloudKey", Addr: "100.96.0.4", Seen: "198.51.100.9:43122"},
	}}
	if err := m.SetEdge(ctx, "n-0000000003"); err != nil {
		t.Fatal(err)
	}
	if c := m.ServeChoiceOf(ctx, "n-0000000004"); !c.Public {
		t.Errorf("a cloud server with an edge: %+v", c)
	}
	if c := m.ServeChoiceOf(ctx, "n-0000000003"); c.Public || !strings.Contains(c.Why, "edge") {
		t.Errorf("the edge itself: %+v", c)
	}
	if err := m.SetServe(ctx, "n-0000000002", config.ServeDirect); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	l := m.linksFor(ctx, m.linkInfos(ctx), "n-0000000002")
	if l == nil || len(l.Peers) != 1 || l.Peers[0].Endpoint != "203.0.113.7:51820" {
		t.Fatalf("the home server's link to the edge: %+v", l)
	}
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); !c.Public {
		t.Errorf("a home server linked to the edge: %+v", c)
	}
	home.mu.Lock()
	direct := home.last.Lab.Direct
	home.mu.Unlock()
	vps.mu.Lock()
	edge := vps.last.Edge
	vps.mu.Unlock()
	if !slices.Contains(direct, "n-0000000002") || edge == nil {
		t.Errorf("sent direct %v, edge %+v", direct, edge)
	}
	// Forgejo on the home server, public: the edge passes its name
	// straight to the home server's Caddy.
	services, err := state.LoadServices(m.Lab)
	if err != nil || len(services) != 1 {
		t.Fatal(services, err)
	}
	r := services[0].Instance.Routes["web"]
	r.Exposure = pkgspec.ExposurePublic
	services[0].Instance.Routes["web"], services[0].Instance.SettingUp = r, false
	lab := m.labNow()
	lab.Direct = direct
	if got := m.edgeDirect(lab, services, m.linkInfos(ctx)); len(got["100.96.0.2:443"]) != 1 || len(got) != 1 {
		t.Errorf("the edge's names for the home server: %v", got)
	}
	// The link didn't connect: through the controller, saying why.
	m.Settings.SetSetting(ctx, linkFailedKey+pairKey("n-0000000002", "n-0000000003"), strconv.FormatInt(time.Now().Unix(), 10))
	if c := m.ServeChoiceOf(ctx, "n-0000000002"); c.Public || !strings.Contains(c.Why, "UDP 51820") {
		t.Errorf("a link that didn't connect: %+v", c)
	}
}
