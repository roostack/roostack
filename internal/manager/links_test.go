package manager

import (
	"context"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/store"
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
	st, err := store.Open(t.TempDir() + "/roostack.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
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
