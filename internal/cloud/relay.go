package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
)

// WireGuardAgent is roostack-agent's WireGuard, for the relay's interface
// (its private key stays with the agent).
type WireGuardAgent interface {
	ApplyWireGuard(ctx context.Context, wg agent.WireGuard) (string, error)
	RemoveWireGuard(ctx context.Context, iface string) error
}

// relayState is the relay as Cloud last answered, kept in link.json.
type relayState struct {
	// Hosts are the names asked to be relayed, Countries who the edges let
	// in, and Relay Cloud's answer.
	Hosts     []string                `json:"hosts"`
	Countries cloudapi.RelayCountries `json:"countries,omitzero"`
	TimeZone  string                  `json:"time_zone,omitempty"`
	// Domain is the lab's own domain whose names are relayed ("": none).
	Domain string         `json:"domain,omitempty"`
	Relay  cloudapi.Relay `json:"relay"`
}

// same says whether a relay asked for is the one there is.
// A domain not proven yet is never the same: asking again looks for its
// proof.
func (st *relayState) same(hosts []string, c cloudapi.RelayCountries, tz, domain string) bool {
	return st != nil && slices.Equal(st.Hosts, hosts) && slices.Equal(st.Countries.Allow, c.Allow) &&
		slices.Equal(st.Countries.Block, c.Block) && st.Countries.OpenUntil.Equal(c.OpenUntil) && st.TimeZone == tz &&
		st.Domain == domain && (domain == "" || st.Relay.Domain != nil && st.Relay.Domain.Verified && !st.Relay.Domain.Missing)
}

// placeholderAddr is the relay interface's address before Cloud has given
// it one: the interface exists only to have its key made (no peers).
const placeholderAddr = "100.81.255.254/32"

