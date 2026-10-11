package dnsrecords

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/dns"
)

// Relayed is how the lab's own domain is relayed through Roostack Cloud
// (Cloud's DESIGN §5, "Custom domains through the relay"; SPEC.md §10.5):
// public addresses are CNAMEs to Name, the lab's relay name, which Cloud
// keeps pointing at its edges; ProofName's TXT record, ProofValue, proves
// the domain is the lab's.
type Relayed struct {
	Name, ProofName, ProofValue string
}

func (m *Manager) relayed() (Relayed, bool) {
	if m.Relay == nil {
		return Relayed{}, false
	}
	r, ok := m.Relay()
	return r, ok && r.Name != ""
}

// setRelayedHost points one public address at the relay name: Roostack's
// own A record goes first (a name with a CNAME can have no other record);
// someone else's is in the way.
func (m *Manager) setRelayedHost(ctx context.Context, p *dns.Provider, values map[string]string, zone, name, host, target string) (string, string) {
	if err := m.removeOwned(ctx, p, values, zone, name, "A"); err != nil {
		return PublicError, err.Error()
	}
	if current, err := p.GetRecord(ctx, m.HTTP, values, zone, name, "A"); err != nil {
		return PublicError, fmt.Sprintf("reading %s from %s: %v", host, p.Title, err)
	} else if len(current) > 0 {
		return PublicConflict, fmt.Sprintf("%s has an A record (%s) that Roostack didn't create, so it can't point it at "+
			"Roostack Cloud's relay. Delete it at your DNS provider.", host, strings.Join(current, ", "))
	}
	if err := m.ensureRecord(ctx, p, values, zone, name, "CNAME", target); err != nil {
		var conflict *conflictError
		if errors.As(err, &conflict) {
			return PublicConflict, conflict.Error()
		}
		return PublicError, err.Error()
	}
	return m.relayedState(ctx, host, target)
}

// relayedState says whether public DNS takes host to the relay's edges.
func (m *Manager) relayedState(ctx context.Context, host, target string) (string, string) {
	got, _ := m.PublicLookup(ctx, host)
	want, _ := m.PublicLookup(ctx, target)
	for _, a := range got {
		if slices.Contains(want, a) {
			return PublicOK, fmt.Sprintf("%s points at Roostack Cloud's relay (%s).", host, target)
		}
	}
	return PublicPending, "The record is set, but isn't visible yet. DNS changes can take a few minutes."
}

// syncProof keeps the TXT record proving the domain in place, while it's
// relayed; otherwise removes Roostack's.
func (m *Manager) syncProof(ctx context.Context, p *dns.Provider, values map[string]string, zone string) *PublicRecord {
	relay, ok := m.relayed()
	owned, _ := m.Records(ctx)
	for _, r := range owned {
		if r.Type != "TXT" || !strings.HasPrefix(r.Name, "_roostack.") || ok && r.Name == relay.ProofName {
			continue
		}
		if name, in := relName(r.Name, zone); in {
			if err := m.removeOwned(ctx, p, values, zone, name, "TXT"); err != nil {
				m.Log.Error("removing an old proof", "record", r.Name, "err", err)
			}
		}
	}
	if !ok || relay.ProofName == "" {
		return nil
	}
	rec := &PublicRecord{Host: relay.ProofName, Type: "TXT", IP: relay.ProofValue}
	name, in := relName(relay.ProofName, zone)
	if !in {
		rec.State, rec.Message = PublicError, fmt.Sprintf("%s isn't in the zone %s", relay.ProofName, zone)
		return rec
	}
	if err := m.ensureRecord(ctx, p, values, zone, name, "TXT", relay.ProofValue); err != nil {
		var conflict *conflictError
		rec.State, rec.Message = PublicError, err.Error()
		if errors.As(err, &conflict) {
			rec.State = PublicConflict
		}
		return rec
	}
	rec.State, rec.Message = PublicOK, "The record proving "+strings.TrimPrefix(relay.ProofName, "_roostack.")+" is this lab's is in place."
	if m.ProofSet != nil {
		m.ProofSet()
	}
	return rec
}

// relayedManual says which records to add by hand while the domain is
// relayed, when Roostack doesn't manage them.
func (m *Manager) relayedManual(ctx context.Context, relay Relayed) ([]PublicRecord, *PublicRecord) {
	var recs []PublicRecord
	for _, h := range m.publicHosts() {
		r := PublicRecord{Host: h, Type: "CNAME", IP: relay.Name}
		r.State, r.Message = m.relayedState(ctx, h, relay.Name)
		if r.State != PublicOK {
			r.State, r.Message = PublicManual, fmt.Sprintf("Add this record at your DNS provider: %s  CNAME  %s", h, relay.Name)
		}
		recs = append(recs, r)
	}
	var proof *PublicRecord
	if relay.ProofName != "" {
		proof = &PublicRecord{Host: relay.ProofName, Type: "TXT", IP: relay.ProofValue, State: PublicManual,
			Message: fmt.Sprintf("Add this record at your DNS provider: %s  TXT  %s", relay.ProofName, relay.ProofValue)}
	}
	return recs, proof
}
