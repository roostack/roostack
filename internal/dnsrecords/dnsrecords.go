// Package dnsrecords keeps the lab's DNS records in place (SPEC.md §6.5.1,
// stage 1): a wildcard record *.<domain> pointing at the lab's LAN IP, so
// devices at home reach private services without any local DNS. It also
// checks that the home network's resolver actually returns that record
// (some routers block private IPs in DNS answers).
//
// Each public address gets its own record, pointing at the public entry's
// address (SPEC.md §6.11, stage 2; public.go). One made private again
// loses it.
//
// After the lab moves to another address, the old one's records are kept
// up to date while it works, and deleted when it ends (SPEC.md §6.6;
// old.go).
//
// On a lab on the internet (SPEC.md §6.12) the wildcard points at the
// lab's public address instead, and covers public addresses too: there's
// no home network, and nothing else to point them at.
package dnsrecords

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/store"
)

// States shown on the dashboard.
const (
	StateOK          = "ok"          // record in place and reachable from home
	StatePending     = "pending"     // not visible yet (DNS propagation)
	StateBlocked     = "blocked"     // the router drops the answer
	StateOverridden  = "overridden"  // home DNS answers with a different address
	StateConflict    = "conflict"    // a record Roostack didn't create is in the way
	StateError       = "error"       // the provider API failed
	StateManual      = "manual"      // Roostack doesn't manage records; add it by hand
	StateUnsupported = "unsupported" // provider can't manage records
)

// Status is the latest result, for the dashboard.
type Status struct {
	State   string
	Record  string // e.g. *.lab.example.com
	IP      string
	Message string
	Checked time.Time
	// Current is the record's existing value, on a conflict.
	Current string
	// Reachable: the home network's resolver returns this server's address.
	Reachable bool
	// Syncing: a sync (update and check) is running now.
	Syncing bool
	// Provider details for the dashboard's controls.
	Provider     string // title, e.g. "Cloudflare"
	CanManage    bool   // the provider supports record management
	Risky        bool   // it needs the Namecheap-style opt-in
	Managing     bool   // record management is switched on
	AcceptedRisk bool
	// Public is each public address's record (public.go); PublicIP the
	// address they point at.
	Public   []PublicRecord
	PublicIP string
	// Internet: the lab is on the internet, and the wildcard points at its
	// public address (SPEC.md §6.12).
	Internet bool
	// Link is link.<domain>'s record, once it's wanted (link.go).
	Link *PublicRecord
	// Proof is the TXT record proving the domain is the lab's, while it's
	// relayed through Roostack Cloud (relay.go).
	Proof *PublicRecord
	// NotDeleted are records of addresses no longer public that Roostack
	// couldn't delete (it tries again at each sync).
	NotDeleted []PublicRecord
	// Old is how the records Roostack set under the lab's old address
	// stand, while there are any (old.go).
	Old *OldRecords
	// Left are records Roostack set under an address the lab no longer
	// has, which it couldn't delete: the Owner deletes them by hand.
	Left []Record
}

// Secrets reads Podman secret values (the DNS credentials).
type Secrets interface {
	Read(ctx context.Context, name string) ([]byte, error)
}

// Manager keeps the wildcard record in place.
type Manager struct {
	Lab     config.Lab
	Secrets Secrets
	Store   *store.Store
	HTTP    *http.Client
	Log     *slog.Logger
	// LANIP finds the lab's LAN address (tests replace it).
	LANIP func() (string, error)
	// LocalLookup resolves through the home network's DNS; PublicLookup
	// through a public resolver. Tests replace both.
	LocalLookup, PublicLookup func(ctx context.Context, host string) ([]string, error)
	// Wait is how long to wait for a new record to become visible.
	Wait time.Duration
	// UpdateLab saves a change to lab.yaml (config.Update).
	UpdateLab func(change func(*config.Lab)) error
	// PublicHosts lists the public addresses (full host names); nil means
	// none. Entry is how visitors get in, which gives the address their
	// records point at. Protected says whether CrowdSec is up: records only
	// go public once it is (SPEC.md §6.11).
	PublicHosts func() []string
	Entry       public.Entry
	Protected   func(ctx context.Context) bool
	// LinkWanted says someone chose link.<domain> as the address other
	// servers and labs reach this one at: its record is kept pointing at
	// the entry's address (link.go).
	LinkWanted func() bool
	// Paused stops updating records (optional): this copy of the lab is
	// out of date, another one runs it (backup.Fence).
	Paused func() bool
	// Relay says the lab's own domain is relayed through Roostack Cloud
	// (optional): public addresses are then CNAMEs to its relay name, and
	// a TXT record proves the domain (relay.go).
	Relay func() (Relayed, bool)
	// ProofSet is told the proof's TXT record is in place (optional), so
	// Roostack Cloud can be asked to look for it at once.
	ProofSet func()
	// OldAddress is the lab's previous address as lab.yaml has it now
	// (optional; Lab.OldAddress otherwise): kept after it stops working,
	// until its records are deleted (SPEC.md §6.6).
	OldAddress func() *config.OldAddress

	mu     sync.Mutex
	status Status
	// notDeleted is the latest sync's NotDeleted.
	notDeleted []PublicRecord
	// oldZones caches each old domain's zone, when lab.yaml has none.
	oldZones map[string]string
	// syncMu makes sure only one sync runs at a time.
	syncMu  sync.Mutex
	syncing bool
	wake    chan struct{}
}

