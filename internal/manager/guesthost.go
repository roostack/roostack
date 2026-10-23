package manager

import (
	"context"
	"errors"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/usererr"
)

// Storage for other labs is offered by one of the lab's servers (SPEC.md
// §10.10): the controller unless the Owner chose another (e.g. a server
// with a big disk). The controller keeps the records and answers guests;
// that server runs their link and stores.

// guestServerKey names the server hosting other labs' backups.
const guestServerKey = "guests.server"

// GuestServer is the ID of the server hosting other labs' backups.
func (x *Guests) GuestServer(ctx context.Context) string {
	if x.Records != nil {
		if id, err := x.Records.Setting(ctx, guestServerKey); err == nil && id != "" && x.core.node(id) != nil {
			return id
		}
	}
	return x.core.labConfig().NodeID
}

// guestHost is that server.
func (x *Guests) guestHost(ctx context.Context) node.GuestHost {
	return node.Capability(x.core.node(x.GuestServer(ctx)), node.Node.GuestHost)
}

// GuestLinkAgent configures the guests' link on the hosting server (the
// storage hub's agent).
func (x *Guests) GuestLinkAgent() GuestLinkAgent { return GuestLinkAgent{x} }

// GuestLinkAgent routes the guests' link to the hosting server.
type GuestLinkAgent struct{ x *Guests }

func (a GuestLinkAgent) ApplyWireGuard(ctx context.Context, wg agent.WireGuard) (string, error) {
	h := a.x.guestHost(ctx)
	if h == nil {
		return "", errors.New("the server offering storage for others can't be reached")
	}
	return h.ApplyGuestLink(ctx, wg)
}

// GuestUsage is how much a guest stores on the hosting server.
func (x *Guests) GuestUsage(ctx context.Context, user string) (int64, error) {
	h := x.guestHost(ctx)
	if h == nil {
		return 0, errors.New("the server offering storage for others can't be reached")
	}
	return h.GuestUsage(ctx, user)
}

// HostsGuests reports whether this server (the controller) hosts other
// labs' backups itself.
func (x *Guests) HostsGuests(ctx context.Context) bool {
	return x.GuestServer(ctx) == x.core.labConfig().NodeID
}

// SetGuestServer moves storage for others to another server. Only while
// no other lab stores anything (moving their backups isn't supported).
func (x *Guests) SetGuestServer(ctx context.Context, id string) error {
	if x.Records == nil || x.Hub == nil {
		return ErrNotFound
	}
	n := x.core.node(id)
	if n == nil {
		return ErrNotFound
	}
	if n.GuestHost() == nil || (n != x.core.node(x.core.labConfig().NodeID) && n.Settings() == nil) {
		return usererr.New("Roostack can't reach %s right now.", n.Identity().Name)
	}
	old := x.GuestServer(ctx)
	if old == n.Identity().ID {
		return nil
	}
	guests, err := x.Records.Guests(ctx, time.Now())
	if err != nil {
		return err
	}
	left, _ := x.LeftoverGuests(ctx)
	if len(guests) > 0 || len(left) > 0 {
		return usererr.New("Other labs store backups on %s: remove them (and delete their backups) before offering storage elsewhere.",
			x.core.nameOf(old))
	}
	if h := x.guestHost(ctx); h != nil && x.Hub.Enabled(ctx) {
		if err := h.RemoveGuestLink(ctx); err != nil {
			x.core.logger().Warn("removing the guests' link", "server", x.core.nameOf(old), "err", err)
		}
	}
	if err := x.Records.SetSetting(ctx, guestServerKey, n.Identity().ID); err != nil {
		return err
	}
	if x.Hub.Enabled(ctx) {
		if err := x.Hub.SyncPeers(ctx); err != nil {
			return err
		}
	}
	x.core.Apply()
	return nil
}
