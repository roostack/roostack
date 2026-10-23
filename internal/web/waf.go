package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/usererr"
)

// Blocking known exploits (SPEC.md §6.10 item 4): CrowdSec's AppSec
// component checks each outside visitor's request to a public service and
// refuses the ones that try a known exploit. The service's page shows what
// it refused this week; its public access page turns it off, for the
// Owner, after a warning.

// wafWeek is how far back the service page and the weekly summary look.
const wafWeek = 7 * 24 * time.Hour

// wafLatest is how many refused requests the service page lists.
const wafLatest = 3

// wafCacheFor keeps CrowdSec's answer for pages that poll (asking it runs
// cscli in its container).
const wafCacheFor = time.Minute

// wafCache is the last answer about refused requests.
type wafCache struct {
	mu     sync.Mutex
	at     time.Time
	blocks []public.Block
	ok     bool
}

// wafBlocks are the requests AppSec refused this week, newest first, and
// whether CrowdSec answered.
func (s *Server) wafBlocks(ctx context.Context) ([]public.Block, bool) {
	if s.Blocks == nil {
		return nil, false
	}
	s.waf.mu.Lock()
	defer s.waf.mu.Unlock()
	if !s.waf.at.IsZero() && time.Since(s.waf.at) < wafCacheFor {
		return s.waf.blocks, s.waf.ok
	}
	blocks, err := s.Blocks(ctx, wafWeek)
	if err != nil {
		s.Log.Warn("asking CrowdSec what it refused", "err", err)
	}
	s.waf.at, s.waf.blocks, s.waf.ok = time.Now(), blocks, err == nil
	return blocks, err == nil
}

// wafView is the service page's "Known exploits blocked" card, shown while
// one of its addresses is public.
type wafView struct {
	// On: AppSec checks its public addresses. Route is one of them, for
	// the link to its public access page (where it's turned on or off).
	On    bool
	Route string
	// Count requests were refused this week; Latest are the newest.
	// Unknown: CrowdSec didn't answer (it isn't running yet).
	Count   int
	Latest  []public.Block
	Unknown bool
}

// wafCard is the card for a service with these addresses, or nil when
// none is public.
func (s *Server) wafCard(ctx context.Context, access []manager.RouteAccess) *wafView {
	var hosts []string
	v := &wafView{}
	for _, ra := range access {
		if ra.Public {
			hosts = append(hosts, strings.ToLower(ra.Host))
			v.On = ra.WAF
			if v.Route == "" {
				v.Route = ra.Route
			}
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	blocks, ok := s.wafBlocks(ctx)
	v.Unknown = !ok
	for _, b := range blocks {
		if slices.Contains(hosts, b.Host) {
			v.Count++
			if len(v.Latest) < wafLatest {
				v.Latest = append(v.Latest, b)
			}
		}
	}
	return v
}

// wafSubmit turns blocking known exploits on or off for a service (all its
// public addresses), from one of its public access pages.
func (s *Server) wafSubmit(w http.ResponseWriter, r *http.Request) {
	ra, err := s.routeAccess(r)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	name := r.PathValue("name")
	on := r.FormValue("waf") == "on"
	if err := s.Manager.SetWAF(r.Context(), name, on); err != nil {
		if msg, ok := usererr.Message(err); ok {
			s.renderPublic(w, r, msg)
			return
		}
		s.serverError(w, err)
		return
	}
	detail := "known exploits blocked again for " + name
	if !on {
		detail = "blocking known exploits turned off for " + name
	}
	s.Store.Audit(r.Context(), currentUser(r), "public access", detail)
	redirect(w, r, "/services/"+name+"/public/"+ra.Route+"#waf")
}
