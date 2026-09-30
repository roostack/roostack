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
	Host    string // e.g. photos.lab.example.com
	IP      string // what it should point at: the entry's address
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
	addr, err := m.entryAddress(ctx)
	var recs []PublicRecord
	for _, h := range hosts {
		r := PublicRecord{Host: h, IP: addr, State: PublicManual,
			Message: fmt.Sprintf("Add this record at your DNS provider: %s  A  %s", h, addr)}
		if err != nil {
			r.State, r.Message = PublicError, err.Error()
		}
		recs = append(recs, r)
	}
	return recs, addr
}

// syncPublic points each public address at the entry's address, once
// protection is running. An address that's no longer public goes back to
// the LAN address, like the wildcard: providers' record APIs here can't
// delete yet (TASKS.md 1.18).
func (m *Manager) syncPublic(ctx context.Context, p *dns.Provider, values map[string]string, zone, lanIP string) ([]PublicRecord, string) {
	hosts := m.publicHosts()
	wildcard := "*." + m.Lab.Domain
	owned, err := m.Records(ctx)
	if err != nil {
		m.Log.Error("reading managed DNS records", "err", err)
	}
	for _, r := range owned {
		if r.Name == wildcard || r.Type != "A" || slices.Contains(hosts, r.Name) || r.Value == lanIP {
			continue
		}
		if name, ok := relName(r.Name, zone); ok {
			if err := m.ensure(ctx, p, values, zone, name, lanIP); err != nil {
				m.Log.Error("pointing a private address home again", "record", r.Name, "err", err)
			}
		}
	}
	if len(hosts) == 0 {
		return nil, ""
	}

	addr, err := m.entryAddress(ctx)
	protected := m.Protected == nil || m.Protected(ctx)
	var recs []PublicRecord
	for _, h := range hosts {
		r := PublicRecord{Host: h, IP: addr}
		name, inZone := relName(h, zone)
		switch {
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
		r := PublicRecord{Host: h, IP: st.IP}
		got, _ := m.PublicLookup(ctx, h)
		switch {
		case slices.Contains(got, st.IP):
			r.State, r.Message = PublicOK, fmt.Sprintf("%s points at this server (%s), through %s.", h, st.IP, st.Record)
		case st.State == StateManual:
			r.State, r.Message = PublicManual, fmt.Sprintf("Add this record at your DNS provider: %s  A  %s", st.Record, st.IP)
		case len(got) > 0:
			r.State, r.Message = PublicConflict, fmt.Sprintf("Public DNS answers %s with %s, not this server (%s). "+
				"A record for it at your DNS provider is in the way: delete it, so %s covers it.", h, strings.Join(got, ", "), st.IP, st.Record)
		default:
			r.State, r.Message = PublicPending, fmt.Sprintf("Waiting for %s to be visible in public DNS.", st.Record)
		}
		recs = append(recs, r)
	}
	if len(recs) == 0 {
		return nil, ""
	}
	return recs, st.IP
}

// relName is host relative to zone ("photos.lab" for photos.lab.example.com
// in example.com).
func relName(host, zone string) (string, bool) {
	rel, ok := strings.CutSuffix(host, "."+zone)
	return rel, ok && rel != ""
}
