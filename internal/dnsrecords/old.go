package dnsrecords

import (
	"context"
	"fmt"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
)

// After the lab moves to another address, its old one keeps working for
// 30 days (SPEC.md §6.6). Meanwhile the records Roostack set under it
// follow the home's address, as the current address's do, with the old
// address's own DNS login (kept as the dns.OldSecretName secrets). When it
// ends, they're deleted with that login, before it goes: setup's hourly
// check calls RemoveOld, and keeps the old address (and its login) in
// lab.yaml until RemoveOld is done with it. Roostack creates nothing new
// under an old address.

// OldRecords is how the records Roostack set under the lab's old address
// stand.
type OldRecords struct {
	Domain   string
	Provider string // its DNS provider's title
	// Ended: the old address has stopped working, and these records are
	// being deleted.
	Ended bool
	// Records are Roostack's records still under it.
	Records []Record
	// Message is the latest problem, "" if none.
	Message string
}

// oldAddress is the lab's previous address, working or ended, while
// lab.yaml keeps it.
func (m *Manager) oldAddress() *config.OldAddress {
	if m.OldAddress != nil {
		return m.OldAddress()
	}
	return m.Lab.OldAddress
}

// oldDomain is the old address's domain, "" if there's none.
func (m *Manager) oldDomain() string {
	if o := m.oldAddress(); o != nil && o.Domain != m.Lab.Domain {
		return o.Domain
	}
	return ""
}

// recordsUnder lists Roostack's records under domain, and not under the
// other (where one domain is under the other, the longer one has them).
func (m *Manager) recordsUnder(ctx context.Context, domain, other string) ([]Record, error) {
	owned, err := m.Records(ctx)
	if err != nil {
		return nil, err
	}
	var recs []Record
	for _, r := range owned {
		if domainOf(r.Name, domain, other) == domain && domain != "" {
			recs = append(recs, r)
		}
	}
	return recs, nil
}

// left lists Roostack's records under neither the lab's address nor its
// old one: left behind by an address that has gone (SPEC.md §6.6).
func (m *Manager) left(ctx context.Context, lab config.Lab) []Record {
	owned, err := m.Records(ctx)
	if err != nil {
		m.Log.Error("reading managed DNS records", "err", err)
		return nil
	}
	old := m.oldDomain()
	var recs []Record
	for _, r := range owned {
		if domainOf(r.Name, lab.Domain, old) == "" {
			recs = append(recs, r)
		}
	}
	return recs
}

// managedAt reports whether Roostack manages records with a provider
// setting.
func managedAt(d config.DNS, p *dns.Provider) bool {
	ok, risky := p.ManagesRecords()
	return ok && d.ManageRecords && (!risky || d.AllowRiskyRecords)
}

// oldLogin reads the old address's DNS login and finds its zone.
func (m *Manager) oldLogin(ctx context.Context, old config.OldAddress, p *dns.Provider) (map[string]string, string, error) {
	values := map[string]string{}
	for k, v := range old.DNS.Values {
		values[k] = v
	}
	for _, f := range dns.CredentialFields(old.DNS) {
		v, err := m.Secrets.Read(ctx, dns.OldSecretName(f))
		if err != nil {
			return nil, "", fmt.Errorf("Roostack couldn't read %s's DNS login: %w", old.Domain, err)
		}
		values[f] = string(v)
	}
	zone := old.DNS.Zone
	if zone == "" {
		m.mu.Lock()
		zone = m.oldZones[old.Domain]
		m.mu.Unlock()
	}
	if zone == "" {
		var err error
		if zone, err = p.Check(ctx, values, old.Domain); err != nil {
			return nil, "", err
		}
		m.mu.Lock()
		if m.oldZones == nil {
			m.oldZones = map[string]string{}
		}
		m.oldZones[old.Domain] = zone
		m.mu.Unlock()
	}
	return values, zone, nil
}

