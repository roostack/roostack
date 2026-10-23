package manager

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/units"
)

// Storage for other labs (SPEC.md §10.10): this lab keeps other labs'
// encrypted backups in their own space with a size limit. (As a guest, it
// stores its own backups at another lab: remotestorage.go.)

// Guests is storage for other labs (Manager.Guests): the labs storing
// here, their space, and which of the lab's servers holds them.
type Guests struct {
	core core
	// Hub is the host's side of the guests' link and invites, and Records
	// the store's record of guests (nil: this lab offers no storage).
	Hub     *labnet.GuestHub
	Records GuestRecords
}

// GuestRecords is the store's record of guests.
type GuestRecords interface {
	Guests(ctx context.Context, now time.Time) ([]store.Guest, error)
	SetGuestQuota(ctx context.Context, id, quota int64) error
	RemoveGuest(ctx context.Context, id int64) error
	SetGuestRemoved(ctx context.Context, id int64, at time.Time) error
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// GuestGrace is how long a removed guest lab's backups stay.
const GuestGrace = labnet.GuestGrace

// GuestHeadroom is the share of the disk (percent) kept free beyond every
// guest's quota, for the lab's own services and backups.
const GuestHeadroom = 10

// GuestView is a guest lab, for the host's page.
type GuestView struct {
	store.Guest
	Used     int64 // bytes, -1 if unknown
	LastSeen time.Time
}

// UsedPercent is how much of its space a guest uses, for a bar (0 if
// unknown).
func (g GuestView) UsedPercent() int {
	if g.Used <= 0 || g.Quota <= 0 {
		return 0
	}
	return int(min(g.Used*100/g.Quota, 100))
}

// DeleteOn is when a removed guest's backups are deleted.
func (g GuestView) DeleteOn() time.Time { return g.Removed.Add(GuestGrace) }

// OfferStorage reports whether this lab offers storage to others.
func (x *Guests) OfferStorage(ctx context.Context) bool {
	return x.Hub != nil && x.Hub.Enabled(ctx)
}

// StorageGuests lists the other labs storing here.
func (x *Guests) StorageGuests(ctx context.Context) ([]GuestView, error) {
	if x.Records == nil {
		return nil, nil
	}
	guests, err := x.Records.Guests(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	var hs map[string]int64
	host := x.guestHost(ctx)
	if host != nil {
		if st, err := host.GuestLinkStatus(ctx); err == nil {
			hs = st.Handshakes
		}
	}
	var out []GuestView
	for _, g := range guests {
		v := GuestView{Guest: g, Used: -1}
		if host != nil {
			if used, err := host.GuestUsage(ctx, g.User()); err == nil {
				v.Used = used
			}
		}
		if t := hs[g.PublicKey]; t > 0 {
			v.LastSeen = time.Unix(t, 0)
		}
		out = append(out, v)
	}
	return out, nil
}

// InviteGuest makes a storage invite for another lab, with quota bytes.
// endpoint is this server's address and guests port as they see it.
func (x *Guests) InviteGuest(ctx context.Context, name string, quota int64, endpoint string) (labnet.StorageInvite, error) {
	if x.Hub == nil {
		return labnet.StorageInvite{}, ErrNotFound
	}
	name = strings.TrimSpace(name)
	switch {
	case name == "" || len([]rune(name)) > 60:
		return labnet.StorageInvite{}, usererr.New("Give it a name, such as \"Sam's lab\".")
	case quota < units.GB:
		return labnet.StorageInvite{}, usererr.New("Offer at least 1 GB.")
	}
	if host, port, err := splitEndpoint(endpoint); err != nil || host == "" || port == 0 {
		return labnet.StorageInvite{}, usererr.New("Enter the address and port the other lab reaches this one at, such as 203.0.113.7:51821.")
	}
	if x.guestHost(ctx) == nil {
		return labnet.StorageInvite{}, usererr.New("Roostack can't reach %s, which offers the storage, right now.", x.core.nameOf(x.GuestServer(ctx)))
	}
	if err := x.quotaFits(ctx, quota, 0); err != nil {
		return labnet.StorageInvite{}, err
	}
	inv, err := x.Hub.NewInvite(ctx, name, quota, endpoint)
	if err != nil {
		return inv, err
	}
	x.core.Apply() // the guest's store starts
	return inv, nil
}

// GuestSpace is the disk of the server offering storage, as the invite
// form explains it (SPEC.md §10.10).
type GuestSpace struct {
	Free, Total int64 // the disk's free and total space
	// Promised is what other labs have been given but not used yet: free
	// space now that they may fill later (not counting removed labs).
	Promised int64
	// Left is about how much more can be promised: Free less Promised and
	// the headroom (10% of the disk).
	Left int64
}

// GuestSpaceLeft describes the space on the server offering storage. ok
// is false when it can't be asked.
func (x *Guests) GuestSpaceLeft(ctx context.Context) (GuestSpace, bool) {
	return x.spaceLeft(ctx, 0)
}

// spaceLeft is GuestSpaceLeft, not counting guest except's quota (one
// whose quota is changing).
func (x *Guests) spaceLeft(ctx context.Context, except int64) (GuestSpace, bool) {
	var sp GuestSpace
	if x.Records == nil {
		return sp, false
	}
	host := x.guestHost(ctx)
	if host == nil {
		return sp, false
	}
	var err error
	if sp.Free, sp.Total, err = host.GuestSpace(ctx); err != nil {
		return sp, false
	}
	guests, err := x.Records.Guests(ctx, time.Now())
	if err != nil {
		return sp, false
	}
	var own int64 // what the guest whose quota is changing already uses
	for _, g := range guests {
		used, _ := host.GuestUsage(ctx, g.User())
		if g.ID == except {
			own = used
			continue
		}
		if g.Removed.IsZero() { // a removed lab can't add more
			sp.Promised += max(g.Quota-used, 0)
		}
	}
	sp.Left = max(sp.Free-sp.Promised-sp.Total*GuestHeadroom/100+own, 0)
	return sp, true
}

// quotaFits checks a quota fits on the disk with room to spare, beside
// every other guest's (SPEC.md §10.10). If the disk can't be asked, it
// isn't refused.
func (x *Guests) quotaFits(ctx context.Context, quota, except int64) error {
	if sp, ok := x.spaceLeft(ctx, except); ok && quota > sp.Left {
		return usererr.New("That doesn't fit: this disk can offer about %s more (after what's promised already and some room to spare).", sizeGB(sp.Left))
	}
	return nil
}

// RemoveGuest cuts a guest lab's access. Its data stays until
// DeleteGuestData.
func (x *Guests) RemoveGuest(ctx context.Context, id int64) error {
	if x.Records == nil {
		return ErrNotFound
	}
	// Its store stops now; its link stays so it can be told, and its data
	// until the grace period ends (PurgeRemovedGuests).
	if err := x.Records.SetGuestRemoved(ctx, id, time.Now()); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	x.core.Apply()
	return nil
}

// DeleteRemovedGuest deletes a removed guest's data now, and its link.
func (x *Guests) DeleteRemovedGuest(ctx context.Context, id int64) error {
	if x.Records == nil {
		return ErrNotFound
	}
	guests, err := x.Records.Guests(ctx, time.Now())
	if err != nil {
		return err
	}
	for _, g := range guests {
		if g.ID == id && !g.Removed.IsZero() {
			return x.purgeGuest(ctx, g)
		}
	}
	return ErrNotFound
}

// PurgeRemovedGuests deletes the data of guests removed longer than
// GuestGrace ago (daily).
func (x *Guests) PurgeRemovedGuests(ctx context.Context, now time.Time) error {
	if x.Records == nil {
		return nil
	}
	guests, err := x.Records.Guests(ctx, now)
	if err != nil {
		return err
	}
	var errs []error
	for _, g := range guests {
		if !g.Removed.IsZero() && now.Sub(g.Removed) >= GuestGrace {
			x.core.logger().Info("deleting a removed guest lab's backups", "guest", g.Name, "removed", g.Removed)
			errs = append(errs, x.purgeGuest(ctx, g))
		}
	}
	return errors.Join(errs...)
}

func (x *Guests) purgeGuest(ctx context.Context, g store.Guest) error {
	host := x.guestHost(ctx)
	if host == nil {
		return fmt.Errorf("deleting %s's backups: %s can't be reached", g.Name, x.core.nameOf(x.GuestServer(ctx)))
	}
	if err := host.RemoveGuestDir(ctx, g.User()); err != nil && !errors.Is(err, node.ErrNotFound) {
		return fmt.Errorf("deleting %s's backups: %w", g.Name, err)
	}
	if err := x.Records.RemoveGuest(ctx, g.ID); err != nil {
		return err
	}
	return x.Hub.SyncPeers(ctx)
}

// SetGuestQuota changes how much a guest may store.
func (x *Guests) SetGuestQuota(ctx context.Context, id, quota int64) error {
	if x.Records == nil {
		return ErrNotFound
	}
	if quota < units.GB {
		return usererr.New("Offer at least 1 GB.")
	}
	if err := x.quotaFits(ctx, quota, id); err != nil {
		return err
	}
	if err := x.Records.SetGuestQuota(ctx, id, quota); err != nil {
		return err
	}
	x.core.Apply()
	return nil
}

// guestStores is the controller's assignment of guest stores.
func (x *Guests) guestStores(ctx context.Context) ([]generate.GuestStorage, string) {
	if x.Hub == nil || !x.Hub.Enabled(ctx) {
		return nil, ""
	}
	addr, err := x.Hub.HostAddr(ctx)
	if err != nil {
		return nil, ""
	}
	guests, err := x.Records.Guests(ctx, time.Now())
	if err != nil {
		x.core.logger().Error("reading storage guests", "err", err)
		return nil, addr.String()
	}
	var out []generate.GuestStorage
	for _, g := range guests {
		if !g.Removed.IsZero() {
			continue // no more backups
		}
		out = append(out, generate.GuestStorage{User: g.User(), Port: g.Port, Quota: g.Quota, Addr: g.Addr,
			Users: g.Users, StoreHash: g.StoreHash, AdminHash: g.AdminHash})
	}
	return out, addr.String()
}

func splitEndpoint(s string) (string, int, error) {
	host, port, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return "", 0, fmt.Errorf("no port")
	}
	n, err := strconv.Atoi(port)
	return host, n, err
}

func sizeGB(b int64) string { return units.Bytes(max(b, 0)) }

// LeftoverGuest is data a removed guest left here.
type LeftoverGuest struct {
	User string
	Size int64
}

// LeftoverGuests lists data of guests no longer storing here.
func (x *Guests) LeftoverGuests(ctx context.Context) ([]LeftoverGuest, error) {
	host := x.guestHost(ctx)
	if host == nil || x.Records == nil {
		return nil, nil
	}
	users, err := host.GuestDirs(ctx)
	if err != nil {
		return nil, err
	}
	guests, err := x.Records.Guests(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	var out []LeftoverGuest
	for _, u := range users {
		if slices.ContainsFunc(guests, func(g store.Guest) bool { return g.User() == u }) {
			continue
		}
		l := LeftoverGuest{User: u, Size: -1}
		if size, err := host.GuestUsage(ctx, u); err == nil {
			l.Size = size
		}
		out = append(out, l)
	}
	return out, nil
}

// DeleteLeftoverGuest deletes a removed guest's data for good.
func (x *Guests) DeleteLeftoverGuest(ctx context.Context, user string) error {
	left, err := x.LeftoverGuests(ctx)
	if err != nil {
		return err
	}
	host := x.guestHost(ctx)
	if !slices.ContainsFunc(left, func(l LeftoverGuest) bool { return l.User == user }) || host == nil {
		return ErrNotFound
	}
	return host.RemoveGuestDir(ctx, user)
}
