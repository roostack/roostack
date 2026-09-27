package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/labnet"
	"github.com/mylab/mylab/internal/store"
)

// Storage for other labs (SPEC.md §10.10). As host, this lab keeps other
// labs' encrypted backups in their own space with a size limit; as guest,
// it stores its own backups at another lab.

// GuestRecords is the store's record of guests.
type GuestRecords interface {
	Guests(ctx context.Context, now time.Time) ([]store.Guest, error)
	SetGuestQuota(ctx context.Context, id, quota int64) error
	RemoveGuest(ctx context.Context, id int64) error
}

// LinkAgent is mylab-agent's WireGuard operations for links between labs.
type LinkAgent interface {
	ApplyWireGuard(ctx context.Context, wg agent.WireGuard) (string, error)
	WireGuardStatus(ctx context.Context, iface string) (agent.WireGuardStatus, error)
	RemoveWireGuard(ctx context.Context, iface string) error
}

// FirstProxyPort is where the controller forwards its machines' backups
// to the first other lab it stores at (the next one +1, …).
const FirstProxyPort = 8200

// headroom is free space kept beyond every guest's quota (10%).
const headroom = 10

// GuestView is a guest lab, for the host's page.
type GuestView struct {
	store.Guest
	Used     int64 // bytes, -1 if unknown
	LastSeen time.Time
}

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
	if m.LinkAgent != nil {
		if st, err := m.LinkAgent.WireGuardStatus(ctx, agent.GuestsInterface); err == nil {
			hs = st.Handshakes
		}
	}
	var out []GuestView
	for _, g := range guests {
		v := GuestView{Guest: g, Used: -1}
		if m.GuestUsage != nil {
			if used, err := m.GuestUsage(g.User()); err == nil {
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
// endpoint is this machine's address and guests port as they see it.
func (m *Manager) InviteGuest(ctx context.Context, name string, quota int64, endpoint string) (labnet.StorageInvite, error) {
	if m.GuestHub == nil {
		return labnet.StorageInvite{}, ErrNotFound
	}
	name = strings.TrimSpace(name)
	switch {
	case name == "" || len([]rune(name)) > 60:
		return labnet.StorageInvite{}, formErrorf("Give it a name, such as \"Tyris's lab\".")
	case quota < 1<<30:
		return labnet.StorageInvite{}, formErrorf("Offer at least 1 GB.")
	}
	if host, port, err := splitEndpoint(endpoint); err != nil || host == "" || port == 0 {
		return labnet.StorageInvite{}, formErrorf("Enter the address and port the other lab reaches this one at, such as 203.0.113.7:51821.")
	}
	// Every quota must fit on the disk, with room to spare.
	guests, err := m.GuestRecords.Guests(ctx, time.Now())
	if err != nil {
		return labnet.StorageInvite{}, err
	}
	promised := quota
	for _, g := range guests {
		promised += g.Quota
	}
	if free, total, err := diskSpace(m.Lab.Paths.BackupStoreDir()); err == nil {
		used := int64(0)
		for _, g := range guests {
			if m.GuestUsage != nil {
				if u, err := m.GuestUsage(g.User()); err == nil {
					used += u
				}
			}
		}
		// Space still to hand out: what's free, plus what guests already
		// have, less the headroom.
		available := free + used - total*headroom/100
		if promised > available {
			return labnet.StorageInvite{}, formErrorf("That doesn't fit: this disk can offer about %s more (after what's promised already and some room to spare).",
				sizeGB(available-(promised-quota)))
		}
	}
	inv, err := m.GuestHub.NewInvite(ctx, name, quota, endpoint)
	if err != nil {
		return inv, err
	}
	m.Apply() // the guest's store starts
	return inv, nil
}

// RemoveGuest cuts a guest lab's access. Its data stays until
// DeleteGuestData.
func (m *Manager) RemoveGuest(ctx context.Context, id int64) error {
	if m.GuestRecords == nil {
		return ErrNotFound
	}
	if err := m.GuestRecords.RemoveGuest(ctx, id); err != nil {
		return err
	}
	if err := m.GuestHub.SyncPeers(ctx); err != nil {
		return err
	}
	m.Apply() // its store stops
	return nil
}

// SetGuestQuota changes how much a guest may store.
func (m *Manager) SetGuestQuota(ctx context.Context, id, quota int64) error {
	if m.GuestRecords == nil {
		return ErrNotFound
	}
	if quota < 1<<30 {
		return formErrorf("Offer at least 1 GB.")
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
	// ProxyPort is where the controller forwards its machines' backups.
	ProxyPort int `json:"proxy_port"`
}

// AddRemoteStorage links this lab to another lab's storage with its
// invite; its machines can then choose it on their Backups page.
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
	for ; n <= 9 && used["mylab-st"+strconv.Itoa(n)]; n++ {
	}
	if n > 9 {
		return formErrorf("This lab already stores at 9 other labs.")
	}
	iface := "mylab-st" + strconv.Itoa(n)
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
	m.Apply() // the controller's firewall lets its machines reach the proxy
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
	return labnet.Usage(ctx, r.HostAddr, r.User, r.AdminPassword)
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

// remoteRepos returns a machine's repository at another lab: as the
// machine reaches it (through the controller's proxy) and as the
// controller does (directly, with the lab's admin access there).
func (m *Manager) remoteRepos(r RemoteStorage, labID, machineID string) (machineURL string, machineEnv []string, adminURL string, adminEnv []string) {
	path := "/" + r.User + "/" + labID + "/" + machineID
	host := m.addressOf(m.Lab.NodeID)
	if machineID == m.Lab.NodeID {
		host = "127.0.0.1"
	}
	machineURL = fmt.Sprintf("rest:http://%s:%d%s", host, r.ProxyPort, path)
	machineEnv = []string{"RESTIC_REST_USERNAME=" + r.User, "RESTIC_REST_PASSWORD=" + r.StorePassword}
	adminURL = fmt.Sprintf("rest:http://%s:%d%s", r.HostAddr, r.Port+1, path)
	adminEnv = []string{"RESTIC_REST_USERNAME=" + r.User, "RESTIC_REST_PASSWORD=" + r.AdminPassword}
	return
}

// diskSpace returns the free and total bytes of the disk holding dir (or
// its nearest existing parent).
func diskSpace(dir string) (free, total int64, err error) {
	for {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		parent := dir[:max(strings.LastIndex(dir, "/"), 1)]
		if parent == dir {
			break
		}
		dir = parent
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	return int64(st.Bavail) * st.Bsize, int64(st.Blocks) * st.Bsize, nil
}

func splitEndpoint(s string) (string, int, error) {
	host, port, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return "", 0, fmt.Errorf("no port")
	}
	n, err := strconv.Atoi(port)
	return host, n, err
}

func sizeGB(b int64) string {
	if b < 0 {
		b = 0
	}
	return fmt.Sprintf("%.0f GB", float64(b)/(1<<30))
}

// LeftoverGuest is data a removed guest left here.
type LeftoverGuest struct {
	User string
	Size int64
}

// LeftoverGuests lists data of guests no longer storing here.
func (m *Manager) LeftoverGuests(ctx context.Context) ([]LeftoverGuest, error) {
	if m.GuestDirs == nil || m.GuestRecords == nil {
		return nil, nil
	}
	users, err := m.GuestDirs()
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
		if m.GuestUsage != nil {
			if size, err := m.GuestUsage(u); err == nil {
				l.Size = size
			}
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
	if !slices.ContainsFunc(left, func(l LeftoverGuest) bool { return l.User == user }) || m.RemoveGuestDir == nil {
		return ErrNotFound
	}
	return m.RemoveGuestDir(ctx, user)
}
