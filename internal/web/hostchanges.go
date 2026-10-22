package web

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/osupdate"
)

// Changes to a server that Roostack didn't make (SPEC.md §3.2): shown on its
// Updates page and in "needs attention", until the Owner says they made
// them on purpose.

// hostCheckFor is how long a server's check is reused (the dashboard
// refreshes every few seconds; the check runs apt and nft).
const hostCheckFor = 5 * time.Minute

type hostCheckCache struct {
	mu   sync.Mutex
	byID map[string]cachedHostCheck
}

type cachedHostCheck struct {
	at    time.Time
	check osupdate.HostCheck
	ok    bool
}

// hostChanges is a server's check, from the cache if it's recent.
func (s *Server) hostChanges(ctx context.Context, m serverRef, n ServiceNode) (osupdate.HostCheck, bool) {
	u := n.Updates()
	if u == nil {
		return osupdate.HostCheck{}, false
	}
	s.hostChecks.mu.Lock()
	c, hit := s.hostChecks.byID[m.ID]
	s.hostChecks.mu.Unlock()
	if hit && time.Since(c.at) < hostCheckFor {
		return c.check, c.ok
	}
	check, err := u.HostChanges(ctx)
	c = cachedHostCheck{at: time.Now(), check: check, ok: err == nil}
	s.hostChecks.mu.Lock()
	if s.hostChecks.byID == nil {
		s.hostChecks.byID = map[string]cachedHostCheck{}
	}
	s.hostChecks.byID[m.ID] = c
	s.hostChecks.mu.Unlock()
	return c.check, c.ok
}

// forgetHostCheck makes the next look check again.
func (s *Server) forgetHostCheck(id string) {
	s.hostChecks.mu.Lock()
	delete(s.hostChecks.byID, id)
	s.hostChecks.mu.Unlock()
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

func (s *Server) hostChangesKnownSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil || n.Updates() == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	if err := n.Updates().AcknowledgeHostChanges(r.Context()); err != nil {
		s.serverError(w, err)
		return
	}
	s.forgetHostCheck(m.ID)
	s.Store.Audit(r.Context(), currentUser(r), "host", "marked host changes on "+m.InSentence()+" as known")
	redirect(w, r, m.Updates())
}
