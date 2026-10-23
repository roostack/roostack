package attention

import (
	"context"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/osupdate"
)

// Changes to a server that Roostack didn't make (SPEC.md §3.2): in "needs
// attention" and on the server's Updates page, until the Owner says they
// made them on purpose.

// hostCheckFor is how long a server's check is reused (the dashboard
// refreshes every few seconds; the check runs apt and nft).
const hostCheckFor = 5 * time.Minute

// HostChecks keeps each server's latest check for a while. Its zero value
// is ready to use; a nil one keeps nothing.
type HostChecks struct {
	mu   sync.Mutex
	byID map[string]cachedHostCheck
}

type cachedHostCheck struct {
	at    time.Time
	check osupdate.HostCheck
	ok    bool
}

// Get is server id's check (u is its updates; nil: it has none), from
// what's kept if it's recent.
func (c *HostChecks) Get(ctx context.Context, id string, u node.Updates) (osupdate.HostCheck, bool) {
	if u == nil {
		return osupdate.HostCheck{}, false
	}
	if c == nil {
		check, err := u.HostChanges(ctx)
		return check, err == nil
	}
	c.mu.Lock()
	kept, hit := c.byID[id]
	c.mu.Unlock()
	if hit && time.Since(kept.at) < hostCheckFor {
		return kept.check, kept.ok
	}
	check, err := u.HostChanges(ctx)
	kept = cachedHostCheck{at: time.Now(), check: check, ok: err == nil}
	c.mu.Lock()
	if c.byID == nil {
		c.byID = map[string]cachedHostCheck{}
	}
	c.byID[id] = kept
	c.mu.Unlock()
	return kept.check, kept.ok
}

// Forget makes the next look at server id check again.
func (c *HostChecks) Forget(id string) {
	c.mu.Lock()
	delete(c.byID, id)
	c.mu.Unlock()
}

// hostChangesText sums the changes up for "needs attention".
func hostChangesText(c osupdate.HostCheck) string {
	var parts []string
	if len(c.AptSources) > 0 {
		parts = append(parts, "software from outside Debian")
	}
	if len(c.Held) > 0 {
		parts = append(parts, "packages held back from updates")
	}
	if len(c.Firewalls) > 0 {
		parts = append(parts, "another firewall")
	}
	if c.FirewallMissing {
		parts = append(parts, "Roostack's firewall isn't loaded")
	}
	return strings.Join(parts, ", ")
}
