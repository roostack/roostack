package labnet

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/store"
)

// Storage for other labs (SPEC.md §10.10): a lab offers space for another
// lab's backups over its own WireGuard interface (roostack-guests), which
// reaches only the guests' backup stores. The guest lab links to it with
// an invite, much like a server joins with a code.

// Guest link ports on the host's address on the guests' link.
const (
	// GuestAPIPort serves joining and usage to guests.
	GuestAPIPort = 8472
	// FirstGuestPort is the first guest's store; its admin access is the
	// next port, and each guest takes two.
	FirstGuestPort = 8100
)

// InviteLifetime is how long a storage invite works.
const InviteLifetime = 24 * time.Hour

const invitePrefix = "roostack-storage-"

// GuestStore is the host's record of guests.
type GuestStore interface {
	AddGuest(ctx context.Context, g store.Guest) (int64, error)
	Guests(ctx context.Context, now time.Time) ([]store.Guest, error)
	JoinGuest(ctx context.Context, tokenHash, publicKey string, now time.Time) (store.Guest, error)
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// GuestHub is the host's side: the guests' WireGuard interface and their
// invites.
type GuestHub struct {
	LabName string
	Store   GuestStore
	Agent   WireGuardAgent
	Log     *slog.Logger
	Now     func() time.Time
	// Usage reports how much a guest stores, in bytes (optional).
	Usage func(ctx context.Context, g store.Guest) (int64, error)
	// Changed is called when guests change, so the host's stores follow
	// (optional).
	Changed func()

	mu        sync.Mutex
	publicKey string
}

// StorageInvite is what another lab needs to store backups here.
type StorageInvite struct {
	// Endpoint is where the host's guests interface listens (host:port).
	Endpoint string `json:"e"`
	// HostKey is the host's WireGuard public key on the guests' link, and
	// HostAddr its address there.
	HostKey  string     `json:"k"`
	HostAddr netip.Addr `json:"h"`
	// Addr is the guest's address on the link.
	Addr netip.Addr `json:"a"`
	// JoinKey is a one-time WireGuard private key, replaced on joining.
	JoinKey string `json:"j"`
	Token   string `json:"t"`
	// HostName names the host lab to the guest.
	HostName string `json:"n"`
	// The guest's store: its user and passwords, and ports.
	User          string `json:"u"`
	StorePassword string `json:"sp"`
	AdminPassword string `json:"ap"`
	Port          int    `json:"p"`
	Quota         int64  `json:"q"`
}

// String encodes the invite for pasting.
func (c StorageInvite) String() string {
	data, _ := json.Marshal(c)
	return invitePrefix + base64.RawURLEncoding.EncodeToString(data)
}

// ParseStorageInvite decodes and checks a pasted invite.
func ParseStorageInvite(s string) (StorageInvite, error) {
	var c StorageInvite
	raw, ok := strings.CutPrefix(strings.TrimSpace(s), invitePrefix)
	if !ok {
		return c, errors.New("that isn't a Roostack storage invite (it should start with " + invitePrefix + ")")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, errors.New("the invite is incomplete; copy all of it")
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, errors.New("the invite is damaged")
	}
	if err := c.link("roostack-st1", c.JoinKey).Validate(); err != nil {
		return c, fmt.Errorf("the invite is damaged: %w", err)
	}
	if c.Token == "" || c.User == "" || c.StorePassword == "" || c.AdminPassword == "" || c.Port < 1024 || c.Port > 65000 {
		return c, errors.New("the invite is damaged")
	}
	return c, nil
}

// link is the guest's WireGuard interface for this invite.
func (c StorageInvite) link(iface, key string) agent.WireGuard {
	return agent.WireGuard{Interface: iface, Address: c.Addr.String() + "/32", JoinKey: key,
		Peers: []agent.Peer{{PublicKey: c.HostKey, AllowedIPs: []string{c.HostAddr.String() + "/32"},
			Endpoint: c.Endpoint, Keepalive: Keepalive}}}
}

// subnet is the host's /24 on the guests' link, picked at random in
// agent.GuestRange the first time, so a lab with links to several hosts
// can tell them apart.
func (h *GuestHub) subnet(ctx context.Context) (netip.Prefix, error) {
	v, err := h.Store.Setting(ctx, "guests.subnet")
	if err != nil {
		return netip.Prefix{}, err
	}
	if v != "" {
		return netip.ParsePrefix(v)
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1<<(24-agent.GuestRange.Bits())))
	if err != nil {
		return netip.Prefix{}, err
	}
	base := agent.GuestRange.Addr().As4()
	third := int(base[2]) + int(n.Int64())
	base[1] += byte(third / 256)
	base[2] = byte(third % 256)
	p := netip.PrefixFrom(netip.AddrFrom4(base), 24)
	return p, h.Store.SetSetting(ctx, "guests.subnet", p.String())
}

