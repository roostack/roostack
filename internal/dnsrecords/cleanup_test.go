package dnsrecords

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/dns/dnstest"
	"roostack.cloud/roostack/internal/store"
)

// Deleting records, against every provider's fake API (dnstest): records
// of addresses no longer public, and an old address's when it ends (SPEC.md
// §6.6, §6.11). Each checks a delete that fails, a record the Owner
// changed by hand (never deleted), and a restart part-way through.

// providerCfg is a provider's settings at Roostack, managing records.
func providerCfg(id string) config.DNS {
	var fields []string
	for _, f := range dns.Get(id).Fields {
		if !f.Optional {
			fields = append(fields, f.Key)
		}
	}
	return config.DNS{Provider: id, Fields: fields, Zone: "example.com", ManageRecords: true, AllowRiskyRecords: id == "namecheap"}
}

// anySecrets has a value for every secret: the fakes don't check them
// (dnstest.Fake.Refuse plays a login that no longer works).
type anySecrets struct{}

func (anySecrets) Read(context.Context, string) ([]byte, error) { return []byte("k"), nil }

// cleanupLab is a lab on domain with provider, against the fake APIs,
// with a store a restarted Roostack gets back (restart).
type cleanupLab struct {
	t     *testing.T
	fake  *dnstest.Fake
	store *store.Store
	lanIP string
	entry string
	hosts []string
}

func newCleanupLab(t *testing.T) *cleanupLab {
	st, err := store.Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &cleanupLab{t: t, fake: dnstest.Start(t), store: st, lanIP: "192.168.1.20", entry: "81.2.69.160"}
}

// manager is Roostack (as after a start) on domain with provider, the
// old address as given.
func (c *cleanupLab) manager(domain, provider string, old *config.OldAddress) *Manager {
	lab := config.Default()
	lab.Domain, lab.DNS, lab.OldAddress = domain, providerCfg(provider), old
	m := &Manager{
		Lab: lab, Secrets: anySecrets{}, Store: c.store, HTTP: c.fake.Client(),
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		LANIP: func() (string, error) { return c.lanIP, nil },
		LocalLookup: func(context.Context, string) ([]string, error) {
			return []string{c.lanIP}, nil
		},
		PublicLookup: func(context.Context, string) ([]string, error) { return []string{c.lanIP, c.entry}, nil },
		PublicHosts:  func() []string { return c.hosts },
		Entry:        entryFunc(func() string { return c.entry }),
		Protected:    func(context.Context) bool { return true },
	}
	return m
}

type entryFunc func() string

func (entryFunc) Kind() string                              { return "port forwarding" }
func (e entryFunc) Address(context.Context) (string, error) { return e(), nil }

// get reads a record through provider's API ("" if none).
func (c *cleanupLab) get(provider, name string) string {
	c.t.Helper()
	got, err := dns.Get(provider).GetRecord(context.Background(), c.fake.Client(), map[string]string{}, "example.com", name, "A")
	if err != nil {
		c.t.Fatalf("reading %s: %v", name, err)
	}
	return strings.Join(got, ",")
}

// byHand changes a record at the provider, as the Owner would.
func (c *cleanupLab) byHand(provider, name, value string) {
	c.t.Helper()
	if err := dns.Get(provider).SetRecord(context.Background(), c.fake.Client(), map[string]string{}, "example.com", name, "A", value); err != nil {
		c.t.Fatal(err)
	}
}

// owned lists the names of Roostack's records.
func owned(t *testing.T, m *Manager) []string {
	t.Helper()
	recs, err := m.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range recs {
		names = append(names, r.Name)
	}
	return names
}

func eachProvider(t *testing.T, test func(t *testing.T, provider string)) {
	for _, p := range dns.Providers() {
		t.Run(p.ID, func(t *testing.T) { test(t, p.ID) })
	}
}