// ErrNeedsRisk means the provider needs the explicit risk opt-in.
var ErrNeedsRisk = errors.New("tick the box confirming you understand the risk")

// SetManaging switches record management on or off from the UI. Turning it
// off leaves existing records in place. The change is saved and applied
// straight away.
func (m *Manager) SetManaging(on, acceptRisk bool) error {
	m.mu.Lock()
	p := dns.Get(m.Lab.DNS.Provider)
	m.mu.Unlock()
	if p == nil {
		return errors.New("set up your domain first")
	}
	if m.UpdateLab == nil {
		return errors.New("dnsrecords: UpdateLab is not set")
	}
	if on && p.RiskyRecords() && !acceptRisk {
		return ErrNeedsRisk
	}
	risk := acceptRisk && p.RiskyRecords()
	if err := m.UpdateLab(func(l *config.Lab) {
		l.DNS.ManageRecords, l.DNS.AllowRiskyRecords = on, risk
	}); err != nil {
		return err
	}
	m.mu.Lock()
	m.Lab.DNS.ManageRecords, m.Lab.DNS.AllowRiskyRecords = on, risk
	m.syncing = true // shown straight away; the sync clears it
	m.mu.Unlock()
	m.Refresh()
	return nil
}

// TakeOver lets Roostack manage a record it didn't create (the conflict shown
// on the dashboard), overwriting its current value on the next sync.
func (m *Manager) TakeOver(ctx context.Context) error {
	st := m.Status()
	if st.State != StateConflict {
		return errors.New("there is no conflicting record to take over")
	}
	if err := m.Store.SetSetting(ctx, ownerKey(st.Record, "A"), "taken over"); err != nil {
		return err
	}
	m.Log.Warn("taking over DNS record", "record", st.Record, "was", st.Current)
	m.Refresh()
	return nil
}

// Refresh asks Run to sync now.
func (m *Manager) Refresh() {
	m.mu.Lock()
	if m.wake == nil {
		m.wake = make(chan struct{}, 1)
	}
	w := m.wake
	m.mu.Unlock()
	select {
	case w <- struct{}{}:
	default:
	}
}

// Status returns the latest result.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status
	st.Syncing = m.syncing
	return st
}

func (m *Manager) set(st Status) Status {
	st.Checked = time.Now()
	m.mu.Lock()
	m.status = st
	m.mu.Unlock()
	if st.State != StateOK && st.State != StateManual {
		m.Log.Warn("DNS records", "state", st.State, "record", st.Record, "detail", st.Message)
	}
	return st
}

// Run syncs now, then every interval (catching a changed LAN IP), more
// often while anything is public, or sooner when Refresh is called.
func (m *Manager) Run(ctx context.Context, interval time.Duration) {
	m.Refresh() // make sure wake exists
	m.mu.Lock()
	wake := m.wake
	m.mu.Unlock()
	for {
		if m.Paused != nil && m.Paused() {
			m.Log.Warn("not updating DNS records: this copy of the lab is out of date (see its Backups page)")
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			case <-wake:
			}
			continue
		}
		st := m.Sync(ctx)
		next := interval
		if len(st.Public) > 0 || len(st.NotDeleted) > 0 {
			// A home's public address can change (SPEC.md §6.11).
			next = min(interval, PublicInterval)
		}
		if slices.ContainsFunc(st.Public, func(r PublicRecord) bool { return r.State == PublicWaiting }) {
			next = 30 * time.Second // CrowdSec is starting
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next):
		case <-wake:
		}
	}
}

// PublicInterval is how often records are synced while anything is public.
const PublicInterval = 5 * time.Minute