// Relay has Roostack Cloud's relay bring visitors for hosts to the lab
// (Roostack Cloud's DESIGN §5, SPEC.md §10.5): the agent makes the relay's
// WireGuard interface (and its key), Cloud is told the key and the names
// and answers with the edges, then the interface dials every edge. Asking
// again with the same names and countries changes nothing; with others,
// Cloud is told. Countries are who the edges let in (Cloud checks them),
// tz the lab's time zone (its statistics' days), and domain the lab's own
// domain whose names in hosts are relayed once proven ("": none; Cloud's
// DESIGN §5, "Custom domains through the relay").
func (s *Service) Relay(ctx context.Context, hosts []string, countries config.Countries, tz, domain string) error {
	if s.WireGuard == nil {
		return errors.New("nothing here can set up the relay's network")
	}
	hosts = slices.Sorted(slices.Values(hosts))
	c := cloudapi.RelayCountries{Allow: countries.Allow, Block: countries.Block, OpenUntil: countries.OpenUntil}
	s.mu.Lock()
	linked, cur := s.st.Linked, s.st.Relay
	s.mu.Unlock()
	if !linked {
		return errors.New("the lab isn't linked to Roostack Cloud")
	}
	if cur.same(hosts, c, tz, domain) {
		return nil
	}
	addr := placeholderAddr
	if cur != nil {
		addr = fmt.Sprintf("%s/%d", cur.Relay.Addr, cur.Relay.Prefix)
	}
	pub, err := s.WireGuard.ApplyWireGuard(ctx, s.relayInterface(addr, cur))
	if err != nil {
		return fmt.Errorf("setting up the relay's network: %w", err)
	}
	var relay cloudapi.Relay
	if err := s.client.do(ctx, true, http.MethodPut, cloudapi.PathLabRelay, cloudapi.RelayRequest{PublicKey: pub, Hosts: hosts, Countries: c, TimeZone: tz, Domain: domain}, &relay); err != nil {
		return err
	}
	a, err := netip.ParseAddr(relay.Addr)
	if err != nil || !agent.RelayRange.Contains(a) || len(relay.Edges) == 0 {
		return errors.New("Roostack Cloud's answer had no relay address or edges")
	}
	st := &relayState{Hosts: hosts, Countries: c, TimeZone: tz, Domain: domain, Relay: relay}
	if _, err := s.WireGuard.ApplyWireGuard(ctx, s.relayInterface(fmt.Sprintf("%s/%d", relay.Addr, relay.Prefix), st)); err != nil {
		return fmt.Errorf("connecting to Roostack Cloud's edges: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Relay = st
	return s.save()
}

// relayInterface is the relay's WireGuard: an address on the relay
// network, and every edge, each allowed to send from its own address only,
// dialled with a keepalive (the lab is behind NAT; the edges aren't).
func (s *Service) relayInterface(addr string, st *relayState) agent.WireGuard {
	wg := agent.WireGuard{Interface: agent.RelayInterface, Address: addr}
	if st != nil {
		for _, e := range st.Relay.Edges {
			wg.Peers = append(wg.Peers, agent.Peer{PublicKey: e.PublicKey, AllowedIPs: []string{e.Addr + "/32"},
				Endpoint: e.Endpoint, Keepalive: 25})
		}
	}
	return wg
}

// StopRelay stops the relay: Cloud gives the names their own records
// back, and the interface goes. Cloud being unreachable doesn't stop the
// lab dropping its side.
func (s *Service) StopRelay(ctx context.Context) error {
	var err error
	if s.Status().Linked {
		err = s.client.do(ctx, true, http.MethodDelete, cloudapi.PathLabRelay, nil, nil)
	}
	if s.WireGuard != nil {
		if rerr := s.WireGuard.RemoveWireGuard(ctx, agent.RelayInterface); rerr != nil {
			err = errors.Join(err, rerr)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Relay = nil
	if serr := s.save(); serr != nil {
		err = errors.Join(err, serr)
	}
	return err
}

// SendBans sends the lab's CrowdSec bans to Cloud every so often while
// the lab is relayed, until ctx ends, so the edges refuse those visitors
// for this lab (Roostack Cloud's DESIGN §5, "Collective defence"). Every
// time, not only on a change: Cloud drops bans an hour after the last
// word. bans fails while CrowdSec isn't running; nothing is sent then.
func (s *Service) SendBans(ctx context.Context, every time.Duration, bans func(context.Context) ([]string, error)) {
	for {
		s.mu.Lock()
		relayed := s.st.Linked && s.st.Relay != nil
		s.mu.Unlock()
		if relayed {
			if err := s.sendBans(ctx, bans); err != nil {
				s.log.Warn("Roostack Cloud: sending CrowdSec's bans to the relay", "err", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (s *Service) sendBans(ctx context.Context, bans func(context.Context) ([]string, error)) error {
	list, err := bans(ctx)
	if err != nil {
		return err
	}
	if len(list) > cloudapi.MaxRelayBans {
		list = list[:cloudapi.MaxRelayBans]
	}
	return s.client.do(ctx, true, http.MethodPut, cloudapi.PathLabRelayBans, cloudapi.RelayBans{Bans: list}, nil)
}

// RelayStats are the lab's statistics through the relay for the last days
// (by the hour, or by the day in its time zone). Not relayed: none.
func (s *Service) RelayStats(ctx context.Context, days int, byDay bool) (cloudapi.RelayStats, error) {
	var st cloudapi.RelayStats
	s.mu.Lock()
	relayed := s.st.Linked && s.st.Relay != nil
	s.mu.Unlock()
	if !relayed {
		return st, nil
	}
	path := fmt.Sprintf("%s?days=%d", cloudapi.PathLabRelayStats, days)
	if byDay {
		path += "&by=day"
	}
	err := s.client.do(ctx, true, http.MethodGet, path, nil, &st)
	return st, err
}

// RelayName is the lab's relay name (its own domain's names point at it
// with a CNAME), and RelayDomain how that domain's proof stands (nil:
// none), as Cloud last said.
func (s *Service) RelayName() (string, *cloudapi.RelayDomain) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Relay == nil {
		return "", nil
	}
	d := s.st.Relay.Relay.Domain
	if d != nil {
		c := *d
		d = &c
	}
	return s.st.Relay.Relay.Name, d
}

// RefreshRelay asks Cloud how the relay stands (its own domain's proof,
// the edges), keeping its answer, every so often until ctx ends.
func (s *Service) RefreshRelay(ctx context.Context, every time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		s.mu.Lock()
		relayed := s.st.Linked && s.st.Relay != nil
		s.mu.Unlock()
		if !relayed {
			continue
		}
		var relay cloudapi.Relay
		if err := s.client.do(ctx, true, http.MethodGet, cloudapi.PathLabRelay, nil, &relay); err != nil {
			s.log.Warn("Roostack Cloud: asking how the relay stands", "err", err)
			continue
		}
		s.mu.Lock()
		if s.st.Relay != nil && relay.Addr == s.st.Relay.Relay.Addr {
			s.st.Relay.Relay.Domain, s.st.Relay.Relay.Name = relay.Domain, relay.Name
			if err := s.save(); err != nil {
				s.log.Warn("Roostack Cloud: saving how the relay stands", "err", err)
			}
		}
		s.mu.Unlock()
	}
}

// RelayEdges are the relay's edges as Cloud last said (nil when not
// relayed): where public names point, and the addresses the lab's Caddy
// believes PROXY headers from.
func (s *Service) RelayEdges() []cloudapi.RelayEdge {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Relay == nil {
		return nil
	}
	return slices.Clone(s.st.Relay.Relay.Edges)
}
