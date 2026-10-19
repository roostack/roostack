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
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
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
	s.st.Relay, s.st.Names = st, relay.Names
	err = s.save()
	s.mu.Unlock()
	if cur != nil && !slices.Equal(cur.Relay.Edges, relay.Edges) {
		s.edgesChanged()
	}
	return err
}

// edgesChanged says the relay's edges changed, and has the lab apply
// them (EdgesChanged), in the background: the apply asks for the relay
// again, which waits for relayMu.
func (s *Service) edgesChanged() {
	s.log.Info("Roostack Cloud: the relay's edges changed; the lab now dials the new ones")
	if s.EdgesChanged != nil {
		go s.EdgesChanged()
	}
}

// TurnRelayOn turns the relay on again after the lab's account turned it
// off at Cloud (Relay.TurnedOff): the same names and countries as last
// asked, with TurnOn. Only the Owner asks this: the lab's own updates
// leave it off (Roostack Cloud's DESIGN §3f).
func (s *Service) TurnRelayOn(ctx context.Context) error {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	s.mu.Lock()
	cur := s.st.Relay
	s.mu.Unlock()
	if cur == nil || s.WireGuard == nil {
		return errors.New("public visitors don't come in through Roostack Cloud's relay")
	}
	// The interface as it is: this asks for its key.
	pub, err := s.WireGuard.ApplyWireGuard(ctx, s.relayInterface(fmt.Sprintf("%s/%d", cur.Relay.Addr, cur.Relay.Prefix), cur))
	if err != nil {
		return fmt.Errorf("setting up the relay's network: %w", err)
	}
	req := cloudapi.RelayRequest{PublicKey: pub, Hosts: cur.Hosts, Countries: cur.Countries, TimeZone: cur.TimeZone,
		Domain: cur.Domain, TurnOn: true}
	var relay cloudapi.Relay
	if err := s.client.do(ctx, true, http.MethodPut, cloudapi.PathLabRelay, req, &relay); err != nil {
		return err
	}
	if !relay.TurnedOff.IsZero() {
		return errors.New("Roostack Cloud kept the relay off")
	}
	if relay.Addr != cur.Relay.Addr {
		return errors.New("the relay changed while it was being turned on: try again")
	}
	// Edges changed meanwhile (one rebuilt) are dialled before they're
	// kept; otherwise the next refresh would see nothing to change.
	changed := len(relay.Edges) > 0 && !slices.Equal(cur.Relay.Edges, relay.Edges)
	if !changed {
		relay.Edges = cur.Relay.Edges
	} else {
		next := *cur
		next.Relay.Edges = relay.Edges
		if _, err := s.WireGuard.ApplyWireGuard(ctx, s.relayInterface(fmt.Sprintf("%s/%d", relay.Addr, relay.Prefix), &next)); err != nil {
			return fmt.Errorf("connecting to Roostack Cloud's changed edges: %w", err)
		}
	}
	s.mu.Lock()
	if s.st.Relay == nil {
		s.mu.Unlock()
		return errors.New("the relay changed while it was being turned on: try again")
	}
	s.st.Relay.Relay, s.st.Names = relay, relay.Names
	s.log.Info("Roostack Cloud: the relay was turned on again")
	err = s.save()
	s.mu.Unlock()
	if changed {
		s.edgesChanged()
	}
	return err
}

// RelayStanding is when the lab's account turned the relay off at Cloud
// (zero: it's on) and why Cloud suspended it ("": it isn't), as Cloud
// last said; both zero when not relayed.
func (s *Service) RelayStanding() (turnedOff time.Time, suspended string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.st.Relay == nil {
		return time.Time{}, ""
	}
	return s.st.Relay.Relay.TurnedOff, s.st.Relay.Relay.Suspended
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
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
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

// ErrNoCrowdSec is what SendBans's bans returns while CrowdSec isn't
// meant to run (nothing is public, SPEC.md §6.11): nothing to send, and
// nothing wrong.
var ErrNoCrowdSec = errors.New("CrowdSec isn't running: nothing is public")

// SendBans sends the lab's CrowdSec bans to Cloud every so often while
// the lab is relayed, until ctx ends, so the edges refuse those visitors
// for this lab (Roostack Cloud's DESIGN §5, "Collective defence"). Every
// time, not only on a change: Cloud drops bans an hour after the last
// word. Nothing is sent while bans fails: ErrNoCrowdSec is said once in
// the log, anything else (CrowdSec should be there) each time.
func (s *Service) SendBans(ctx context.Context, every time.Duration, bans func(context.Context) ([]string, error)) {
	quiet := false
	for {
		s.mu.Lock()
		relayed := s.st.Linked && s.st.Relay != nil
		s.mu.Unlock()
		if relayed {
			err := s.sendBans(ctx, bans)
			switch {
			case errors.Is(err, ErrNoCrowdSec):
				if !quiet {
					s.log.Info("Roostack Cloud: no CrowdSec bans to send to the relay while nothing is public")
				}
			case err != nil:
				s.log.Warn("Roostack Cloud: sending CrowdSec's bans to the relay", "err", err)
			}
			quiet = errors.Is(err, ErrNoCrowdSec)
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
// the edges), keeping its answer, every so often until ctx ends. Changed
// edges (one rebuilt with a new address and key, Cloud's `cloud
// edge-update`) are dialled at once, and the lab applies them.
func (s *Service) RefreshRelay(ctx context.Context, every time.Duration) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		if err := s.refreshRelay(ctx); err != nil {
			s.log.Warn("Roostack Cloud: asking how the relay stands", "err", err)
		}
	}
}

// refreshRelay asks Cloud how the relay stands once.
func (s *Service) refreshRelay(ctx context.Context) error {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	s.mu.Lock()
	relayed := s.st.Linked && s.st.Relay != nil
	s.mu.Unlock()
	if !relayed {
		return nil
	}
	var relay cloudapi.Relay
	if err := s.client.do(ctx, true, http.MethodGet, cloudapi.PathLabRelay, nil, &relay); err != nil {
		return err
	}
	s.mu.Lock()
	cur := s.st.Relay
	if cur == nil || relay.Addr != cur.Relay.Addr {
		// Unlinked meanwhile, or a new address: the next Relay takes it.
		s.mu.Unlock()
		return nil
	}
	cur.Relay.Domain, cur.Relay.Name = relay.Domain, relay.Name
	cur.Relay.TurnedOff, cur.Relay.Suspended = relay.TurnedOff, relay.Suspended
	s.st.Names = relay.Names
	changed := len(relay.Edges) > 0 && !slices.Equal(cur.Relay.Edges, relay.Edges) && s.WireGuard != nil
	next := *cur
	next.Relay.Edges = relay.Edges
	err := s.save()
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("saving how the relay stands: %w", err)
	}
	if !changed {
		return nil
	}
	// The interface dials the new edges first, and only then are they
	// kept: a failure leaves the old ones, and the next refresh tries
	// again.
	if _, err := s.WireGuard.ApplyWireGuard(ctx, s.relayInterface(fmt.Sprintf("%s/%d", next.Relay.Addr, next.Relay.Prefix), &next)); err != nil {
		return fmt.Errorf("connecting to Roostack Cloud's changed edges: %w", err)
	}
	s.mu.Lock()
	if s.st.Relay == nil {
		// Unlinked meanwhile: the interface goes after this.
		s.mu.Unlock()
		return nil
	}
	s.st.Relay.Relay.Edges = relay.Edges
	err = s.save()
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("saving the relay's edges: %w", err)
	}
	s.edgesChanged()
	return nil
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
