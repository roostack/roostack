package manager

import (
	"context"
	"errors"
	"time"

	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/node"
)

// Storage for other labs is offered by one of the lab's servers (SPEC.md
// §10.10): the controller unless the Owner chose another (e.g. a server
// with a big disk). The controller keeps the records and answers guests;
// that server runs their link and stores.

// guestServerKey names the server hosting other labs' backups.
const guestServerKey = "guests.server"

// GuestServer is the ID of the server hosting other labs' backups.
func (m *Manager) GuestServer(ctx context.Context) string {
	if m.GuestRecords != nil {
		if id, err := m.GuestRecords.Setting(ctx, guestServerKey); err == nil && id != "" && m.node(id) != nil {
			return id
		}
	}
	return m.Lab.NodeID
}

// guestHost is that server.
func (m *Manager) guestHost(ctx context.Context) node.GuestHost {
	h, _ := m.node(m.GuestServer(ctx)).(node.GuestHost)
	return h
}

// GuestLinkAgent configures the guests' link on the hosting server (the
// storage hub's agent).
func (m *Manager) GuestLinkAgent() GuestLinkAgent { return GuestLinkAgent{m} }

// GuestLinkAgent routes the guests' link to the hosting server.
type GuestLinkAgent struct{ m *Manager }

func (a GuestLinkAgent) ApplyWireGuard(ctx context.Context, wg agent.WireGuard) (string, error) {
	h := a.m.guestHost(ctx)
	if h == nil {
		return "", errors.New("the server offering storage for others can't be reached")
	}
	return h.ApplyGuestLink(ctx, wg)
}

// GuestUsage is how much a guest stores on the hosting server.
func (m *Manager) GuestUsage(ctx context.Context, user string) (int64, error) {
	h := m.guestHost(ctx)
	if h == nil {
		return 0, errors.New("the server offering storage for others can't be reached")
	}
	return h.GuestUsage(ctx, user)
}

// HostsGuests reports whether this server (the controller) hosts other
// labs' backups itself.
func (m *Manager) HostsGuests(ctx context.Context) bool { return m.GuestServer(ctx) == m.Lab.NodeID }

// SetGuestServer moves storage for others to another server. Only while
// no other lab stores anything (moving their backups isn't supported).
func (m *Manager) SetGuestServer(ctx context.Context, id string) error {
	if m.GuestRecords == nil || m.GuestHub == nil {
		return ErrNotFound
	}
	n := m.node(id)
	if n == nil {
		return ErrNotFound
	}
	if _, ok := n.(node.GuestHost); !ok || (n != m.Node && n.Settings() == nil) {
		return formErrorf("MyLab can't reach %s right now.", n.Identity().Name)
	}
	old := m.GuestServer(ctx)
	if old == n.Identity().ID {
		return nil
	}
	guests, err := m.GuestRecords.Guests(ctx, time.Now())
	if err != nil {
		return err
	}
	left, _ := m.LeftoverGuests(ctx)
	if len(guests) > 0 || len(left) > 0 {
		return formErrorf("Other labs store backups on %s: remove them (and delete their backups) before offering storage elsewhere.",
			m.nameOf(old))
	}
	if h := m.guestHost(ctx); h != nil && m.GuestHub.Enabled(ctx) {
		if err := h.RemoveGuestLink(ctx); err != nil {
			m.Log.Warn("removing the guests' link", "server", m.nameOf(old), "err", err)
		}
	}
	if err := m.GuestRecords.SetSetting(ctx, guestServerKey, n.Identity().ID); err != nil {
		return err
	}
	if m.GuestHub.Enabled(ctx) {
		if err := m.GuestHub.SyncPeers(ctx); err != nil {
			return err
		}
	}
	m.Apply()
	return nil
}