// A service made private loses its public record; a delete that fails is
// said and tried again (here after a restart); one the Owner changed is
// left alone and forgotten.
func TestPrivateAgainDeletes(t *testing.T) {
	eachProvider(t, func(t *testing.T, provider string) {
		ctx := context.Background()
		c := newCleanupLab(t)
		c.hosts = []string{"photos.lab.example.com", "vault.lab.example.com"}
		m := c.manager("lab.example.com", provider, nil)
		m.Sync(ctx)
		if c.get(provider, "photos.lab") != c.entry || c.get(provider, "vault.lab") != c.entry || c.get(provider, "*.lab") != c.lanIP {
			t.Fatalf("public: photos %q, vault %q", c.get(provider, "photos.lab"), c.get(provider, "vault.lab"))
		}
		c.byHand(provider, "vault.lab", "198.51.100.7")

		// Both private, with the provider failing changes.
		c.hosts = nil
		c.fake.FailWrites(provider, 0)
		st := m.Sync(ctx)
		if len(st.NotDeleted) != 1 || st.NotDeleted[0].Host != "photos.lab.example.com" ||
			!strings.Contains(st.NotDeleted[0].Message, "couldn't delete") || c.get(provider, "photos.lab") != c.entry {
			t.Fatalf("a delete that fails: %+v, record %q", st.NotDeleted, c.get(provider, "photos.lab"))
		}
		if !slices.Contains(owned(t, m), "photos.lab.example.com") {
			t.Error("a record Roostack couldn't delete stays Roostack's, to try again")
		}
		if c.get(provider, "vault.lab") != "198.51.100.7" || slices.Contains(owned(t, m), "vault.lab.example.com") {
			t.Errorf("changed by hand: %q, and must no longer be Roostack's: %v", c.get(provider, "vault.lab"), owned(t, m))
		}

		// Restarted, the provider working again: the delete is tried again.
		c.fake.FailWrites(provider, -1)
		m = c.manager("lab.example.com", provider, nil)
		st = m.Sync(ctx)
		if len(st.NotDeleted) != 0 || c.get(provider, "photos.lab") != "" {
			t.Errorf("after the restart: %+v, record %q", st.NotDeleted, c.get(provider, "photos.lab"))
		}
		if c.get(provider, "vault.lab") != "198.51.100.7" || c.get(provider, "*.lab") != c.lanIP {
			t.Errorf("others: vault %q, wildcard %q", c.get(provider, "vault.lab"), c.get(provider, "*.lab"))
		}
		if got := owned(t, m); !slices.Equal(got, []string{"*.lab.example.com"}) {
			t.Errorf("Roostack's records: %v", got)
		}
	})
}

// An old address's records follow the home's address while it works, and
// are deleted with its login when it ends: a login that no longer works
// is said and keeps them; a restart part-way through carries on; ones the
// Owner changed stay; the current address's are never touched.
func TestOldAddressRecords(t *testing.T) {
	eachProvider(t, func(t *testing.T, provider string) {
		ctx := context.Background()
		c := newCleanupLab(t)
		// The new address is on another provider (with its own login).
		now := "gandi"
		if provider == now {
			now = "cloudflare"
		}
		c.hosts = []string{"photos.old.example.com", "vault.old.example.com", "git.old.example.com"}
		c.manager("old.example.com", provider, nil).Sync(ctx)

		// Moved to lab.example.com; vault and git are private now, and git's
		// record was changed by hand.
		old := &config.OldAddress{Domain: "old.example.com", DNS: providerCfg(provider), Until: time.Now().Add(30 * 24 * time.Hour)}
		c.hosts = []string{"photos.lab.example.com"}
		c.byHand(provider, "git.old", "198.51.100.7")
		m := c.manager("lab.example.com", now, old)
		c.lanIP, c.entry = "192.168.1.99", "81.2.69.200" // the home's addresses change
		st := m.Sync(ctx)
		if c.get(provider, "*.old") != c.lanIP || c.get(provider, "photos.old") != c.entry {
			t.Errorf("kept up to date: wildcard %q, photos %q", c.get(provider, "*.old"), c.get(provider, "photos.old"))
		}
		if c.get(provider, "vault.old") != "" || c.get(provider, "git.old") != "198.51.100.7" {
			t.Errorf("private: vault %q (deleted), git %q (the Owner's)", c.get(provider, "vault.old"), c.get(provider, "git.old"))
		}
		if st.Old == nil || st.Old.Ended || st.Old.Message != "" || len(st.Old.Records) != 2 {
			t.Errorf("while it works: %+v", st.Old)
		}
		if c.get(now, "*.lab") != c.lanIP || c.get(now, "photos.lab") != c.entry {
			t.Fatalf("the new address: wildcard %q, photos %q", c.get(now, "*.lab"), c.get(now, "photos.lab"))
		}

		// It ends, and the old login no longer works.
		old.Until = time.Now()
		c.fake.Refuse(provider, true)
		if m.RemoveOld(ctx, *old) {
			t.Fatal("done, with a login refused")
		}
		if o := m.Status().Old; o == nil || !o.Ended || len(o.Records) != 2 || !strings.Contains(o.Message, "couldn't delete") ||
			!strings.Contains(o.Message, dns.Get(provider).Title) {
			t.Errorf("a login refused: %+v", o)
		}
		// Working again, but the provider fails after one delete.
		c.fake.Refuse(provider, false)
		c.fake.FailWrites(provider, 1)
		if m.RemoveOld(ctx, *old) || c.get(provider, "*.old") != "" || c.get(provider, "photos.old") != c.entry {
			t.Fatalf("part-way: wildcard %q, photos %q", c.get(provider, "*.old"), c.get(provider, "photos.old"))
		}
		// Restarted (lab.yaml keeps the old address); meanwhile the Owner
		// changed what's left.
		c.fake.FailWrites(provider, -1)
		c.byHand(provider, "photos.old", "198.51.100.8")
		m = c.manager("lab.example.com", now, old)
		if !m.RemoveOld(ctx, *old) {
			t.Fatalf("after the restart: %+v", m.Status().Old)
		}
		if m.Status().Old != nil || c.get(provider, "photos.old") != "198.51.100.8" || c.get(provider, "git.old") != "198.51.100.7" {
			t.Errorf("done: %+v; the Owner's: photos %q, git %q", m.Status().Old, c.get(provider, "photos.old"), c.get(provider, "git.old"))
		}
		if got := owned(t, m); !slices.Equal(got, []string{"*.lab.example.com", "photos.lab.example.com"}) {
			t.Errorf("Roostack's records: %v", got)
		}
		if c.get(now, "*.lab") != c.lanIP || c.get(now, "photos.lab") != c.entry {
			t.Error("the current address's records must stay")
		}
	})
}