// syncOld keeps the old address's records pointing at the right place
// while it works: the wildcard (and a roost.ac name itself) where the
// current wildcard points; a public address's record at the entry's
// address while the same service is public at the current address, and
// deleted once it isn't (as at the current address). CNAMEs to Roostack
// Cloud's relay and the proof's TXT record stay as they are, until the
// end. Once it has ended, RemoveOld has the records.
func (m *Manager) syncOld(ctx context.Context, lab config.Lab, st Status) *OldRecords {
	old := m.oldAddress()
	if old == nil || old.Domain == "" || old.Domain == lab.Domain {
		return nil
	}
	recs, err := m.recordsUnder(ctx, old.Domain, lab.Domain)
	if err != nil || len(recs) == 0 {
		return nil
	}
	o := &OldRecords{Domain: old.Domain, Ended: !time.Now().Before(old.Until), Records: recs}
	p := dns.Get(old.DNS.Provider)
	if p != nil {
		o.Provider = p.Title
	}
	if o.Ended {
		// RemoveOld's to delete: say what it last found.
		if prev := m.Status().Old; prev != nil && prev.Domain == o.Domain {
			o.Message = prev.Message
		}
		return o
	}
	if p == nil || !managedAt(old.DNS, p) {
		return nil // the Owner looks after the old address's records
	}
	values, zone, err := m.oldLogin(ctx, *old, p)
	if err != nil {
		o.Message = m.oldProblem(*old, p, "update", err)
		return o
	}
	hosts := m.publicHosts()
	_, relayed := m.relayed()
	protected := m.Protected == nil || m.Protected(ctx)
	oldUI := config.Lab{Domain: old.Domain, DNS: old.DNS}.UIHost()
	var entry string
	var entryErr error
	for _, r := range recs {
		name, inZone := zoneName(r.Name, zone)
		if r.Type != "A" || !inZone {
			continue
		}
		err = nil
		switch now := strings.TrimSuffix(r.Name, "."+old.Domain) + "." + lab.Domain; {
		case r.Name == "*."+old.Domain || r.Name == old.Domain:
			if st.IP != "" {
				err = m.ensure(ctx, p, values, zone, name, st.IP)
			}
		case r.Name == oldUI && m.wantsA(lab, lab.UIHost(), hosts), m.wantsA(lab, now, hosts):
			// Still public: at the entry's address, unless that's not
			// known now (relayed through Roostack Cloud, CrowdSec not up,
			// no answer), when it's left as it is.
			if relayed || !protected {
				continue
			}
			if entry == "" && entryErr == nil {
				entry, entryErr = m.entryAddress(ctx)
			}
			if entryErr == nil {
				err = m.ensure(ctx, p, values, zone, name, entry)
			}
		default:
			err = m.release(ctx, p, values, zone, name, r, st.IP)
		}
		if err != nil && o.Message == "" {
			o.Message = m.oldProblem(*old, p, "update", err)
		}
	}
	if o.Records, err = m.recordsUnder(ctx, old.Domain, lab.Domain); err != nil || len(o.Records) == 0 {
		return nil
	}
	return o
}

// oldProblem says plainly that the old address's records couldn't be
// updated or deleted, and logs it.
func (m *Manager) oldProblem(old config.OldAddress, p *dns.Provider, what string, err error) string {
	m.Log.Warn("DNS records under the lab's old address", "address", old.Domain, "action", what, "err", err)
	return fmt.Sprintf("Roostack couldn't %s its records under %s with that address's login at %s: %v",
		what, old.Domain, p.Title, err)
}

