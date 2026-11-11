package node

import (
	"context"
	"fmt"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
)

// The lab's VPN (SPEC.md §10.11): the server its people's devices connect
// to runs its interface, roostack-people, with every device as a peer.
// The controller keeps the devices and sends them with the assignment.

// applyPeople sets up the VPN's interface as assigned, or stops it on a
// server it's no longer on, keeping its key so that devices' settings
// still work when it's turned on again.
func (n *Local) applyPeople(ctx context.Context, act *activity.Log, wg *agent.WireGuard) error {
	if n.Links == nil {
		return nil
	}
	if wg == nil {
		st, err := n.Links.WireGuardStatus(ctx, agent.PeopleInterface)
		if err != nil || !st.Configured {
			return nil
		}
		act.Step("The lab's VPN is off on this server")
		_, err = n.Links.ApplyWireGuard(ctx, agent.WireGuard{Interface: agent.PeopleInterface, Address: st.Address, Down: true})
		return err
	}
	act.Step("Updating the lab's VPN")
	act.Printf("%d devices, on UDP port %d", len(wg.Peers), wg.ListenPort)
	_, err := n.Links.ApplyWireGuard(ctx, *wg)
	return err
}

// VPNStatus is the lab's VPN's interface on this server: its public key,
// which devices' settings name, and when each device last connected.
func (n *Local) VPNStatus(ctx context.Context) (agent.WireGuardStatus, error) {
	if n.Links == nil {
		return agent.WireGuardStatus{}, fmt.Errorf("this server has no WireGuard")
	}
	return n.Links.WireGuardStatus(ctx, agent.PeopleInterface)
}

// VPNStatus is what the server last said about the lab's VPN's interface
// there (not configured if it has none, or hasn't said).
func (r *Remote) VPNStatus(context.Context) (agent.WireGuardStatus, error) {
	st, err := r.wireStatus()
	if err != nil || st.VPN == nil {
		return agent.WireGuardStatus{}, err
	}
	return *st.VPN, nil
}
