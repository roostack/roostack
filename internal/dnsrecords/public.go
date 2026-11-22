package dnsrecords

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/dns"
)

// Public record states (SPEC.md §6.11).
const (
	PublicOK       = "ok"       // points at the entry's address, and public DNS shows it
	PublicPending  = "pending"  // set, but not visible yet
	PublicWaiting  = "waiting"  // waits for protection (CrowdSec) to start
	PublicManual   = "manual"   // Roostack doesn't manage records: add it by hand
	PublicConflict = "conflict" // a record Roostack didn't create is in the way
	PublicError    = "error"
)

// PublicRecord is a public address's record.
type PublicRecord struct {
	Host string // e.g. photos.lab.example.com
	// IP is what it should point at: the entry's address, or Type's value
	// (a CNAME's target, a TXT record's text) when Type isn't empty.
	IP      string
	Type    string // "" for A
	State   string
	Message string
}

func (m *Manager) publicHosts() []string {
	if m.PublicHosts == nil {
		return nil
	}
	hosts := slices.Clone(m.PublicHosts())
	slices.Sort(hosts)
	return hosts
}

// direct is Direct's answer, never nil.
func (m *Manager) direct() map[string]string {
	if m.Direct == nil {
		return map[string]string{}
	}
	if d := m.Direct(); d != nil {
		return d
	}
	return map[string]string{}
}

func (m *Manager) entryAddress(ctx context.Context) (string, error) {
	if m.Entry == nil {
		return "", errors.New("no way in for public visitors is set up")
	}
	return m.Entry.Address(ctx)
}

// publicManual says which records to add by hand, when Roostack doesn't
// manage them.
func (m *Manager) publicManual(ctx context.Context) ([]PublicRecord, string) {
	hosts := m.publicHosts()
	if len(hosts) == 0 {
		return nil, ""
	}
	if relay, ok := m.relayed(); ok {
		recs, _ := m.relayedManual(ctx, relay)
		return recs, relay.Name
	}
	addr, err := m.entryAddress(ctx)
	direct := m.direct()
	var recs []PublicRecord
	for _, h := range hosts {
		ip, own := direct[h]
		if !own {
			ip = addr
		}
		r := PublicRecord{Host: h, IP: ip, State: PublicManual,
			Message: fmt.Sprintf("Add this record at your DNS provider: %s  A  %s", h, ip)}
		if err != nil && !own {
			r.State, r.Message = PublicError, err.Error()
		}
		recs = append(recs, r)
	}
	return recs, addr
}

// syncPublic points each public address at the entry's address, once
// protection is running. An address that's no longer public loses its
// record, and the wildcard covers it again: only Roostack's own record,
// as Roostack left it (removeOwned). Roostack Cloud's records API can't
// delete, so there it points home like the wildcard. Records under the
// lab's old address are old.go's.
func (m *Manager) syncPublic(ctx context.Context, p *dns.Provider, values map[string]string, zone, lanIP string) ([]PublicRecord, string) {
	hosts := m.publicHosts()
	relay, relayed := m.relayed()
	old := m.oldDomain()
	current := func(r Record) bool { return domainOf(r.Name, m.Lab.Domain, old) == m.Lab.Domain }
	owned, err := m.Records(ctx)
	if err != nil {
		m.Log.Error("reading managed DNS records", "err", err)
	}
	// Roostack's CNAMEs to the relay, where they aren't wanted any more
	// (not relayed, or not public): gone, so A records (or the wildcard)
	// can take over.
	for _, r := range owned {
		if r.Type != "CNAME" || relayed && slices.Contains(hosts, r.Name) || !current(r) {
			continue
		}
		if name, ok := relName(r.Name, zone); ok {
			if err := m.removeOwned(ctx, p, values, zone, name, "CNAME"); err != nil {
				m.Log.Error("removing a CNAME to the relay", "record", r.Name, "err", err)
			}
		}
	}
	// A delete that failed is listed (NotDeleted), and tried again at the
	// next sync.
	for _, r := range owned {
		if r.Type != "A" || !current(r) || m.wantsA(m.Lab, r.Name, hosts) {
			continue
		}
		if name, ok := zoneName(r.Name, zone); ok {
			if err := m.release(ctx, p, values, zone, name, r, lanIP); err != nil {
				m.Log.Error("removing a private address's record", "record", r.Name, "err", err)
				m.mu.Lock()
				m.notDeleted = append(m.notDeleted, PublicRecord{Host: r.Name, IP: r.Value, State: PublicError,
					Message: fmt.Sprintf("Roostack couldn't delete its record, which still points at %s: %v. "+
						"It will try again in a few minutes.", r.Value, err)})
				m.mu.Unlock()
			}
		}
	}
	if len(hosts) == 0 {
		return nil, ""
	}

	addr, err := m.entryAddress(ctx)
	if relayed {
		addr, err = relay.Name, nil
	}
	protected := m.Protected == nil || m.Protected(ctx)
	direct := m.direct()
	var recs []PublicRecord
	for _, h := range hosts {
		r := PublicRecord{Host: h, IP: addr}
		if relayed {
			r.Type = "CNAME"
		}
		name, inZone := zoneName(h, zone)
		if ip, own := direct[h]; own {
			// Served by its own server, at that server's address.
			r.IP, r.Type = ip, ""
			switch {
			case !inZone:
				r.State, r.Message = PublicError, fmt.Sprintf("%s isn't in the zone %s", h, zone)
			case !protected:
				r.State, r.Message = PublicWaiting, "Waiting for CrowdSec, which protects public services, to start."
			default:
				r.State, r.Message = m.setPublic(ctx, p, values, zone, name, h, ip)
			}
			recs = append(recs, r)
			continue
		}
		switch {
		case relayed && inZone && name == "":
			// The domain itself at the top of its zone, which can't be a
			// CNAME (SPEC.md §5.3).
			r.State, r.Message = m.relayedState(ctx, h, relay.Name)
			if r.State != PublicOK {
				r.State, r.Message = PublicManual, fmt.Sprintf("DNS doesn't allow a CNAME at the top of a domain, so Roostack can't point %s at "+
					"Roostack Cloud's relay. If your DNS provider flattens CNAMEs there (Cloudflare does), add %s  CNAME  %s yourself; "+
					"otherwise make the domain itself home-only.", h, h, relay.Name)
			}
		case relayed && inZone && protected:
			r.State, r.Message = m.setRelayedHost(ctx, p, values, zone, name, h, relay.Name)
		case err != nil:
			r.State, r.Message = PublicError, err.Error()
		case !inZone:
			r.State, r.Message = PublicError, fmt.Sprintf("%s isn't in the zone %s", h, zone)
		case !protected:
			r.State, r.Message = PublicWaiting, "Waiting for CrowdSec, which protects public services, to start."
		default:
			r.State, r.Message = m.setPublic(ctx, p, values, zone, name, h, addr)
		}
		recs = append(recs, r)
	}
	return recs, addr
}