// Enabled reports whether this lab offers storage to others yet.
func (h *GuestHub) Enabled(ctx context.Context) bool {
	v, err := h.Store.Setting(ctx, "guests.subnet")
	return err == nil && v != ""
}

// HostAddr is this lab's address on the guests' link.
func (h *GuestHub) HostAddr(ctx context.Context) (netip.Addr, error) {
	p, err := h.subnet(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	return p.Addr().Next(), nil
}

// SyncPeers configures the guests' interface: one peer per guest.
func (h *GuestHub) SyncPeers(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, err := h.subnet(ctx)
	if err != nil {
		return err
	}
	guests, err := h.Store.Guests(ctx, h.Now())
	if err != nil {
		return err
	}
	wg := agent.WireGuard{Interface: agent.GuestsInterface, Address: fmt.Sprintf("%s/24", p.Addr().Next()),
		ListenPort: agent.GuestsPort}
	for _, g := range guests {
		wg.Peers = append(wg.Peers, agent.Peer{PublicKey: g.PublicKey, AllowedIPs: []string{g.Addr + "/32"}})
	}
	pub, err := h.Agent.ApplyWireGuard(ctx, wg)
	if err != nil {
		return fmt.Errorf("setting up storage for others: %w", err)
	}
	h.publicKey = pub
	return nil
}

// Start brings up the guests' interface if storage is offered, and keeps
// it in sync (expired invites stop working).
func (h *GuestHub) Start(ctx context.Context) {
	go func() {
		for {
			wait := peerSync
			if h.Enabled(ctx) {
				if err := h.SyncPeers(ctx); err != nil {
					h.Log.Error("syncing storage for others", "err", err)
					wait = 10 * time.Second
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}()
}

// NewInvite makes an invite for another lab to store up to quota bytes
// here. endpoint is this server's address and guests port as the other
// lab sees it.
func (h *GuestHub) NewInvite(ctx context.Context, name string, quota int64, endpoint string) (StorageInvite, error) {
	p, err := h.subnet(ctx)
	if err != nil {
		return StorageInvite{}, err
	}
	guests, err := h.Store.Guests(ctx, h.Now())
	if err != nil {
		return StorageInvite{}, err
	}
	usedAddr, usedPort := map[string]bool{}, map[int]bool{}
	for _, g := range guests {
		usedAddr[g.Addr], usedPort[g.Port] = true, true
	}
	addr := netip.Addr{}
	for a := p.Addr().Next().Next(); p.Contains(a); a = a.Next() {
		if !usedAddr[a.String()] && a.As4()[3] != 255 {
			addr = a
			break
		}
	}
	port := FirstGuestPort
	for usedPort[port] {
		port += 2
	}
	if !addr.IsValid() {
		return StorageInvite{}, errors.New("no room for more guests")
	}
	key, err := agent.NewWireGuardKey()
	if err != nil {
		return StorageInvite{}, err
	}
	pub, _ := agent.WireGuardPublicKey(key)
	token, tokenHash := NewToken()
	storePW, storeHash, err := password()
	if err != nil {
		return StorageInvite{}, err
	}
	adminPW, adminHash, err := password()
	if err != nil {
		return StorageInvite{}, err
	}
	id, err := h.Store.AddGuest(ctx, store.Guest{Name: name, Quota: quota, Addr: addr.String(), PublicKey: pub,
		TokenHash: tokenHash, Expires: h.Now().Add(InviteLifetime), Port: port, StoreHash: storeHash, AdminHash: adminHash})
	if err != nil {
		return StorageInvite{}, err
	}
	if err := h.SyncPeers(ctx); err != nil {
		return StorageInvite{}, err
	}
	if h.Changed != nil {
		h.Changed()
	}
	h.mu.Lock()
	hostKey := h.publicKey
	h.mu.Unlock()
	inv := StorageInvite{Endpoint: endpoint, HostKey: hostKey, HostAddr: p.Addr().Next(), Addr: addr, JoinKey: key,
		Token: token, HostName: h.LabName, User: store.Guest{ID: id}.User(), StorePassword: storePW,
		AdminPassword: adminPW, Port: port, Quota: quota}
	if _, err := ParseStorageInvite(inv.String()); err != nil {
		return StorageInvite{}, err
	}
	return inv, nil
}

// GuestUsage is what a guest stores at the host.
type GuestUsage struct {
	Used  int64 `json:"used"`
	Quota int64 `json:"quota"`
	// Host is the host lab's name now (it may have been renamed).
	Host string `json:"host,omitempty"`
	// DeleteOn is set once the host has stopped storing the guest's
	// backups: what's there is deleted then.
	DeleteOn time.Time `json:"delete_on,omitzero"`
}

// GuestGrace is how long a removed guest lab's backups stay, so it hears
// about it (its link stays up to say so) and can move them elsewhere.
const GuestGrace = 30 * 24 * time.Hour

// Handler is the API guests reach over the guests' link.
func (h *GuestHub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/guest/join", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token     string `json:"token"`
			PublicKey string `json:"public_key"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		if raw, err := base64.StdEncoding.DecodeString(req.PublicKey); err != nil || len(raw) != 32 {
			WriteError(w, http.StatusBadRequest, errors.New("invalid WireGuard key"))
			return
		}
		g, err := h.Store.JoinGuest(r.Context(), HashToken(req.Token), req.PublicKey, h.Now())
		if err != nil {
			WriteError(w, http.StatusForbidden, err)
			return
		}
		// The reply goes over the one-time key; switch keys after it.
		go func() {
			time.Sleep(2 * time.Second)
			if err := h.SyncPeers(context.Background()); err != nil {
				h.Log.Error("adding a guest", "guest", g.Name, "err", err)
			}
		}()
		h.Log.Info("guest joined", "guest", g.Name)
		WriteJSON(w, http.StatusOK, map[string]string{"host": h.LabName})
	})
	mux.HandleFunc("GET /v1/guest/usage", func(w http.ResponseWriter, r *http.Request) {
		g, ok := h.guest(r)
		if !ok {
			WriteError(w, http.StatusForbidden, errors.New("not a guest here"))
			return
		}
		u := GuestUsage{Used: -1, Quota: g.Quota, Host: h.LabName}
		if !g.Removed.IsZero() {
			u.DeleteOn = g.Removed.Add(GuestGrace)
		}
		if h.Usage != nil {
			if used, err := h.Usage(r.Context(), g); err == nil {
				u.Used = used
			}
		}
		WriteJSON(w, http.StatusOK, u)
	})
	return mux
}

// guest authenticates a guest by its admin password.
func (h *GuestHub) guest(r *http.Request) (store.Guest, bool) {
	user, pw, ok := r.BasicAuth()
	if !ok {
		return store.Guest{}, false
	}
	guests, err := h.Store.Guests(r.Context(), h.Now())
	if err != nil {
		return store.Guest{}, false
	}
	for _, g := range guests {
		if g.User() == user && g.Joined && bcrypt.CompareHashAndPassword([]byte(g.AdminHash), []byte(pw)) == nil {
			return g, true
		}
	}
	return store.Guest{}, false
}

// ServeGuests runs the guest API on this lab's address on the guests'
// link until ctx ends (it waits for the interface to come up).
func (h *GuestHub) ServeGuests(ctx context.Context) {
	var ln net.Listener
	for {
		if h.Enabled(ctx) {
			if addr, err := h.HostAddr(ctx); err == nil {
				if ln, err = net.Listen("tcp", net.JoinHostPort(addr.String(), strconv.Itoa(GuestAPIPort))); err == nil {
					break
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
	srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	h.Log.Info("guest API listening", "addr", ln.Addr())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		h.Log.Error("guest API", "err", err)
	}
}

// Joining waits up to handshakeTimeout for the WireGuard link to come up,
// and joinTimeout in all for the host to answer.
var (
	handshakeTimeout = 20 * time.Second
	joinTimeout      = 45 * time.Second
)

// linkUp reports whether iface has had a WireGuard handshake with peer,
// if the agent can say (true if it can't, so joining waits as before).
func linkUp(ctx context.Context, ag WireGuardAgent, iface, peer string) bool {
	sa, ok := ag.(interface {
		WireGuardStatus(ctx context.Context, iface string) (agent.WireGuardStatus, error)
	})
	if !ok {
		return true
	}
	st, err := sa.WireGuardStatus(ctx, iface)
	return err != nil || st.Handshakes[peer] > 0
}

// JoinStorage links this lab to another lab's storage with an invite,
// over WireGuard interface iface (roostack-st1…9): up with the invite's
// one-time key, register this lab's own key, then switch to it.
func JoinStorage(ctx context.Context, inv StorageInvite, iface string, ag WireGuardAgent) error {
	ownKey, err := agent.NewWireGuardKey()
	if err != nil {
		return err
	}
	ownPub, _ := agent.WireGuardPublicKey(ownKey)
	if _, err := ag.ApplyWireGuard(ctx, inv.link(iface, inv.JoinKey)); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"token": inv.Token, "public_key": ownPub})
	url := "http://" + net.JoinHostPort(inv.HostAddr.String(), strconv.Itoa(GuestAPIPort)) + "/v1/guest/join"
	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	handshake := false
	for {
		err = post(ctx, client, url, body, nil)
		var refused *RemoteError
		if err == nil || errors.As(err, &refused) || time.Since(start) > joinTimeout {
			break
		}
		// No WireGuard handshake at all after a while: the other lab's
		// UDP port can't be reached, and waiting longer won't help.
		if !handshake {
			handshake = linkUp(ctx, ag, iface, inv.HostKey)
			if !handshake && time.Since(start) > handshakeTimeout {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	var refused *RemoteError
	if err != nil && !errors.As(err, &refused) {
		_, port, _ := net.SplitHostPort(inv.Endpoint)
		if !handshake && !linkUp(ctx, ag, iface, inv.HostKey) {
			return fmt.Errorf("%s didn't answer at %s. Its UDP port %s has to let other labs in: on a cloud server, in the provider's "+
				"firewall (such as Linode's Cloud Firewall); at home, by forwarding the port on the router. Their Storage for others "+
				"page explains how. (Details: no WireGuard handshake in %d seconds.)",
				inv.HostName, inv.Endpoint, port, int(time.Since(start).Seconds()))
		}
		return fmt.Errorf("the link to %s at %s works, but Roostack there didn't answer: it may be restarting, so try again in a minute. "+
			"(Details: %v)", inv.HostName, inv.Endpoint, err)
	}
	if err != nil {
		return fmt.Errorf("%s refused the invite: %w", inv.HostName, err)
	}
	_, err = ag.ApplyWireGuard(ctx, inv.link(iface, ownKey))
	return err
}

// Usage asks the host how much this lab stores there.
func Usage(ctx context.Context, hostAddr netip.Addr, user, adminPassword string) (GuestUsage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://"+net.JoinHostPort(hostAddr.String(), strconv.Itoa(GuestAPIPort))+"/v1/guest/usage", nil)
	if err != nil {
		return GuestUsage{}, err
	}
	req.SetBasicAuth(user, adminPassword)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return GuestUsage{}, err
	}
	defer resp.Body.Close()
	var u GuestUsage
	return u, DecodeResponse(resp, &u)
}

// password returns a random password and its bcrypt hash.
func password() (string, string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	pw := hex.EncodeToString(b)
	hash, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return pw, string(hash), err
}
