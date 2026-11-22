package web

import (
	"context"
	"net/http"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/public"
)

// What CrowdSec has stopped (SPEC.md §6.10 item 4): a card on Public
// access, loaded on its own (asking CrowdSec runs cscli in its container),
// so that the protection running while anything is public isn't invisible:
// the attacks it blocked, the requests AppSec refused, and the community's
// list it pulled.

// protectionPeriod is how far back the card looks.
const protectionPeriod = 30 * 24 * time.Hour

// protectionCacheFor keeps CrowdSec's answer: the page may be opened
// often, and each answer takes cscli a second or so.
const protectionCacheFor = 5 * time.Minute

// protectionView is the card.
type protectionView struct {
	// Unknown: CrowdSec didn't answer (it may be starting).
	Unknown bool
	// Attacks are its scenarios' alerts over protectionPeriod, and
	// Refused the requests AppSec refused then.
	Attacks public.Attacks
	Refused int
	// Community is how many addresses and ranges are on the community's
	// list it pulled, as of CommunityAt; Sharing says the Owner shares
	// attack reports (without, there's no list).
	Community   int
	CommunityAt time.Time
	Sharing     bool
}

// The card's figures, for the count function (int64).
func (v protectionView) BlockedN() int64   { return int64(v.Attacks.Blocked) }
func (v protectionView) AddressN() int64   { return int64(v.Attacks.Addresses) }
func (v protectionView) RefusedN() int64   { return int64(v.Refused) }
func (v protectionView) CommunityN() int64 { return int64(v.Community) }

// protectionCache is the last answer.
type protectionCache struct {
	mu sync.Mutex
	at time.Time
	v  protectionView
}

// protectionPart is the card.
func (s *Server) protectionPart(w http.ResponseWriter, r *http.Request) {
	s.renderPart(w, "settings-public", "protection", s.protection(r.Context()))
}

func (s *Server) protection(ctx context.Context) protectionView {
	s.protect.mu.Lock()
	defer s.protect.mu.Unlock()
	if !s.protect.at.IsZero() && time.Since(s.protect.at) < protectionCacheFor {
		return s.withCommunity(s.protect.v)
	}
	var v protectionView
	if s.Attacks == nil || s.Blocks == nil {
		v.Unknown = true
	} else {
		a, err := s.Attacks(ctx, protectionPeriod)
		if err != nil {
			s.Log.Warn("asking CrowdSec what it has stopped", "err", err)
			v.Unknown = true
		}
		blocks, err := s.Blocks(ctx, protectionPeriod)
		if err != nil {
			s.Log.Warn("asking CrowdSec what AppSec refused", "err", err)
			v.Unknown = true
		}
		v.Attacks, v.Refused = a, len(blocks)
	}
	if !v.Unknown {
		s.protect.at, s.protect.v = time.Now(), v
	}
	return s.withCommunity(v)
}

// withCommunity adds the community's list as the controller last read it
// (it reads it every quarter of an hour), and whether the Owner shares.
func (s *Server) withCommunity(v protectionView) protectionView {
	if s.Community != nil {
		v.Community, v.CommunityAt = s.Community()
	}
	asked, share := s.Manager.SignalSharing()
	v.Sharing = !asked || share
	return v
}