// Sync makes sure the wildcard record points at the LAN IP, then checks
// what the home network sees. The old address's records follow.
func (m *Manager) Sync(ctx context.Context) Status {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.mu.Lock()
	lab := m.Lab
	m.syncing = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.syncing = false
		m.mu.Unlock()
	}()
	m.mu.Lock()
	m.notDeleted = nil
	m.mu.Unlock()
	st := m.syncCurrent(ctx, lab)
	m.mu.Lock()
	st.NotDeleted = m.notDeleted
	m.mu.Unlock()
	st.Old = m.syncOld(ctx, lab, st)
	st.Left = m.left(ctx, lab)
	return m.set(st)
}

// syncCurrent keeps the records of the lab's current address.
func (m *Manager) syncCurrent(ctx context.Context, lab config.Lab) Status {
	record := "*." + lab.Domain
	ip := lab.DNS.LANIP
	if ip == "" && lab.OnInternet() {
		// The public address visitors reach (which, behind a provider's
		// NAT, isn't the one on the server's interface).
		ip, _ = m.entryAddress(ctx)
	}
	if ip == "" {
		var err error
		if ip, err = m.lanIP(); err != nil {
			return Status{State: StateError, Record: record, Message: "Couldn't find this server's LAN address: " + err.Error()}
		}
	}
	st := Status{Record: record, IP: ip, Managing: lab.DNS.ManageRecords, AcceptedRisk: lab.DNS.AllowRiskyRecords,
		Internet: lab.OnInternet()}

	p := dns.Get(lab.DNS.Provider)
	if p != nil {
		st.Provider, st.CanManage, st.Risky = p.Title, p.CanManageRecords(), p.RiskyRecords()
	}
	if p == nil || lab.Domain == "" {
		st.State, st.Message = StateManual, "Set up your domain first."
		return st
	}
	ok, risky := p.ManagesRecords()
	switch {
	case !ok:
		st.State, st.Message = StateUnsupported, p.Title+" records can't be managed by Roostack yet."
		return st
	case !lab.DNS.ManageRecords, risky && !lab.DNS.AllowRiskyRecords:
		st.State = StateManual
		st = m.check(ctx, st)
		if lab.OnInternet() {
			st.Public, st.PublicIP = m.coveredByWildcard(ctx, st)
		} else {
			st.Public, st.PublicIP = m.publicManual(ctx)
		}
		if relay, ok := m.relayed(); ok {
			_, st.Proof = m.relayedManual(ctx, relay)
		}
		st = m.linkManual(ctx, st)
		return st
	}

	values := map[string]string{}
	for k, v := range lab.DNS.Values {
		values[k] = v
	}
	for _, f := range lab.DNS.Fields {
		v, err := m.Secrets.Read(ctx, dns.SecretName(f))
		if err != nil {
			st.State, st.Message = StateError, "Couldn't read the DNS credentials: "+err.Error()
			return st
		}
		values[f] = string(v)
	}
	zone := lab.DNS.Zone
	if zone == "" {
		var err error
		if zone, err = p.Check(ctx, values, lab.Domain); err != nil {
			st.State, st.Message = StateError, err.Error()
			return st
		}
	}
	name := "*"
	if rel := strings.TrimSuffix(lab.Domain, "."+zone); rel != lab.Domain {
		name += "." + rel
	} else if lab.Domain != zone {
		st.State, st.Message = StateError, fmt.Sprintf("%s isn't in the zone %s", lab.Domain, zone)
		return st
	}

	err := m.ensure(ctx, p, values, zone, name, ip)
	if err == nil && lab.UIHost() == lab.Domain {
		// Roostack's pages are at the name itself (a roost.ac name), which
		// the wildcard doesn't cover (SPEC.md §6.6).
		err = m.ensure(ctx, p, values, zone, strings.TrimPrefix(strings.TrimPrefix(name, "*"), "."), ip)
	}
	if err != nil {
		var conflict *conflictError
		if errors.As(err, &conflict) {
			st.State, st.Message, st.Current = StateConflict, conflict.Error(), conflict.current
		} else {
			st.State, st.Message = StateError, err.Error()
		}
		if !lab.OnInternet() {
			st.Public, st.PublicIP = m.syncPublic(ctx, p, values, zone, ip)
		}
		return st
	}
	st = m.check(ctx, st)
	if lab.OnInternet() {
		st.Public, st.PublicIP = m.coveredByWildcard(ctx, st)
	} else {
		st.Public, st.PublicIP = m.syncPublic(ctx, p, values, zone, ip)
	}
	st.Proof = m.syncProof(ctx, p, values, zone)
	st = m.linkManaged(ctx, st, p, values, zone)
	return st
}

type conflictError struct{ record, current, want string }

