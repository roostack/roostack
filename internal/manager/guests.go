package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/units"
)

// Storage for other labs (SPEC.md §10.10). As host, this lab keeps other
// labs' encrypted backups in their own space with a size limit; as guest,
// it stores its own backups at another lab.

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

// LinkAgent is roostack-agent's WireGuard operations for links between labs.
type LinkAgent interface {
	ApplyWireGuard(ctx context.Context, wg agent.WireGuard) (string, error)
	WireGuardStatus(ctx context.Context, iface string) (agent.WireGuardStatus, error)
	RemoveWireGuard(ctx context.Context, iface string) error
}

// FirstProxyPort is where the controller forwards its servers' backups
// to the first other lab it stores at (the next one +1, …).
const FirstProxyPort = 8200

// GuestHeadroom is the share of the disk (percent) kept free beyond every
// guest's quota, for the lab's own services and backups.
const GuestHeadroom = 10

// GuestView is a guest lab, for the host's page.
type GuestView struct {
	store.Guest
	Used     int64 // bytes, -1 if unknown
	LastSeen time.Time
}

// DeleteOn is when a removed guest's backups are deleted.
func (g GuestView) DeleteOn() time.Time { return g.Removed.Add(GuestGrace) }

// OfferStorage reports whether this lab offers storage to others.
func (m *Manager) OfferStorage(ctx context.Context) bool {
	return m.GuestHub != nil && m.GuestHub.Enabled(ctx)
}

