package manager

import (
	"context"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/state"
)

// Roostack Cloud's relay as the lab's way in, behaving as the lab's own
// (SPEC.md §10.5): each server other than the controller that serves its
// public names itself has its own tunnel to the relay's edges, which pass
// its names straight to it, and the rest to the controller.

// relayConnected is how recently a server's tunnel must have connected to
// an edge to count as connected now: its keepalive renews the handshake
// every two minutes.
const relayConnected = 3 * time.Minute

// relayTunnelServers are the servers that get a tunnel of their own to
// the relay: those serving their public routes directly, while the lab
// comes in through the relay. lab has Direct as the apply works it out.
func relayTunnelServers(relayOn bool, direct []string) []string {
	if !relayOn {
		return nil
	}
	return direct
}

// RelayServers are the lab's servers' tunnels for Roostack Cloud to know
// (cloudapi.RelayServer): each server with one whose key it has reported,
// with the public names it serves once its tunnel has connected to an
// edge. Until then its names go to the controller, which passes them on,
// so nothing goes dark while a server joins. Once it has connected, its
// names stay with it: if the server or its tunnel is down later, so are
// they, as for any server that's down (Roostack Cloud's DESIGN §5, the
// maintainer's choice of 2026-11-24, rather than falling back).
func (m *Manager) RelayServers(ctx context.Context) []cloudapi.RelayServer {
	ids := relayTunnelServers(m.labNow().Public.Relay && m.Relay.Tunnel != nil, m.Direct())
	if len(ids) == 0 {
		return nil
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		m.Log.Error("listing the names servers serve through the relay", "err", err)
		return nil
	}
	lab := m.labDirect()
	relayed := publicHostsOf(m.withStatusPage(), services, false)
	var out []cloudapi.RelayServer
	for _, r := range m.others() {
		id := r.Identity().ID
		if !slices.Contains(ids, id) {
			continue
		}
		st, ok := relayStatusOf(ctx, r)
		if !ok {
			continue
		}
		sv := cloudapi.RelayServer{ID: id, PublicKey: st.PublicKey}
		if relayJoined(st) {
			for _, h := range ownNames(lab, services, id) {
				if slices.Contains(relayed, h) && !slices.Contains(sv.Hosts, h) {
					sv.Hosts = append(sv.Hosts, h)
				}
			}
			slices.Sort(sv.Hosts)
		}
		out = append(out, sv)
	}
	slices.SortFunc(out, func(a, b cloudapi.RelayServer) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// relayStatusOf is server r's tunnel to the relay as it last said, if it
// has one with a key.
func relayStatusOf(ctx context.Context, r RemoteServer) (agent.WireGuardStatus, bool) {
	s, ok := r.(interface {
		RelayStatus(context.Context) (agent.WireGuardStatus, error)
	})
	if !ok {
		return agent.WireGuardStatus{}, false
	}
	st, err := s.RelayStatus(ctx)
	return st, err == nil && st.Configured && st.PublicKey != ""
}

// relayJoined says whether a tunnel has connected to any edge since it
// came up (WireGuard says 0 until the first handshake).
func relayJoined(st agent.WireGuardStatus) bool {
	for _, t := range st.Handshakes {
		if t > 0 {
			return true
		}
	}
	return false
}

// relayUp says whether a tunnel has connected to any edge lately.
func relayUp(st agent.WireGuardStatus, now time.Time) bool {
	for _, t := range st.Handshakes {
		if t > 0 && now.Sub(time.Unix(t, 0)) < relayConnected {
			return true
		}
	}
	return false
}

// RelayNote says how visitors reach server id's public services through
// its own tunnel to the relay, in a sentence for its card: only through
// the relay (its own web ports are closed to the internet), not yet, or
// not now; "" when it has no tunnel.
func (m *Manager) RelayNote(ctx context.Context, id string) string {
	if !slices.Contains(relayTunnelServers(m.labNow().Public.Relay && m.Relay.Tunnel != nil, m.Direct()), id) {
		return ""
	}
	for _, r := range m.others() {
		if r.Identity().ID != id {
			continue
		}
		st, ok := relayStatusOf(ctx, r)
		switch {
		case ok && relayUp(st, time.Now()):
			return "Visitors reach its public services only through Roostack Cloud's relay, which comes to it over a tunnel of its own."
		case ok && relayJoined(st):
			return "Its tunnel to Roostack Cloud's relay isn't connected, so visitors can't reach its public services. It dials the relay's edges on UDP, which a firewall on its network may need to allow."
		}
	}
	return "Its tunnel to Roostack Cloud's relay isn't connected yet, so its public services are going through the server that runs the lab meanwhile. It dials the relay's edges on UDP, which a firewall on its network may need to allow."
}
