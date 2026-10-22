package dnsrecords

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns/dnstest"
	"roostack.cloud/roostack/internal/store"
)

type fakeSecrets map[string]string

func (f fakeSecrets) Read(_ context.Context, name string) ([]byte, error) {
	return []byte(f[name]), nil
}

// newManager uses the real Gandi record code against the fake APIs
// (dnstest), with fake LAN IP and DNS lookups.
func newManager(t *testing.T, dnsCfg config.DNS, local, public []string) (*Manager, gandi) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	g := gandi{dnstest.Start(t)}
	lab := config.Default()
	lab.Domain = "lab.example.com"
	lab.DNS = dnsCfg
	m := &Manager{
		Lab: lab, Secrets: fakeSecrets{"roostack-caddy-dns-bearer_token": "pat"}, Store: st, HTTP: g.Client(),
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		LANIP:        func() (string, error) { return "192.168.1.20", nil },
		LocalLookup:  func(context.Context, string) ([]string, error) { return local, nil },
		PublicLookup: func(context.Context, string) ([]string, error) { return public, nil },
	}
	m.UpdateLab = func(change func(*config.Lab)) error { l := m.Lab; change(&l); return nil }
	return m, g
}

var managed = config.DNS{Provider: "gandi", Fields: []string{"bearer_token"}, Zone: "example.com", ManageRecords: true}

func TestCreatesWildcardAndChecks(t *testing.T) {
	m, g := newManager(t, managed, []string{"192.168.1.20"}, []string{"192.168.1.20"})
	st := m.Sync(context.Background())
	if st.State != StateOK || st.Record != "*.lab.example.com" || st.IP != "192.168.1.20" {
		t.Fatalf("status %+v", st)
	}
	if got := g.get("*.lab"); got != "192.168.1.20" {
		t.Errorf("record = %q", got)
	}
	if reqs := strings.Join(g.Requests(), "\n"); !strings.Contains(reqs, "auth=Bearer pat") {
		t.Errorf("credentials not read from secrets:\n%s", reqs)
	}

	// The LAN IP changes: Roostack updates its own record.
	m.LANIP = func() (string, error) { return "192.168.1.99", nil }
	m.LocalLookup = func(context.Context, string) ([]string, error) { return []string{"192.168.1.99"}, nil }
	if st := m.Sync(context.Background()); st.State != StateOK || g.get("*.lab") != "192.168.1.99" {
		t.Errorf("after IP change: %+v, record %q", st, g.get("*.lab"))
	}

	// The records Roostack manages are listed with their current values.
	recs, err := m.Records(context.Background())
	if err != nil || len(recs) != 1 || recs[0] != (Record{Name: "*.lab.example.com", Type: "A", Value: "192.168.1.99", Via: "gandi"}) {
		t.Errorf("records = %+v, %v", recs, err)
	}
}

func TestNeverOverwritesOthersRecords(t *testing.T) {
	m, g := newManager(t, managed, nil, nil)
	g.put("*.lab", "203.0.113.5") // someone else's record
	st := m.Sync(context.Background())
	if st.State != StateConflict || !strings.Contains(st.Message, "didn't create that record") {
		t.Fatalf("status %+v", st)
	}
	if g.get("*.lab") != "203.0.113.5" {
		t.Error("a record Roostack didn't create must not be changed")
	}

	// An existing record with the right value is adopted, and then managed.
	g.put("*.lab", "192.168.1.20")
	m.LocalLookup = func(context.Context, string) ([]string, error) { return []string{"192.168.1.20"}, nil }
	if st := m.Sync(context.Background()); st.State != StateOK {
		t.Fatalf("adopt: %+v", st)
	}
	m.LANIP = func() (string, error) { return "192.168.1.30", nil }
	m.Sync(context.Background())
	if g.get("*.lab") != "192.168.1.30" {
		t.Error("an adopted record should be kept up to date")
	}
}

func TestRouterBlocksPrivateAnswers(t *testing.T) {
	m, _ := newManager(t, managed, nil, []string{"192.168.1.20"})
	st := m.Sync(context.Background())
	if st.State != StateBlocked || !strings.Contains(st.Message, "rebinding") {
		t.Errorf("status %+v", st)
	}
}

func TestNotVisibleYet(t *testing.T) {
	m, _ := newManager(t, managed, nil, nil)
	if st := m.Sync(context.Background()); st.State != StatePending {
		t.Errorf("status %+v", st)
	}
}