// Old and new addresses in one zone, the new one under the old
// (lab.example.com, then home.lab.example.com): each address's sync and
// cleanup keeps to its own records.
func TestOldAddressSameZone(t *testing.T) {
	ctx := context.Background()
	c := newCleanupLab(t)
	c.hosts = []string{"photos.lab.example.com"}
	c.manager("lab.example.com", "cloudflare", nil).Sync(ctx)
	old := &config.OldAddress{Domain: "lab.example.com", DNS: providerCfg("cloudflare"), Until: time.Now().Add(time.Hour)}
	c.hosts = []string{"photos.home.lab.example.com"}
	m := c.manager("home.lab.example.com", "cloudflare", old)
	m.Sync(ctx)
	if c.get("cloudflare", "photos.lab") != c.entry || c.get("cloudflare", "*.lab") != c.lanIP {
		t.Errorf("the old address's records, while it works: photos %q, wildcard %q",
			c.get("cloudflare", "photos.lab"), c.get("cloudflare", "*.lab"))
	}
	if c.get("cloudflare", "photos.home.lab") != c.entry || c.get("cloudflare", "*.home.lab") != c.lanIP {
		t.Error("the new address's records")
	}
	old.Until = time.Now()
	if !m.RemoveOld(ctx, *old) || c.get("cloudflare", "photos.lab") != "" || c.get("cloudflare", "*.lab") != "" {
		t.Errorf("ended: photos %q, wildcard %q", c.get("cloudflare", "photos.lab"), c.get("cloudflare", "*.lab"))
	}
	if c.get("cloudflare", "photos.home.lab") != c.entry || c.get("cloudflare", "*.home.lab") != c.lanIP {
		t.Error("the new address's records, under the old domain's name, must stay")
	}
}

// A roost.ac name's records go when Roostack Cloud releases the name;
// where record management was off, they're left: either way, no longer
// Roostack's, with nothing asked of the provider.
func TestOldAddressNotDeleted(t *testing.T) {
	ctx := context.Background()
	for _, old := range []config.OldAddress{
		{Domain: "smiths.roost.ac", DNS: config.DNS{Provider: config.ProviderCloud, ManageRecords: true}},
		{Domain: "old.example.com", DNS: config.DNS{Provider: "cloudflare", Zone: "example.com"}},
	} {
		c := newCleanupLab(t)
		c.fake.Refuse("cloudflare", true)
		m := c.manager("lab.example.com", "gandi", &old)
		m.Store.SetSetting(ctx, ownerKey("*."+old.Domain, "A"), "192.168.1.20")
		if !m.RemoveOld(ctx, old) || len(owned(t, m)) != 0 || m.Status().Old != nil {
			t.Errorf("%s: %v, %+v", old.Domain, owned(t, m), m.Status().Old)
		}
	}
}

// Records under no address the lab has (left when an earlier old address
// stopped early, say) are listed, until the Owner says they're gone.
func TestLeftRecords(t *testing.T) {
	ctx := context.Background()
	c := newCleanupLab(t)
	old := &config.OldAddress{Domain: "old.example.com", DNS: providerCfg("gandi"), Until: time.Now().Add(time.Hour)}
	m := c.manager("lab.example.com", "gandi", old)
	m.Store.SetSetting(ctx, ownerKey("*.older.example.com", "A"), "192.168.1.20")
	m.Store.SetSetting(ctx, ownerKey("*.old.example.com", "A"), "192.168.1.20")
	st := m.Sync(ctx)
	if len(st.Left) != 1 || st.Left[0].Name != "*.older.example.com" {
		t.Fatalf("left: %+v", st.Left)
	}
	if err := m.Forget(ctx); err != nil {
		t.Fatal(err)
	}
	if got := owned(t, m); !slices.Equal(got, []string{"*.lab.example.com", "*.old.example.com"}) || m.Status().Left != nil {
		t.Errorf("after forgetting: %v (the working old address's stays)", got)
	}
}
