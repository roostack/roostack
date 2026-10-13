package dnsrecords

import (
	"context"
	"fmt"
	"slices"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/dns"
)

// link.<domain> is the name other servers and labs reach this one at over
// WireGuard (joining, storage for others; SPEC.md §10.2). It follows the
// home's public address, which can change, where a typed address can't.
// It's made only once someone chooses it, so a lab that never needs it
// doesn't publish its home's address. Unlike public web addresses it
// doesn't wait for CrowdSec: it isn't a website. On a lab on the internet
// the wildcard covers it.

func (m *Manager) linkHost() string { return caddy.LinkSubdomain + "." + m.Lab.Domain }

func (m *Manager) linkWanted() bool { return m.LinkWanted != nil && m.LinkWanted() }

// linkManaged keeps link.<domain> pointing at the entry's address, while
// Roostack manages records.
func (m *Manager) linkManaged(ctx context.Context, st Status, p *dns.Provider, values map[string]string, zone string) Status {
	if !m.linkWanted() {
		return st
	}
	h := m.linkHost()
	if st.Internet {
		r := m.covered(ctx, st, h)
		st.Link = &r
		return st
	}
	addr, err := m.entryAddress(ctx)
	r := PublicRecord{Host: h, IP: addr}
	name, inZone := relName(h, zone)
	switch {
	case err != nil:
		r.State, r.Message = PublicError, err.Error()
	case !inZone:
		r.State, r.Message = PublicError, fmt.Sprintf("%s isn't in the zone %s", h, zone)
	default:
		r.State, r.Message = m.setPublic(ctx, p, values, zone, name, h, addr)
		if st.PublicIP == "" {
			st.PublicIP = addr
		}
	}
	st.Link = &r
	return st
}

// linkManual says to add link.<domain> by hand, when Roostack doesn't
// manage records.
func (m *Manager) linkManual(ctx context.Context, st Status) Status {
	if !m.linkWanted() {
		return st
	}
	h := m.linkHost()
	if st.Internet {
		r := m.covered(ctx, st, h)
		st.Link = &r
		return st
	}
	addr, err := m.entryAddress(ctx)
	r := PublicRecord{Host: h, IP: addr, State: PublicManual,
		Message: fmt.Sprintf("Add this record at your DNS provider: %s  A  %s", h, addr)}
	if err != nil {
		r.State, r.Message = PublicError, err.Error()
	} else if got, _ := m.PublicLookup(ctx, h); slices.Contains(got, addr) {
		r.State, r.Message = PublicOK, fmt.Sprintf("%s points at %s.", h, addr)
	}
	if st.PublicIP == "" && err == nil {
		st.PublicIP = addr
	}
	st.Link = &r
	return st
}
