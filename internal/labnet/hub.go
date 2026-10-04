package labnet

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/store"
)

// JoinCodeLifetime is how long a join code works.
const JoinCodeLifetime = time.Hour

// Keepalive keeps a server's WireGuard connection (and any NAT mapping
// on the way) open, so the controller can always reach it.
const Keepalive = 25

// HubStore is the controller's record of servers and join codes.
type HubStore interface {
	AddJoinCode(ctx context.Context, c store.JoinCode) error
	JoinCodes(ctx context.Context, now time.Time) ([]store.JoinCode, error)
	Join(ctx context.Context, tokenHash string, m store.Server, now time.Time) (store.Server, error)
	Servers(ctx context.Context) ([]store.Server, error)
	RemoveServer(ctx context.Context, id string) error
	RenameServer(ctx context.Context, id, name string) error
	SetServerCert(ctx context.Context, id, serial, prev string, expires time.Time) error
}

// WireGuardAgent is roostack-agent's WireGuard operation.
type WireGuardAgent interface {
	ApplyWireGuard(ctx context.Context, wg agent.WireGuard) (string, error)
}

// Hub is the controller's side of Roostack's network: the other servers
// dial it over WireGuard, and it lets them join.
type Hub struct {
	StateDir string
	LabName  string
	Self     nodeid.Identity
	Store    HubStore
	Agent    WireGuardAgent
	Log      *slog.Logger
	Now      func() time.Time
	// Started is called once the network is first set up (Roostack restarts
	// to pick up its new role, e.g. for the firewall). Optional.
	Started func()
	// UI is where the lab's pages are (https://roost.<domain>), put in
	// join codes. Optional.
	UI string
	// Joined is called when a server joins (to audit it: a security email,
	// SPEC.md §6.13). Optional.
	Joined func(ctx context.Context, name string)
	// SwitchDelay is how long after a join the server's own key replaces
	// the one-time key (0: 2s).
	SwitchDelay time.Duration

	mu        sync.Mutex
	publicKey string
}

// Enabled reports whether the lab has a network yet.
func (h *Hub) Enabled() bool {
	m, ok, err := LoadMembership(h.StateDir)
	return err == nil && ok && m.Role == config.RoleController
}

