package dnsrecords

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/dns"
)

// ChallengeLife is how long a DNS challenge's record stays: long enough
// for Let's Encrypt to look it up, after which Roostack removes it.
const ChallengeLife = 10 * time.Minute

// ChallengePrefix starts a DNS challenge's record name (RFC 8555 §8.4).
const ChallengePrefix = "_acme-challenge."

// SetChallenge sets the DNS challenge for host's certificate, its
// _acme-challenge TXT record, to value, for a server serving its private
// routes at home (SPEC.md §10.5, "Serving directly"), and removes it after
// ChallengeLife. It's Roostack's own record, so one somebody else made is
// never overwritten.
func (m *Manager) SetChallenge(ctx context.Context, host, value string) error {
	m.mu.Lock()
	lab := m.Lab
	m.mu.Unlock()
	p := dns.Get(lab.DNS.Provider)
	if p == nil || !lab.DNS.ManageRecords {
		return errors.New("this domain's DNS records aren't managed by Roostack")
	}
	if ok, risky := p.ManagesRecords(); !ok || risky && !lab.DNS.AllowRiskyRecords {
		return errors.New("this domain's DNS records aren't managed by Roostack")
	}
	if !strings.HasSuffix(host, "."+lab.Domain) || strings.ContainsAny(value, "\"\\\n") || value == "" {
		return fmt.Errorf("not a challenge for this lab: %q", host)
	}
	values, zone, err := m.login(ctx, lab, p)
	if err != nil {
		return err
	}
	name, ok := relName(ChallengePrefix+host, zone)
	if !ok {
		return fmt.Errorf("%s isn't in the zone %s", host, zone)
	}
	if err := m.ensureRecord(ctx, p, values, zone, name, "TXT", value); err != nil {
		return err
	}
	time.AfterFunc(ChallengeLife, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := m.removeOwned(ctx, p, values, zone, name, "TXT"); err != nil {
			m.Log.Warn("removing a DNS challenge's record", "record", ChallengePrefix+host, "err", err)
		}
	})
	return nil
}
