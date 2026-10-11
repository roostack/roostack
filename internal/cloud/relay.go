package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/agent"
)

// WireGuardAgent is roostack-agent's WireGuard, for the relay's interface
// (its private key stays with the agent).
type WireGuardAgent interface {
	ApplyWireGuard(ctx context.Context, wg agent.WireGuard) (string, error)
	RemoveWireGuard(ctx context.Context, iface string) error
}

// relayState is the relay as Cloud last answered, kept in link.json.
type relayState struct {
	// Hosts are the names asked to be relayed, and Relay Cloud's answer.
	Hosts []string       `json:"hosts"`
	Relay cloudapi.Relay `json:"relay"`
}

// placeholderAddr is the relay interface's address before Cloud has given
// it one: the interface exists only to have its key made (no peers).
const placeholderAddr = "100.81.255.254/32"

// Relay has Roostack Cloud's relay bring visitors for hosts to the lab
// (Roostack Cloud's DESIGN §5, SPEC.md §10.5): the agent makes the relay's
// WireGuard interface (and its key), Cloud is told the key and the names
// and answers with the edges, then the interface dials every edge. Asking
// again with the same names changes nothing; with others, Cloud is told.
func (s *Service) Relay(ctx context.Context, hosts []string) error {
	if s.WireGuard == nil {
		return errors.New("nothing here can set up the relay's network")
	}
	hosts = slices.Sorted(slices.Values(hosts))
	s.mu.Lock()
	linked, cur := s.st.Linked, s.st.Relay
	s.mu.Unlock()
	if !linked {
		return errors.New("the lab isn't linked to Roostack Cloud")
	}
	if cur != nil && slices.Equal(cur.Hosts, hosts) {
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
	if err := s.client.do(ctx, true, http.MethodPut, cloudapi.PathLabRelay, cloudapi.RelayRequest{PublicKey: pub, Hosts: hosts}, &relay); err != nil {
		return err
	}
	a, err := netip.ParseAddr(relay.Addr)
	if err != nil || !agent.RelayRange.Contains(a) || len(relay.Edges) == 0 {
		return errors.New("Roostack Cloud's answer had no relay address or edges")
	}
	st := &relayState{Hosts: hosts, Relay: relay}
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