func (e *conflictError) Error() string {
	return fmt.Sprintf("%s already points at %s, and Roostack didn't create that record, so it won't change it. "+
		"Delete it at your DNS provider, or point it at %s.", e.record, e.current, e.want)
}

// ownerKey is where Roostack remembers a record it created and its value.
const ownerPrefix = "dns.owned."

func ownerKey(fqdn, typ string) string { return ownerPrefix + fqdn + "/" + typ }

// Record is a DNS record Roostack manages at the provider.
type Record struct {
	Name  string // e.g. *.lab.example.com
	Type  string // e.g. A
	Value string // what Roostack last set it to
}

// Records lists the records Roostack manages (the ones it created, adopted
// or took over), by name.
func (m *Manager) Records(ctx context.Context) ([]Record, error) {
	owned, err := m.Store.SettingsWithPrefix(ctx, ownerPrefix)
	if err != nil {
		return nil, err
	}
	var recs []Record
	for key, value := range owned {
		name, typ, ok := strings.Cut(strings.TrimPrefix(key, ownerPrefix), "/")
		if !ok || value == "" {
			continue // released (value cleared) or not a record
		}
		recs = append(recs, Record{Name: name, Type: typ, Value: value})
	}
	slices.SortFunc(recs, func(a, b Record) int { return strings.Compare(a.Name+a.Type, b.Name+b.Type) })
	return recs, nil
}

// ensure creates or updates the A record, but only if Roostack owns it (or
// it doesn't exist, or already has the right value).
func (m *Manager) ensure(ctx context.Context, p *dns.Provider, values map[string]string, zone, name, ip string) error {
	return m.ensureRecord(ctx, p, values, zone, name, "A", ip)
}

// ensureRecord is ensure for a record of any type.
func (m *Manager) ensureRecord(ctx context.Context, p *dns.Provider, values map[string]string, zone, name, typ, value string) error {
	fqdn := fqdnOf(zone, name)
	key := ownerKey(fqdn, typ)
	owned, err := m.Store.Setting(ctx, key)
	if err != nil {
		return err
	}
	current, err := p.GetRecord(ctx, m.HTTP, values, zone, name, typ)
	if err != nil {
		return fmt.Errorf("reading %s from %s: %w", fqdn, p.Title, err)
	}
	switch {
	case len(current) == 1 && current[0] == value:
		// Already right; adopt it if Roostack didn't create it.
	case len(current) == 0 || owned != "":
		if err := p.SetRecord(ctx, m.HTTP, values, zone, name, typ, value); err != nil {
			return fmt.Errorf("updating %s at %s: %w", fqdn, p.Title, err)
		}
		m.Log.Info("DNS record set", "record", fqdn, "type", typ, "value", value)
	default:
		return &conflictError{record: fqdn, current: strings.Join(current, ", "), want: value}
	}
	return m.Store.SetSetting(ctx, key, value)
}

// removeOwned deletes a record Roostack set, if it's still as Roostack
// left it. One changed by hand since (another value, or more values) is
// the Owner's now: left in place, and no longer Roostack's. None there,
// or none of Roostack's: nothing to do. Only a delete that failed keeps
// the record Roostack's, to try again.
func (m *Manager) removeOwned(ctx context.Context, p *dns.Provider, values map[string]string, zone, name, typ string) error {
	fqdn := fqdnOf(zone, name)
	key := ownerKey(fqdn, typ)
	owned, err := m.Store.Setting(ctx, key)
	if err != nil || owned == "" {
		return err
	}
	current, err := p.GetRecord(ctx, m.HTTP, values, zone, name, typ)
	if err != nil {
		return fmt.Errorf("reading %s from %s: %w", fqdn, p.Title, err)
	}
	switch {
	case len(current) == 0:
	case len(current) == 1 && dns.SameValue(typ, current[0], owned):
		if err := p.DeleteRecord(ctx, m.HTTP, values, zone, name, typ); err != nil {
			return fmt.Errorf("deleting %s from %s: %w", fqdn, p.Title, err)
		}
		m.Log.Info("DNS record deleted", "record", fqdn, "type", typ)
	default:
		m.Log.Warn("DNS record changed by hand: left in place, and Roostack no longer manages it",
			"record", fqdn, "type", typ, "set_to", owned, "now", strings.Join(current, ", "))
	}
	return m.Store.SetSetting(ctx, key, "")
}