// RemoveOld deletes the records Roostack set under the lab's old address,
// once it has stopped working, with its login (SPEC.md §6.6). Only
// Roostack's own records go, and only as Roostack left them: one changed
// by hand since is the Owner's (removeOwned). done says nothing more needs
// the old login: it may go. Otherwise (the provider refused or couldn't
// be reached) Status().Old says why, and the caller tries again later. A
// roost.ac name's records go when Roostack Cloud releases the name; where
// the Owner had turned record management off, they're left in place.
func (m *Manager) RemoveOld(ctx context.Context, old config.OldAddress) (done bool) {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.mu.Lock()
	current := m.Lab.Domain
	m.mu.Unlock()
	o := &OldRecords{Domain: old.Domain, Ended: true}
	defer func() {
		if len(o.Records) == 0 {
			o = nil
		}
		m.mu.Lock()
		m.status.Old = o
		m.mu.Unlock()
	}()
	if old.Domain == "" || old.Domain == current {
		return true
	}
	recs, err := m.recordsUnder(ctx, old.Domain, current)
	if err != nil {
		m.Log.Error("reading managed DNS records", "err", err)
		return false
	}
	if len(recs) == 0 {
		return true
	}
	o.Records = recs
	p := dns.Get(old.DNS.Provider)
	if p != nil {
		o.Provider = p.Title
	}
	switch {
	case m.Paused != nil && m.Paused():
		o.Message = "This copy of the lab is out of date (another copy runs it now), so it leaves DNS records alone."
		return false
	case old.DNS.Provider == config.ProviderCloud:
		m.Log.Info("the old roost.ac name's records go when Roostack Cloud releases it", "address", old.Domain)
		o.Records = nil
		return m.forget(ctx, recs) == nil
	case p == nil || !managedAt(old.DNS, p):
		m.Log.Info("record management was off for the old address: its records are left in place", "address", old.Domain)
		o.Records = nil
		return m.forget(ctx, recs) == nil
	case !p.CanDeleteRecords():
		// Left for the Owner to delete by hand (Left).
		o.Records = nil
		return true
	}
	values, zone, err := m.oldLogin(ctx, old, p)
	if err != nil {
		o.Message = m.oldProblem(old, p, "delete", err)
		return false
	}
	var failed error
	for _, r := range recs {
		name, inZone := zoneName(r.Name, zone)
		if !inZone {
			continue // can't be in this login's zone: left for the Owner (Left)
		}
		if err := m.removeOwned(ctx, p, values, zone, name, r.Type); err != nil && failed == nil {
			failed = err
		}
	}
	if o.Records, err = m.recordsUnder(ctx, old.Domain, current); err != nil {
		return false
	}
	if failed != nil {
		o.Message = m.oldProblem(old, p, "delete", failed)
		return false
	}
	if len(o.Records) > 0 {
		o.Records = nil // outside the zone: Left
	} else {
		m.Log.Info("deleted the DNS records under the lab's old address", "address", old.Domain)
	}
	return true
}

// Forget stops Roostack treating as its own the records it set under an
// address the lab no longer uses, which it couldn't delete: the Owner
// deletes them by hand (or has). Records under the current address, and
// under an old one still working, are kept.
func (m *Manager) Forget(ctx context.Context) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.mu.Lock()
	lab := m.Lab
	m.mu.Unlock()
	working := ""
	if o := m.oldAddress(); o != nil && time.Now().Before(o.Until) {
		working = o.Domain
	}
	owned, err := m.Records(ctx)
	if err != nil {
		return err
	}
	var gone []Record
	for _, r := range owned {
		if domainOf(r.Name, lab.Domain, working) == "" {
			gone = append(gone, r)
		}
	}
	if err := m.forget(ctx, gone); err != nil {
		return err
	}
	m.mu.Lock()
	if m.status.Old != nil && m.status.Old.Ended {
		m.status.Old = nil
	}
	m.status.Left = nil
	m.mu.Unlock()
	return nil
}

// forget clears Roostack's ownership of records, leaving them in place.
func (m *Manager) forget(ctx context.Context, recs []Record) error {
	for _, r := range recs {
		if err := m.Store.SetSetting(ctx, ownerKey(r.Name, r.Type), ""); err != nil {
			return err
		}
		m.Log.Info("DNS record no longer managed by Roostack", "record", r.Name, "type", r.Type)
	}
	return nil
}