func TestManualAndRisky(t *testing.T) {
	cfg := managed
	cfg.ManageRecords = false
	m, g := newManager(t, cfg, nil, nil)
	st := m.Sync(context.Background())
	if st.State != StateManual || !strings.Contains(st.Message, "*.lab.example.com  A  192.168.1.20") || g.get("*.lab") != "" {
		t.Errorf("manual: %+v", st)
	}
	// Once the user adds it by hand and home DNS returns it, it's fine.
	m.LocalLookup = func(context.Context, string) ([]string, error) { return []string{"192.168.1.20"}, nil }
	if st := m.Sync(context.Background()); !strings.Contains(st.Message, "can reach your services") {
		t.Errorf("manual, working: %+v", st)
	}

	risky := config.DNS{Provider: "namecheap", Fields: []string{"user", "api_key"}, Zone: "example.com", ManageRecords: true}
	m, _ = newManager(t, risky, nil, nil)
	if st := m.Sync(context.Background()); st.State != StateManual {
		t.Errorf("Namecheap without opt-in must not be managed: %+v", st)
	}
}

// gandi is the fake APIs' zone (dnstest), as these tests read and change
// it at Gandi: a name's value of a type as Gandi holds it, "" if none.
type gandi struct{ *dnstest.Fake }

// get is a name's A record; getType any type's.
func (g gandi) get(name string) string { return g.getType(name, "A") }

func (g gandi) getType(name, typ string) string {
	return strings.Join(g.Get(dnstest.Zone, name, typ), ",")
}

// put sets a name's A record behind Roostack's back.
func (g gandi) put(name, ip string) { g.Set(dnstest.Zone, name, "A", []string{ip}) }

func TestTakeOver(t *testing.T) {
	m, g := newManager(t, managed, []string{"192.168.1.20"}, []string{"192.168.1.20"})
	g.put("*.lab", "203.0.113.5")
	if err := m.TakeOver(context.Background()); err == nil {
		t.Error("nothing to take over before a conflict is found")
	}
	st := m.Sync(context.Background())
	if st.State != StateConflict || st.Current != "203.0.113.5" {
		t.Fatalf("status %+v", st)
	}
	if err := m.TakeOver(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := m.Sync(context.Background()); st.State != StateOK || g.get("*.lab") != "192.168.1.20" {
		t.Errorf("after taking over: %+v, record %q", st, g.get("*.lab"))
	}
}

func TestSetManaging(t *testing.T) {
	cfg := managed
	cfg.ManageRecords = false
	m, g := newManager(t, cfg, []string{"192.168.1.20"}, []string{"192.168.1.20"})
	var saved config.Lab
	m.UpdateLab = func(change func(*config.Lab)) error { change(&saved); return nil }

	if st := m.Sync(context.Background()); st.State != StateManual || !st.CanManage || st.Managing {
		t.Fatalf("before: %+v", st)
	}
	if err := m.SetManaging(true, false); err != nil {
		t.Fatal(err)
	}
	if !saved.DNS.ManageRecords {
		t.Error("the choice must be saved to lab.yaml")
	}
	if st := m.Sync(context.Background()); st.State != StateOK || g.get("*.lab") != "192.168.1.20" || !st.Managing {
		t.Errorf("after enabling: %+v", st)
	}

	risky := config.DNS{Provider: "namecheap", Fields: []string{"user", "api_key"}, Zone: "example.com"}
	m, _ = newManager(t, risky, nil, nil)
	if err := m.SetManaging(true, false); !errors.Is(err, ErrNeedsRisk) {
		t.Errorf("namecheap without accepting the risk: %v", err)
	}
	if err := m.SetManaging(true, true); err != nil || !m.Lab.DNS.AllowRiskyRecords {
		t.Errorf("namecheap with the risk accepted: %v", err)
	}
}

func TestSetManagingWithoutSaverIsAnError(t *testing.T) {
	m, _ := newManager(t, managed, nil, nil)
	m.UpdateLab = nil
	if err := m.SetManaging(true, false); err == nil {
		t.Error("expected an error, not a crash")
	}
}

func TestHomeDNSOverridesDomain(t *testing.T) {
	m, _ := newManager(t, managed, []string{"192.168.86.48"}, []string{"192.168.1.20"})
	st := m.Sync(context.Background())
	if st.State != StateOverridden || !strings.Contains(st.Message, "192.168.86.48 instead of this server (192.168.1.20)") {
		t.Errorf("status %+v", st)
	}
}
