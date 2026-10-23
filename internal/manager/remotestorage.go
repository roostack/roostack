package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/destination"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
)

// Storage at other labs (SPEC.md §10.10): this lab stores its own backups
// at another lab that offers it storage. Each such lab is a backup
// provider ("remote"), which the controller reaches over a link of its own
// and forwards its servers' backups to.

// LinkAgent is roostack-agent's WireGuard operations for links between labs.
type LinkAgent interface {
	ApplyWireGuard(ctx context.Context, wg agent.WireGuard) (string, error)
	WireGuardStatus(ctx context.Context, iface string) (agent.WireGuardStatus, error)
	RemoveWireGuard(ctx context.Context, iface string) error
}

// FirstProxyPort is where the controller forwards its servers' backups
// to the first other lab it stores at (the next one +1, …).
const FirstProxyPort = 8200

// RemoteStorage is this lab's storage at another lab: a provider.
type RemoteStorage struct {
	HostName string     `json:"host_name"`
	Iface    string     `json:"iface"`
	HostAddr netip.Addr `json:"host_addr"`
	// User is this lab's name at the host, with AdminPassword its access
	// that can delete (the controller's, for pruning).
	User          string `json:"user"`
	AdminPassword string `json:"admin_password"`
	// Servers are this lab's servers' own store passwords there, by
	// server ID (each is its own store user: remoteServerUser).
	Servers map[string]string `json:"servers,omitempty"`
	// StorePassword is the store password every server shared, in links
	// made before servers had their own (2026-10-17): backups set up then
	// keep it.
	StorePassword string `json:"store_password,omitempty"`
	Port          int    `json:"port"`
	Quota         int64  `json:"quota"`
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
		return usererr.New("%s", err)
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
		return usererr.New("This lab already stores at 9 other labs.")
	}
	iface := "roostack-st" + strconv.Itoa(n)
	if err := labnet.JoinStorage(ctx, inv, iface, m.LinkAgent); err != nil {
		m.LinkAgent.RemoveWireGuard(ctx, iface)
		return usererr.New("%s", err)
	}
	r := RemoteStorage{HostName: inv.HostName, Iface: iface, HostAddr: inv.HostAddr, User: inv.User,
		StorePassword: inv.StorePassword, AdminPassword: inv.AdminPassword, Port: inv.Port, Quota: inv.Quota,
		ProxyPort: FirstProxyPort + n - 1}
	// Each server gets its own user there when its backups are set up
	// (remoteServerUser).
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
			r.Ref = destination.OfProvider(p.ID)
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
	p, err := m.provider(ctx, destination.Parse(r.Ref).ID)
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
// server reaches it (through the controller's proxy, as its own user
// there: remoteServerUser) and as the controller does (directly, with the
// lab's admin access there).
func (m *Manager) remoteRepos(r RemoteStorage, labID, serverID string) (serverURL string, serverEnv []string, adminURL string, adminEnv []string) {
	// rest-server takes at most two levels, and a user reaches only the
	// first level named after it (--private-repos), as on a server's
	// disk.
	path := "/" + serverID + "/" + labID
	host := m.addressOf(m.Lab.NodeID)
	if serverID == m.Lab.NodeID {
		host = "127.0.0.1"
	}
	serverURL = fmt.Sprintf("rest:http://%s:%d%s", host, r.ProxyPort, path)
	serverEnv = []string{"RESTIC_REST_USERNAME=" + serverID, "RESTIC_REST_PASSWORD=" + r.Servers[serverID]}
	adminURL, adminEnv = remoteAdmin(r, serverURL)
	return
}

// remoteAdmin is the controller's access to a repository at another lab,
// from the server's URL for it (through the proxy): the same path on the
// host's admin port. Backups set up before servers had their own users
// are at /<lab's user>/<server ID>, and keep working.
func remoteAdmin(r RemoteStorage, serverURL string) (string, []string) {
	path := serverURL
	if u, err := url.Parse(strings.TrimPrefix(serverURL, "rest:")); err == nil {
		path = u.Path
	}
	return fmt.Sprintf("rest:http://%s:%d%s", r.HostAddr, r.Port+1, path),
		[]string{"RESTIC_REST_USERNAME=" + r.User, "RESTIC_REST_PASSWORD=" + r.AdminPassword}
}

// remoteServerUser makes sure a server has its own user at another lab's
// store (SPEC.md §10.10, review M21), so a server that's taken over can't
// add to its siblings' backups there or read them: a password kept in the
// lab's record, whose hash the host is given over the guest API (again
// each time, in case it lost it). It returns the record with it.
func (m *Manager) remoteServerUser(ctx context.Context, ref, serverID string) (RemoteStorage, error) {
	m.remoteConfigMu.Lock()
	defer m.remoteConfigMu.Unlock()
	d, err := m.destinationOf(ctx, ref)
	if err != nil {
		return RemoteStorage{}, err
	}
	r := d.remote
	r.Ref = ref
	pw := r.Servers[serverID]
	var hash string
	if pw == "" {
		pw, hash, err = labnet.NewPassword()
	} else {
		hash, err = labnet.HashPassword(pw)
	}
	if err != nil {
		return r, err
	}
	if err := labnet.SetGuestUser(ctx, r.HostAddr, r.User, r.AdminPassword, serverID, hash); err != nil {
		return r, usererr.New("%s didn't take a user for %s: %v. Check their lab is running and try again.", r.HostName, m.nameOf(serverID), err)
	}
	if r.Servers[serverID] == pw {
		return r, nil
	}
	if r.Servers == nil {
		r.Servers = map[string]string{}
	}
	r.Servers[serverID] = pw
	cfg, _ := json.Marshal(r)
	p := d.provider
	p.Config = string(cfg)
	return r, m.BackupStore.UpdateBackupProvider(ctx, p)
}

// removeRemoteUsers removes a server's users at other labs (it's leaving
// the lab), and forgets their passwords.
func (m *Manager) removeRemoteUsers(ctx context.Context, act *activity.Log, serverID string) {
	if m.BackupStore == nil {
		return
	}
	m.remoteConfigMu.Lock()
	defer m.remoteConfigMu.Unlock()
	for _, r := range m.RemoteStorages(ctx) {
		if r.Servers[serverID] == "" {
			continue
		}
		if err := labnet.RemoveGuestUser(ctx, r.HostAddr, r.User, r.AdminPassword, serverID); err != nil {
			act.Printf("Couldn't remove %s's user at %s (%v): it can't reach it without this lab's network anyway.",
				m.nameOf(serverID), r.HostName, err)
			m.Log.Warn("removing a server's user at another lab", "server", serverID, "host", r.HostName, "err", err)
		} else {
			act.Printf("Removed %s's user at %s.", m.nameOf(serverID), r.HostName)
		}
		p, err := m.provider(ctx, destination.Parse(r.Ref).ID)
		if err != nil {
			continue
		}
		delete(r.Servers, serverID)
		cfg, _ := json.Marshal(r)
		p.Config = string(cfg)
		if err := m.BackupStore.UpdateBackupProvider(ctx, p); err != nil {
			m.Log.Warn("forgetting a removed server's password at another lab", "err", err)
		}
	}
}
