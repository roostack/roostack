package public

import (
	"crypto/rand"
	"encoding/hex"
	"net/netip"
	"sync"
	"time"
)

// CheckPath is where, on a public address, Caddy passes the outside check
// to Roostack (SPEC.md §6.11). The Owner opens
// https://<address>/.well-known/roostack-check/<token> on a phone using
// mobile data: arriving from a public address shows the way in works.
const CheckPath = "/.well-known/roostack-check/"

// checkLife is how long a check link works.
const checkLife = time.Hour

// Checks hands out check links and records the visits. Links live in
// memory: a restart means making a new one.
type Checks struct {
	// Record saves a successful check (the host, when, and from where).
	Record func(host string, at time.Time, from string) error

	mu     sync.Mutex
	tokens map[string]pending
}

type pending struct {
	host    string
	expires time.Time
}

// New returns a check link token for host.
func (c *Checks) New(host string) string {
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tokens == nil {
		c.tokens = map[string]pending{}
	}
	now := time.Now()
	for t, p := range c.tokens {
		if now.After(p.expires) {
			delete(c.tokens, t)
		}
	}
	c.tokens[token] = pending{host: host, expires: now.Add(checkLife)}
	return token
}

// Visit results.
const (
	VisitUnknown  = iota // no such link, or it expired
	VisitFromHome        // the link works, but the visitor is at home
	VisitOutside         // it worked from outside: recorded
)

// Visit records a visit to a check link by remote, the visitor's address
// as Caddy saw it, on host. A visit from home (Wi-Fi still on) leaves the
// link working, so the Owner can try again on mobile data.
func (c *Checks) Visit(token, host, remote string) (int, error) {
	c.mu.Lock()
	p, ok := c.tokens[token]
	if !ok || time.Now().After(p.expires) || p.host != host {
		c.mu.Unlock()
		return VisitUnknown, nil
	}
	a, err := netip.ParseAddr(remote)
	if err != nil || !IsPublic(a) {
		c.mu.Unlock()
		return VisitFromHome, nil
	}
	delete(c.tokens, token)
	c.mu.Unlock()
	if c.Record != nil {
		if err := c.Record(host, time.Now(), a.String()); err != nil {
			return VisitOutside, err
		}
	}
	return VisitOutside, nil
}

// Current returns host's check link token that still works, or "".
func (c *Checks) Current(host string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var best string
	var latest time.Time
	for t, p := range c.tokens {
		if p.host == host && time.Now().Before(p.expires) && p.expires.After(latest) {
			best, latest = t, p.expires
		}
	}
	return best
}