// setPublic sets one public record and looks it up in public DNS.
func (m *Manager) setPublic(ctx context.Context, p *dns.Provider, values map[string]string, zone, name, host, addr string) (string, string) {
	if err := m.ensure(ctx, p, values, zone, name, addr); err != nil {
		var conflict *conflictError
		if errors.As(err, &conflict) {
			return PublicConflict, conflict.Error()
		}
		return PublicError, err.Error()
	}
	if got, _ := m.PublicLookup(ctx, host); slices.Contains(got, addr) {
		return PublicOK, fmt.Sprintf("%s points at %s.", host, addr)
	}
	return PublicPending, "The record is set, but isn't visible yet. DNS changes can take a few minutes."
}

// coveredByWildcard is the public addresses' state on a lab on the
// internet: the wildcard, pointing at this server, covers them, so they
// need no records of their own (SPEC.md §6.12). They're fine once public
// DNS returns this server's address for them.
func (m *Manager) coveredByWildcard(ctx context.Context, st Status) ([]PublicRecord, string) {
	var recs []PublicRecord
	for _, h := range m.publicHosts() {
		recs = append(recs, m.covered(ctx, st, h))
	}
	if len(recs) == 0 {
		return nil, ""
	}
	return recs, st.IP
}

// setDirect sets the records of recs' addresses that servers serving
// directly serve themselves, on a lab on the internet, where the wildcard
// points at the controller (SPEC.md §10.5).
func (m *Manager) setDirect(ctx context.Context, p *dns.Provider, values map[string]string, zone string, recs []PublicRecord) {
	direct := m.direct()
	for i, r := range recs {
		ip, own := direct[r.Host]
		if !own {
			continue
		}
		recs[i].IP = ip
		if name, ok := zoneName(r.Host, zone); ok {
			recs[i].State, recs[i].Message = m.setPublic(ctx, p, values, zone, name, r.Host, ip)
		} else {
			recs[i].State, recs[i].Message = PublicError, fmt.Sprintf("%s isn't in the zone %s", r.Host, zone)
		}
	}
}

// covered is one host's state when the wildcard covers it, or, for one a
// server serving directly serves, its own record would.
func (m *Manager) covered(ctx context.Context, st Status, h string) PublicRecord {
	if ip, own := m.direct()[h]; own {
		r := PublicRecord{Host: h, IP: ip, State: PublicManual, Message: fmt.Sprintf("Add this record at your DNS provider: %s  A  %s", h, ip)}
		if got, _ := m.PublicLookup(ctx, h); slices.Contains(got, ip) {
			r.State, r.Message = PublicOK, fmt.Sprintf("%s points at %s.", h, ip)
		}
		return r
	}
	r := PublicRecord{Host: h, IP: st.IP}
	got, _ := m.PublicLookup(ctx, h)
	switch {
	case slices.Contains(got, st.IP):
		r.State, r.Message = PublicOK, fmt.Sprintf("%s points at this server (%s), through %s.", h, st.IP, st.Record)
	case st.State == StateManual:
		r.State, r.Message = PublicManual, fmt.Sprintf("Add this record at your DNS provider: %s  A  %s", st.Record, st.IP)
	case len(got) > 0:
		r.State, r.Message = PublicConflict, fmt.Sprintf("Public DNS answers %s with %s, not this server (%s). "+
			"A record for it at your DNS provider is in the way: delete it so that %s covers it.", h, strings.Join(got, ", "), st.IP, st.Record)
	default:
		r.State, r.Message = PublicPending, fmt.Sprintf("Waiting for %s to be visible in public DNS.", st.Record)
	}
	return r
}

// relName is host relative to zone ("photos.lab" for photos.lab.example.com
// in example.com).
func relName(host, zone string) (string, bool) {
	rel, ok := strings.CutSuffix(host, "."+zone)
	return rel, ok && rel != ""
}