// StorageGuests lists the other labs storing here.
func (m *Manager) StorageGuests(ctx context.Context) ([]GuestView, error) {
	if m.GuestRecords == nil {
		return nil, nil
	}
	guests, err := m.GuestRecords.Guests(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	var hs map[string]int64
	host := m.guestHost(ctx)
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
func (m *Manager) InviteGuest(ctx context.Context, name string, quota int64, endpoint string) (labnet.StorageInvite, error) {
	if m.GuestHub == nil {
		return labnet.StorageInvite{}, ErrNotFound
	}
	name = strings.TrimSpace(name)
	switch {
	case name == "" || len([]rune(name)) > 60:
		return labnet.StorageInvite{}, formErrorf("Give it a name, such as \"Sam's lab\".")
	case quota < units.GB:
		return labnet.StorageInvite{}, formErrorf("Offer at least 1 GB.")
	}
	if host, port, err := splitEndpoint(endpoint); err != nil || host == "" || port == 0 {
		return labnet.StorageInvite{}, formErrorf("Enter the address and port the other lab reaches this one at, such as 203.0.113.7:51821.")
	}
	if m.guestHost(ctx) == nil {
		return labnet.StorageInvite{}, formErrorf("Roostack can't reach %s, which offers the storage, right now.", m.nameOf(m.GuestServer(ctx)))
	}
	if err := m.quotaFits(ctx, quota, 0); err != nil {
		return labnet.StorageInvite{}, err
	}
	inv, err := m.GuestHub.NewInvite(ctx, name, quota, endpoint)
	if err != nil {
		return inv, err
	}
	m.Apply() // the guest's store starts
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
func (m *Manager) GuestSpaceLeft(ctx context.Context) (GuestSpace, bool) {
	return m.spaceLeft(ctx, 0)
}

// spaceLeft is GuestSpaceLeft, not counting guest except's quota (one
// whose quota is changing).
func (m *Manager) spaceLeft(ctx context.Context, except int64) (GuestSpace, bool) {
	var sp GuestSpace
	if m.GuestRecords == nil {
		return sp, false
	}
	host := m.guestHost(ctx)
	if host == nil {
		return sp, false
	}
	var err error
	if sp.Free, sp.Total, err = host.GuestSpace(ctx); err != nil {
		return sp, false
	}
	guests, err := m.GuestRecords.Guests(ctx, time.Now())
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
func (m *Manager) quotaFits(ctx context.Context, quota, except int64) error {
	if sp, ok := m.spaceLeft(ctx, except); ok && quota > sp.Left {
		return formErrorf("That doesn't fit: this disk can offer about %s more (after what's promised already and some room to spare).", sizeGB(sp.Left))
	}
	return nil
}

// RemoveGuest cuts a guest lab's access. Its data stays until
// DeleteGuestData.
func (m *Manager) RemoveGuest(ctx context.Context, id int64) error {
	if m.GuestRecords == nil {
		return ErrNotFound
	}
	// Its store stops now; its link stays so it can be told, and its data
	// until the grace period ends (PurgeRemovedGuests).
	if err := m.GuestRecords.SetGuestRemoved(ctx, id, time.Now()); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	m.Apply()
	return nil
}

// DeleteRemovedGuest deletes a removed guest's data now, and its link.
func (m *Manager) DeleteRemovedGuest(ctx context.Context, id int64) error {
	if m.GuestRecords == nil {
		return ErrNotFound
	}
	guests, err := m.GuestRecords.Guests(ctx, time.Now())
	if err != nil {
		return err
	}
	for _, g := range guests {
		if g.ID == id && !g.Removed.IsZero() {
			return m.purgeGuest(ctx, g)
		}
	}
	return ErrNotFound
}

// PurgeRemovedGuests deletes the data of guests removed longer than
// GuestGrace ago (daily).
func (m *Manager) PurgeRemovedGuests(ctx context.Context, now time.Time) error {
	if m.GuestRecords == nil {
		return nil
	}
	guests, err := m.GuestRecords.Guests(ctx, now)
	if err != nil {
		return err
	}
	var errs []error
	for _, g := range guests {
		if !g.Removed.IsZero() && now.Sub(g.Removed) >= GuestGrace {
			m.Log.Info("deleting a removed guest lab's backups", "guest", g.Name, "removed", g.Removed)
			errs = append(errs, m.purgeGuest(ctx, g))
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) purgeGuest(ctx context.Context, g store.Guest) error {
	host := m.guestHost(ctx)
	if host == nil {
		return fmt.Errorf("deleting %s's backups: %s can't be reached", g.Name, m.nameOf(m.GuestServer(ctx)))
	}
	if err := host.RemoveGuestDir(ctx, g.User()); err != nil && !errors.Is(err, node.ErrNotFound) {
		return fmt.Errorf("deleting %s's backups: %w", g.Name, err)
	}
	if err := m.GuestRecords.RemoveGuest(ctx, g.ID); err != nil {
		return err
	}
	return m.GuestHub.SyncPeers(ctx)
}

// SetGuestQuota changes how much a guest may store.
func (m *Manager) SetGuestQuota(ctx context.Context, id, quota int64) error {
	if m.GuestRecords == nil {
		return ErrNotFound
	}
	if quota < units.GB {
		return formErrorf("Offer at least 1 GB.")
	}
	if err := m.quotaFits(ctx, quota, id); err != nil {
		return err
	}
	if err := m.GuestRecords.SetGuestQuota(ctx, id, quota); err != nil {
		return err
	}
	m.Apply()
	return nil
}

// guestStores is the controller's assignment of guest stores.
func (m *Manager) guestStores(ctx context.Context) ([]generate.GuestStorage, string) {
	if m.GuestHub == nil || !m.GuestHub.Enabled(ctx) {
		return nil, ""
	}
	addr, err := m.GuestHub.HostAddr(ctx)
	if err != nil {
		return nil, ""
	}
	guests, err := m.GuestRecords.Guests(ctx, time.Now())
	if err != nil {
		m.Log.Error("reading storage guests", "err", err)
		return nil, addr.String()
	}
	var out []generate.GuestStorage
	for _, g := range guests {
		if !g.Removed.IsZero() {
			continue // no more backups
		}
		out = append(out, generate.GuestStorage{User: g.User(), Port: g.Port, Quota: g.Quota,
			StoreHash: g.StoreHash, AdminHash: g.AdminHash})
	}
	return out, addr.String()
}

// RemoteStorage is this lab's storage at another lab: a provider.
type RemoteStorage struct {
	HostName      string     `json:"host_name"`
	Iface         string     `json:"iface"`
	HostAddr      netip.Addr `json:"host_addr"`
	User          string     `json:"user"`
	StorePassword string     `json:"store_password"`
	AdminPassword string     `json:"admin_password"`
	Port          int        `json:"port"`
	Quota         int64      `json:"quota"`
	// ProxyPort is where the controller forwards its servers' backups.
	ProxyPort int `json:"proxy_port"`
	// Ref is its backup choice ("provider:<id>"); not stored.
	Ref string `json:"-"`
}

// AddRemoteStorage links this lab to another lab's storage with its
// invite; its servers can then choose it on their Backups page.
func (m *Manager) AddRemoteStorage(ctx context.Context, code string) error {
	if m.BackupStore == nil || m.LinkAgent == nil {
		return ErrNotFound
	}
	inv, err := labnet.ParseStorageInvite(code)
	if err != nil {
		return formErrorf("%s", err)
	}
	remotes, err := m.remoteStorage(ctx)
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, r := range remotes {
		used[r.Iface] = true
	}
	n := 1
	for ; n <= 9 && used["roostack-st"+strconv.Itoa(n)]; n++ {
	}
	if n > 9 {
		return formErrorf("This lab already stores at 9 other labs.")
	}
	iface := "roostack-st" + strconv.Itoa(n)
	if err := labnet.JoinStorage(ctx, inv, iface, m.LinkAgent); err != nil {
		m.LinkAgent.RemoveWireGuard(ctx, iface)
		return formErrorf("%s", err)
	}
	r := RemoteStorage{HostName: inv.HostName, Iface: iface, HostAddr: inv.HostAddr, User: inv.User,
		StorePassword: inv.StorePassword, AdminPassword: inv.AdminPassword, Port: inv.Port, Quota: inv.Quota,
		ProxyPort: FirstProxyPort + n - 1}
	cfg, _ := json.Marshal(r)
	if _, err := m.BackupStore.AddBackupProvider(ctx, store.BackupProvider{Kind: "remote",
		Name: inv.HostName + "'s storage", Config: string(cfg)}); err != nil {
		return err
	}
	if m.RemoteAdded != nil {
		m.RemoteAdded(r)
	}
	m.Apply() // the controller's firewall lets its servers reach the proxy
	return nil
}

// remoteStorage lists this lab's storage at other labs.
func (m *Manager) remoteStorage(ctx context.Context) ([]RemoteStorage, error) {
	providers, err := m.BackupStore.BackupProviders(ctx)
	if err != nil {
		return nil, err
	}
	var out []RemoteStorage
	for _, p := range providers {
		if p.Kind != "remote" {
			continue
		}
		var r RemoteStorage
		if json.Unmarshal([]byte(p.Config), &r) == nil {
			r.Ref = fmt.Sprintf("provider:%d", p.ID)
			out = append(out, r)
		}
	}
	return out, nil
}

// RemoteStorages lists this lab's storage at other labs (e.g. to start
// the forwarding proxies at startup).
func (m *Manager) RemoteStorages(ctx context.Context) []RemoteStorage {
	if m.BackupStore == nil {
		return nil
	}
	r, _ := m.remoteStorage(ctx)
	return r
}

// RemoteUsage asks the host how much this lab stores there.
func (m *Manager) RemoteUsage(ctx context.Context, r RemoteStorage) (labnet.GuestUsage, error) {
	u, err := labnet.Usage(ctx, r.HostAddr, r.User, r.AdminPassword)
	if err == nil && u.Host != "" && u.Host != r.HostName {
		m.renameRemote(ctx, r, u.Host)
	}
	if err == nil {
		m.remoteMu.Lock()
		if m.remoteSeen == nil {
			m.remoteSeen = map[string]labnet.GuestUsage{}
		}
		m.remoteSeen[r.Ref] = u
		m.remoteMu.Unlock()
	}
	return u, err
}

// CheckRemotes asks every lab storing this one's backups how it's going
// (hourly), so a removal is noticed.
func (m *Manager) CheckRemotes(ctx context.Context) {
	for _, r := range m.RemoteStorages(ctx) {
		m.RemoteUsage(ctx, r)
	}
}

// EndingStorage is storage at another lab that has stopped keeping this
// lab's backups: what's there is deleted on DeleteOn.
type EndingStorage struct {
	HostName string
	DeleteOn time.Time
}

// EndingStorages lists them, as last heard.
func (m *Manager) EndingStorages(ctx context.Context) []EndingStorage {
	m.remoteMu.Lock()
	defer m.remoteMu.Unlock()
	var out []EndingStorage
	for _, r := range m.RemoteStorages(ctx) {
		if u, ok := m.remoteSeen[r.Ref]; ok && !u.DeleteOn.IsZero() {
			out = append(out, EndingStorage{HostName: r.HostName, DeleteOn: u.DeleteOn})
		}
	}
	return out
}

// renameRemote follows a host lab's new name.
func (m *Manager) renameRemote(ctx context.Context, r RemoteStorage, name string) {
	_, id, _ := strings.Cut(r.Ref, ":")
	p, err := m.provider(ctx, id)
	if err != nil {
		return
	}
	r.HostName = name
	cfg, _ := json.Marshal(r)
	p.Name, p.Config = name+"'s storage", string(cfg)
	if err := m.BackupStore.UpdateBackupProvider(ctx, p); err != nil {
		m.Log.Warn("renaming another lab's storage", "err", err)
	}
}

// proxyPorts are the controller's forwarding ports, for its firewall.
func (m *Manager) proxyPorts(ctx context.Context) []int {
	var ports []int
	for _, r := range m.RemoteStorages(ctx) {
		ports = append(ports, r.ProxyPort)
	}
	slices.Sort(ports)
	return ports
}

// remoteRepos returns a server's repository at another lab: as the
// server reaches it (through the controller's proxy) and as the
// controller does (directly, with the lab's admin access there).
func (m *Manager) remoteRepos(r RemoteStorage, labID, serverID string) (serverURL string, serverEnv []string, adminURL string, adminEnv []string) {
	// rest-server takes at most two levels; the user is this lab's own.
	path := "/" + r.User + "/" + serverID
	host := m.addressOf(m.Lab.NodeID)
	if serverID == m.Lab.NodeID {
		host = "127.0.0.1"
	}
	serverURL = fmt.Sprintf("rest:http://%s:%d%s", host, r.ProxyPort, path)
	serverEnv = []string{"RESTIC_REST_USERNAME=" + r.User, "RESTIC_REST_PASSWORD=" + r.StorePassword}
	adminURL = fmt.Sprintf("rest:http://%s:%d%s", r.HostAddr, r.Port+1, path)
	adminEnv = []string{"RESTIC_REST_USERNAME=" + r.User, "RESTIC_REST_PASSWORD=" + r.AdminPassword}
	return
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
func (m *Manager) LeftoverGuests(ctx context.Context) ([]LeftoverGuest, error) {
	host := m.guestHost(ctx)
	if host == nil || m.GuestRecords == nil {
		return nil, nil
	}
	users, err := host.GuestDirs(ctx)
	if err != nil {
		return nil, err
	}
	guests, err := m.GuestRecords.Guests(ctx, time.Now())
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
func (m *Manager) DeleteLeftoverGuest(ctx context.Context, user string) error {
	left, err := m.LeftoverGuests(ctx)
	if err != nil {
		return err
	}
	host := m.guestHost(ctx)
	if !slices.ContainsFunc(left, func(l LeftoverGuest) bool { return l.User == user }) || host == nil {
		return ErrNotFound
	}
	return host.RemoveGuestDir(ctx, user)
}