// release lets go of an A record no longer wanted: deleted, or, where the
// provider can't delete (Roostack Cloud's), pointed home like the
// wildcard.
func (m *Manager) release(ctx context.Context, p *dns.Provider, values map[string]string, zone, name string, rec Record, home string) error {
	if p.CanDeleteRecords() {
		return m.removeOwned(ctx, p, values, zone, name, "A")
	}
	if rec.Value == home || home == "" {
		return nil
	}
	return m.ensure(ctx, p, values, zone, name, home)
}

// wantsA reports whether the current address wants an A record at host:
// the wildcard, the name itself where Roostack's pages are (a roost.ac
// name), a public address or link.<domain>.
func (m *Manager) wantsA(lab config.Lab, host string, publicHosts []string) bool {
	return host == "*."+lab.Domain || host == lab.Domain && lab.UIHost() == lab.Domain ||
		slices.Contains(publicHosts, host) || host == m.linkHost() && m.linkWanted()
}

// domainOf says which of domains a host is under ("" for none); where one
// domain is under another, the longer one.
func domainOf(host string, domains ...string) string {
	best := ""
	for _, d := range domains {
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) && len(d) > len(best) {
			best = d
		}
	}
	return best
}

// zoneName is host relative to zone, "" for the zone itself.
func zoneName(host, zone string) (string, bool) {
	if host == zone {
		return "", true
	}
	return relName(host, zone)
}

// fqdnOf joins a zone-relative name and the zone ("" is the zone itself).
func fqdnOf(zone, name string) string {
	if name == "" {
		return zone
	}
	return name + "." + zone
}

// check looks up a random name under the wildcard through a public
// resolver and through the home network's resolver.
func (m *Manager) check(ctx context.Context, st Status) Status {
	token := make([]byte, 4)
	rand.Read(token)
	host := "roostack-check-" + hex.EncodeToString(token) + "." + m.Lab.Domain

	deadline := time.Now().Add(m.Wait)
	public := false
	for {
		addrs, _ := m.PublicLookup(ctx, host)
		if slices.Contains(addrs, st.IP) {
			public = true
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		time.Sleep(3 * time.Second)
	}
	local, _ := m.LocalLookup(ctx, host)
	homeOK := slices.Contains(local, st.IP)
	st.Reachable = homeOK

	switch {
	case homeOK && st.Internet:
		if st.State == "" {
			st.State = StateOK
		}
		st.Message = "Your services' addresses point at this server."
	case homeOK:
		if st.State == "" {
			st.State = StateOK
		}
		st.Message = "Devices on your home network can reach your services."
	case st.State == StateManual:
		st.Message = fmt.Sprintf("Add this record at your DNS provider: %s  A  %s", st.Record, st.IP)
	case len(local) > 0:
		// Home DNS has its own answer for the domain: a local DNS server,
		// a DNS rewrite, or a router setting overrides public DNS.
		st.State = StateOverridden
		st.Message = fmt.Sprintf("Your home network's DNS answers %s with %s instead of this server (%s). "+
			"Something on your network overrides this domain, such as a local DNS server (Pi-hole, AdGuard) "+
			"or a router setting. Point %s at %s there, or remove the override.",
			st.Record, strings.Join(local, ", "), st.IP, st.Record, st.IP)
	case public && st.Internet:
		// Public DNS has it; this server's resolver doesn't yet (a cache).
		st.State = StatePending
		st.Message = "The record is in place; this server's DNS will see it shortly."
	case public:
		st.State = StateBlocked
		st.Message = "The record exists, but your home network's DNS won't return it. This is usually " +
			"the router's \"DNS rebinding protection\", which blocks answers that point at home addresses. " +
			"Allow " + m.Lab.Domain + " in the router's settings (often called rebind protection or " +
			"private domain exceptions), or use a local DNS server."
	default:
		st.State = StatePending
		st.Message = "The record isn't visible yet. DNS changes can take a few minutes; Roostack checks again automatically."
	}
	return st
}

func (m *Manager) lanIP() (string, error) {
	if m.LANIP != nil {
		return m.LANIP()
	}
	return DetectLANIP()
}

// DetectLANIP returns the address this server uses to reach the internet
// (its LAN address behind a home router). No packets are sent.
func DetectLANIP() (string, error) {
	c, err := net.Dial("udp4", "192.0.2.1:9") // TEST-NET-1: routing lookup only
	if err != nil {
		return "", err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

// SystemLookup resolves through this server's configured DNS (normally
// the home router).
func SystemLookup(ctx context.Context, host string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, host)
}

// PublicLookup resolves through Cloudflare's public resolver, bypassing the
// router, to tell "not published yet" apart from "blocked at home".
func PublicLookup(ctx context.Context, host string) ([]string, error) {
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", "1.1.1.1:53")
	}}
	return r.LookupHost(ctx, host)
}
