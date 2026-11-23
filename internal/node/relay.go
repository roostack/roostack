package node

import (
	"context"
	"fmt"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
)

// Roostack Cloud's relay (SPEC.md §10.5): a server other than the
// controller that serves public names itself has a tunnel of its own to
// the relay's edges, roostack-relay, so their visitors come straight to
// it. The controller tells Cloud its key and gives it the edges with the
// assignment.

// applyRelay sets up this server's tunnel to the relay as assigned, or
// stops it when it's no longer assigned, keeping its key so that Cloud
// knows it if it comes back. The controller's own tunnel is Roostack
// Cloud's link's (internal/cloud), so a controller (node false) leaves it
// alone.
func (n *Local) applyRelay(ctx context.Context, act *activity.Log, wg *agent.WireGuard, node bool) error {
	if n.Links == nil || !node {
		return nil
	}
	if wg == nil {
		st, err := n.Links.WireGuardStatus(ctx, agent.RelayInterface)
		if err != nil || !st.Configured {
			return nil
		}
		act.Step("This server's public services no longer come through Roostack Cloud's relay")
		_, err = n.Links.ApplyWireGuard(ctx, agent.WireGuard{Interface: agent.RelayInterface, Address: st.Address, Down: true})
		return err
	}
	act.Step("Updating this server's tunnel to Roostack Cloud's relay")
	_, err := n.Links.ApplyWireGuard(ctx, *wg)
	return err
}

// RelayStatus is this server's tunnel to the relay: its public key, which
// the controller tells Cloud, and when each edge last connected.
func (n *Local) RelayStatus(ctx context.Context) (agent.WireGuardStatus, error) {
	if n.Links == nil {
		return agent.WireGuardStatus{}, fmt.Errorf("this server has no WireGuard")
	}
	return n.Links.WireGuardStatus(ctx, agent.RelayInterface)
}

// RelayStatus is what the server last said about its tunnel to the relay
// (not configured if it has none, or hasn't said).
func (r *Remote) RelayStatus(context.Context) (agent.WireGuardStatus, error) {
	st, err := r.wireStatus()
	if err != nil || st.Relay == nil {
		return agent.WireGuardStatus{}, err
	}
	return *st.Relay, nil
}