// Start brings up the hub's WireGuard interface if the lab has a network
// (e.g. after a restart; wg-quick also starts it at boot). It retries in
// the background while roostack-agent isn't reachable yet, then re-syncs now
// and then, so an unused join code's one-time key stops working when the
// code expires.
func (h *Hub) Start(ctx context.Context) {
	go func() {
		wait := 3 * time.Second
		for attempt := 1; ; attempt++ {
			if h.Enabled() {
				if err := h.SyncPeers(ctx); err != nil {
					if attempt == 1 || attempt%10 == 0 {
						h.Log.Error("syncing Roostack's network", "err", err)
					}
					wait = 3 * time.Second
				} else {
					wait = peerSync
				}
			} else {
				wait = peerSync
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}

// peerSync is how often the hub re-syncs its WireGuard peers.
const peerSync = 5 * time.Minute

// init sets up the network on first use: the CA, the controller's own
// certificate, its membership and its WireGuard interface.
func (h *Hub) init(ctx context.Context) error {
	if h.Enabled() {
		return nil
	}
	ca, err := LoadOrCreateCA(CADir(h.StateDir), h.LabName)
	if err != nil {
		return err
	}
	id := Identity{Dir: IdentityDir(h.StateDir)}
	csr, err := id.CSR(h.Self.ID)
	if err != nil {
		return err
	}
	cert, _, err := ca.Issue(csr, h.Self.ID, ControllerAddr)
	if err != nil {
		return err
	}
	if err := id.Save(cert, CertPEM(ca.Cert)); err != nil {
		return err
	}
	m := Membership{Role: config.RoleController, Addr: ControllerAddr, PrefixBits: DefaultPrefix.Bits()}
	if err := h.SyncPeers(ctx); err != nil {
		return err
	}
	if err := m.Save(h.StateDir); err != nil {
		return err
	}
	if h.Started != nil {
		h.Started()
	}
	return nil
}

// SyncPeers configures the hub's WireGuard interface: every server, plus
// the one-time keys of join codes still waiting.
func (h *Hub) SyncPeers(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	servers, err := h.Store.Servers(ctx)
	if err != nil {
		return err
	}
	codes, err := h.Store.JoinCodes(ctx, h.Now())
	if err != nil {
		return err
	}
	// The hub forwards one server's backups to another's store (SPEC.md
	// §10.10); its firewall lets nothing else through.
	wg := agent.WireGuard{Address: fmt.Sprintf("%s/%d", ControllerAddr, DefaultPrefix.Bits()), ListenPort: agent.WireGuardPort,
		Forward: true}
	for _, m := range servers {
		wg.Peers = append(wg.Peers, agent.Peer{PublicKey: m.PublicKey, AllowedIPs: []string{m.Addr + "/32"}})
	}
	for _, c := range codes {
		wg.Peers = append(wg.Peers, agent.Peer{PublicKey: c.PublicKey, AllowedIPs: []string{c.Addr + "/32"}})
	}
	pub, err := h.Agent.ApplyWireGuard(ctx, wg)
	if err != nil {
		return fmt.Errorf("setting up Roostack's network: %w", err)
	}
	h.publicKey = pub
	return nil
}

// NewJoinCode makes a code for a new server, which dials the controller
// at endpoint (host:port of this server's WireGuard, as the new server
// sees it). It sets up the network first if needed.
func (h *Hub) NewJoinCode(ctx context.Context, endpoint string) (JoinCode, error) {
	if err := h.init(ctx); err != nil {
		return JoinCode{}, err
	}
	addr, err := h.freeAddr(ctx)
	if err != nil {
		return JoinCode{}, err
	}
	key, err := agent.NewWireGuardKey()
	if err != nil {
		return JoinCode{}, err
	}
	pub, _ := agent.WireGuardPublicKey(key)
	token, hash := NewToken()
	if err := h.Store.AddJoinCode(ctx, store.JoinCode{TokenHash: hash, Addr: addr.String(), PublicKey: pub,
		Expires: h.Now().Add(JoinCodeLifetime)}); err != nil {
		return JoinCode{}, err
	}
	if err := h.SyncPeers(ctx); err != nil {
		return JoinCode{}, err
	}
	ca, err := LoadOrCreateCA(CADir(h.StateDir), h.LabName)
	if err != nil {
		return JoinCode{}, err
	}
	h.mu.Lock()
	ctrlKey := h.publicKey
	h.mu.Unlock()
	c := JoinCode{Endpoint: endpoint, ControllerKey: ctrlKey, ControllerID: h.Self.ID, ControllerAddr: ControllerAddr,
		Addr: addr, PrefixBits: DefaultPrefix.Bits(), JoinKey: key, CA: ca.Fingerprint(), Token: token,
		LabName: h.LabName, UI: h.UI}
	// Check it the way the new server will (e.g. the endpoint's format).
	if _, err := ParseJoinCode(c.String()); err != nil {
		return JoinCode{}, err
	}
	return c, nil
}

// freeAddr returns the lowest address no server or code uses.
func (h *Hub) freeAddr(ctx context.Context) (netip.Addr, error) {
	used := map[netip.Addr]bool{ControllerAddr: true}
	servers, err := h.Store.Servers(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, m := range servers {
		a, _ := netip.ParseAddr(m.Addr)
		used[a] = true
	}
	codes, err := h.Store.JoinCodes(ctx, h.Now())
	if err != nil {
		return netip.Addr{}, err
	}
	for _, c := range codes {
		a, _ := netip.ParseAddr(c.Addr)
		used[a] = true
	}
	for a := ControllerAddr.Next(); DefaultPrefix.Contains(a); a = a.Next() {
		if !used[a] && a != DefaultPrefix.Masked().Addr() && a.As4()[3] != 255 {
			return a, nil
		}
	}
	return netip.Addr{}, errors.New("Roostack's network is full")
}

// Join registers a server that presented a join code, and signs its
// certificate. Its own WireGuard key replaces the code's one-time key.
func (h *Hub) Join(ctx context.Context, req JoinRequest) (JoinResponse, error) {
	if !nodeid.ValidID(req.NodeID) || req.NodeID == h.Self.ID {
		return JoinResponse{}, fmt.Errorf("invalid server ID %q", req.NodeID)
	}
	if raw, err := base64.StdEncoding.DecodeString(req.PublicKey); err != nil || len(raw) != 32 {
		return JoinResponse{}, errors.New("invalid WireGuard key")
	}
	name := req.Name
	if name == "" || len(name) > 63 {
		name = req.NodeID
	}
	m, err := h.Store.Join(ctx, HashToken(req.Token), store.Server{ID: req.NodeID, Name: name, PublicKey: req.PublicKey}, h.Now())
	if err != nil {
		return JoinResponse{}, err
	}
	ca, err := LoadOrCreateCA(CADir(h.StateDir), h.LabName)
	if err != nil {
		return JoinResponse{}, err
	}
	addr, _ := netip.ParseAddr(m.Addr)
	cert, c, err := ca.Issue(req.CSR, m.ID, addr)
	if err == nil {
		err = h.Store.SetServerCert(ctx, m.ID, Serial(c), "", c.NotAfter)
	}
	if err != nil {
		h.Store.RemoveServer(ctx, m.ID)
		return JoinResponse{}, fmt.Errorf("signing the certificate: %w", err)
	}
	// The reply still travels over the one-time key, so switch to the
	// server's own key only after it has gone; the server switches when
	// it has the reply, and its handshakes succeed from then on.
	go func() {
		time.Sleep(h.switchDelay())
		if err := h.SyncPeers(context.Background()); err != nil {
			h.Log.Error("adding a server to Roostack's network", "id", m.ID, "err", err)
		}
	}()
	h.Log.Info("server joined", "id", m.ID, "name", m.Name, "addr", m.Addr)
	if h.Joined != nil {
		h.Joined(ctx, m.Name)
	}
	return JoinResponse{Cert: cert, CA: CertPEM(ca.Cert)}, nil
}

func (h *Hub) switchDelay() time.Duration {
	if h.SwitchDelay != 0 {
		return h.SwitchDelay
	}
	return 2 * time.Second
}

// Remove forgets a server and drops its WireGuard peer, so it can no
// longer reach the lab's network.
func (h *Hub) Remove(ctx context.Context, id string) error {
	if err := h.Store.RemoveServer(ctx, id); err != nil {
		return err
	}
	h.Log.Info("server removed", "id", id)
	return h.SyncPeers(ctx)
}

// Rename changes a server's name in the lab.
func (h *Hub) Rename(ctx context.Context, id, name string) error {
	return h.Store.RenameServer(ctx, id, name)
}
