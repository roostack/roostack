package node

import (
	"context"
	"testing"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
)

// relayLinks is a LinkAgent keeping only the relay's interface.
type relayLinks struct {
	applied []agent.WireGuard
	status  agent.WireGuardStatus
}

func (l *relayLinks) ApplyWireGuard(_ context.Context, wg agent.WireGuard) (string, error) {
	l.applied = append(l.applied, wg)
	l.status = agent.WireGuardStatus{Configured: !wg.Down, PublicKey: "key", Address: wg.Address}
	return "key", nil
}

func (l *relayLinks) WireGuardStatus(context.Context, string) (agent.WireGuardStatus, error) {
	return l.status, nil
}
func (l *relayLinks) RemoveWireGuard(context.Context, string) error { return nil }
func (l *relayLinks) ApplyLinks(context.Context, agent.Links) error { return nil }

// TestApplyRelay: a server's own tunnel to Roostack Cloud's relay is set
// up as assigned and stopped (keeping its key) when it no longer is; the
// controller's, which Roostack Cloud's link keeps, is left alone.
func TestApplyRelay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	links := &relayLinks{}
	n := &Local{Links: links}
	var act *activity.Log // a nil log records nothing
	wg := &agent.WireGuard{Interface: agent.RelayInterface, Address: "100.81.2.1/16"}
	if err := n.applyRelay(ctx, act, wg, false); err != nil || len(links.applied) != 0 {
		t.Errorf("on the controller: %v %+v", err, links.applied)
	}
	if err := n.applyRelay(ctx, act, wg, true); err != nil || len(links.applied) != 1 || links.applied[0].Address != "100.81.2.1/16" {
		t.Fatalf("assigned: %v %+v", err, links.applied)
	}
	if st, err := n.RelayStatus(ctx); err != nil || st.PublicKey != "key" {
		t.Errorf("status: %+v %v", st, err)
	}
	if err := n.applyRelay(ctx, act, nil, true); err != nil || len(links.applied) != 2 || !links.applied[1].Down ||
		links.applied[1].Interface != agent.RelayInterface {
		t.Errorf("no longer assigned: %v %+v", err, links.applied)
	}
	if err := n.applyRelay(ctx, act, nil, true); err != nil || len(links.applied) != 2 {
		t.Errorf("stopped already: %v %+v", err, links.applied)
	}
}
