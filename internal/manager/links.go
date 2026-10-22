package manager

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/trust"
	"roostack.cloud/roostack/internal/usererr"
)

// Direct links between servers (SPEC.md §10.10): servers dial only the
// controller, so a server backing up to another server's disk would send
// everything through the controller (twice over its connection). When one
// of the two can be reached by the other, they get a WireGuard link of
// their own. Where a server can be reached: an address the Owner typed;
// its home-network address, when both are at home; or the address it
// connects to the controller from, when it's somewhere else (a server on
// the internet). A link that doesn't connect within linkGrace is dropped
// for linkRetry, and the backups go through the controller meanwhile (it
// forwards the store port only).

// KV is the controller's settings (store.Store).
type KV interface {
	Setting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

// Settings keys.
const (
	// LocationKey + server ID: where it is, LocationHome or LocationAway
	// (asked on the backup plan and Settings → Servers).
	LocationKey   = "server-location."
	LocationHome  = "home"
	LocationAway  = "away"
	linkAddrKey   = "server-link-address."
	linkFailedKey = "server-link-failed."
)

// How long a new direct link has to connect, and how long one that didn't
// waits before it's tried again.
var (
	linkGrace = 3 * time.Minute
	linkRetry = 24 * time.Hour
)

// linkFresh is how recent a link's last handshake must be for it to count
// as up: WireGuard renews it every two minutes while there's traffic (and
// the keepalive makes some).
const linkFresh = 5 * time.Minute

// Link paths, for people.
const (
	PathDirect = "direct" // a link of their own
	PathHub    = "hub"    // through the controller
)

// linkInfo is what the controller knows about a server for its links.
type linkInfo struct {
	labnet.Peer
	// Home are its home-network addresses (as it reported them).
	Home []string
}

// linkState is the controller's view of direct links (not saved: a new
// one gets linkGrace again after a restart).
type linkState struct {
	mu sync.Mutex
	// since is when each pair's link was first handed out.
	since map[string]time.Time
}

// pairKey names a pair of servers, in either order.
func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// linkPairs are the pairs of servers (neither the controller, which every
// server dials anyway) where one backs up to the other's disk.
func (m *Manager) linkPairs(ctx context.Context) [][2]string {
	if m.BackupStore == nil {
		return nil
	}
	targets, _ := m.BackupStore.BackupTargets(ctx)
	copies, _ := m.BackupStore.BackupCopies(ctx)
	seen := map[string]bool{}
	var out [][2]string
	for _, t := range append(targets, copies...) {
		kind, store, _ := strings.Cut(t.Provider, ":")
		if kind != "server" {
			continue
		}
		from := t.ServerID
		if from == store || from == m.Lab.NodeID || store == m.Lab.NodeID || from == "" || store == "" {
			continue
		}
		if k := pairKey(from, store); !seen[k] {
			seen[k] = true
			out = append(out, [2]string{from, store})
		}
	}
	return out
}

// linkInfos are the lab's other servers' keys, addresses and home
// addresses.
func (m *Manager) linkInfos(ctx context.Context) map[string]linkInfo {
	reg, ok := m.Registry.(interface {
		Peers(ctx context.Context) (map[string]labnet.Peer, error)
	})
	if !ok {
		return nil
	}
	peers, err := reg.Peers(ctx)
	if err != nil {
		return nil
	}
	out := map[string]linkInfo{}
	for id, p := range peers {
		info := linkInfo{Peer: p}
		if l := node.Capability(m.node(id), node.Node.Linked); l != nil {
			info.Home, _ = l.LinkStatus(ctx)
		}
		out[id] = info
	}
	return out
}

// setting reads a controller setting ("" if none).
func (m *Manager) setting(ctx context.Context, key string) string {
	if m.Settings == nil {
		return ""
	}
	v, _ := m.Settings.Setting(ctx, key)
	return v
}

// reachAt is where server from can dial server to directly ("" if
// nowhere known).
func (m *Manager) reachAt(ctx context.Context, infos map[string]linkInfo, from, to string) string {
	if a := m.setting(ctx, linkAddrKey+to); a != "" {
		return a
	}
	port := strconv.Itoa(agent.WireGuardPort)
	locFrom, locTo := m.setting(ctx, LocationKey+from), m.setting(ctx, LocationKey+to)
	if locFrom == LocationHome && locTo == LocationHome {
		for _, a := range infos[to].Home {
			// Never an address the other has too (VMs behind the same NAT
			// all have the same one).
			if !slices.Contains(infos[from].Home, a) {
				return net.JoinHostPort(a, port)
			}
		}
	}
	if locTo == LocationAway {
		if ap, err := netip.ParseAddrPort(infos[to].Seen); err == nil && !trust.IsHome(ap.Addr()) {
			return net.JoinHostPort(ap.Addr().Unmap().String(), port)
		}
	}
	return ""
}

// linkFailed reports whether a pair's link didn't connect lately.
func (m *Manager) linkFailed(ctx context.Context, a, b string) bool {
	v := m.setting(ctx, linkFailedKey+pairKey(a, b))
	t, err := strconv.ParseInt(v, 10, 64)
	return err == nil && time.Since(time.Unix(t, 0)) < linkRetry
}

// linksFor is a server's direct links, for its assignment: nil for the
// controller (it leaves its links alone), otherwise every link it should
// have (none removes them).
func (m *Manager) linksFor(ctx context.Context, infos map[string]linkInfo, id string) *agent.Links {
	if id == m.Lab.NodeID || infos == nil {
		return nil
	}
	// The controller dials it (it can be reached and the controller
	// can't, SPEC.md §10.2), so it keeps listening.
	l := &agent.Links{Listen: infos[id].Dial != ""}
	for _, p := range m.linkPairs(ctx) {
		other := p[1]
		if p[1] == id {
			other = p[0]
		} else if p[0] != id {
			continue
		}
		if m.linkFailed(ctx, id, other) || infos[other].PublicKey == "" {
			continue
		}
		dial, dialled := m.reachAt(ctx, infos, id, other), m.reachAt(ctx, infos, other, id)
		if dial == "" && dialled == "" {
			continue // neither can reach the other: through the controller
		}
		peer := agent.Peer{PublicKey: infos[other].PublicKey, AllowedIPs: []string{infos[other].Addr + "/32"}}
		if dial != "" {
			peer.Endpoint, peer.Keepalive = dial, labnet.Keepalive
		}
		if dialled != "" {
			l.Listen = true
		}
		l.Peers = append(l.Peers, peer)
		m.links.mu.Lock()
		if m.links.since == nil {
			m.links.since = map[string]time.Time{}
		}
		if _, ok := m.links.since[pairKey(id, other)]; !ok {
			m.links.since[pairKey(id, other)] = time.Now()
		}
		m.links.mu.Unlock()
	}
	return l
}

// LinkPath says how server from reaches server to's backup store: directly
// or through the controller ("" when one of them is the controller, which
// every server reaches directly).
func (m *Manager) LinkPath(ctx context.Context, from, to string) string {
	if from == to || from == m.Lab.NodeID || to == m.Lab.NodeID {
		return ""
	}
	infos := m.linkInfos(ctx)
	for _, peer := range m.linksFor(ctx, infos, from).Peers {
		if peer.PublicKey == infos[to].PublicKey {
			return PathDirect
		}
	}
	return PathHub
}

// LinkFailed reports whether a direct link between two servers was tried
// lately and didn't connect (it's tried again after linkRetry, or when an
// address changes).
func (m *Manager) LinkFailed(ctx context.Context, a, b string) bool { return m.linkFailed(ctx, a, b) }

// CheckLinks drops direct links that haven't connected within linkGrace
// (their backups go through the controller), for linkRetry. Run every
// minute or so.
func (m *Manager) CheckLinks(ctx context.Context) {
	infos := m.linkInfos(ctx)
	if infos == nil {
		return
	}
	var failed []string
	for _, p := range m.linkPairs(ctx) {
		key := pairKey(p[0], p[1])
		m.links.mu.Lock()
		since, ok := m.links.since[key]
		m.links.mu.Unlock()
		if !ok || time.Since(since) < linkGrace || m.linkFailed(ctx, p[0], p[1]) {
			continue
		}
		// Either side seeing a recent handshake is enough.
		up := false
		for _, side := range [][2]string{{p[0], p[1]}, {p[1], p[0]}} {
			if l := node.Capability(m.node(side[0]), node.Node.Linked); l != nil {
				_, hs := l.LinkStatus(ctx)
				if t := hs[infos[side[1]].PublicKey]; t > 0 && time.Since(time.Unix(t, 0)) < linkFresh {
					up = true
				}
			}
		}
		if up {
			continue
		}
		if m.Settings != nil {
			m.Settings.SetSetting(ctx, linkFailedKey+key, strconv.FormatInt(time.Now().Unix(), 10))
		}
		m.links.mu.Lock()
		delete(m.links.since, key)
		m.links.mu.Unlock()
		failed = append(failed, fmt.Sprintf("%s and %s", m.nameOf(p[0]), m.nameOf(p[1])))
	}
	if len(failed) > 0 {
		m.Log.Warn("direct links didn't connect; backups between them go through the controller", "servers", strings.Join(failed, "; "))
		m.ApplyBecause(strings.Join(failed, "; ") + " couldn't link directly: their backups go through the controller")
	}
}

// LinkAddress is where other servers reach a server directly, as the
// Owner typed it ("" if not set).
func (m *Manager) LinkAddress(ctx context.Context, id string) string {
	return m.setting(ctx, linkAddrKey+id)
}

// SetLinkAddress sets (or clears, with "") where other servers reach a
// server directly: for a server Roostack can't work that out for (e.g.
// behind a router forwarding a port). Its links are tried again.
func (m *Manager) SetLinkAddress(ctx context.Context, id, addr string) error {
	if m.Settings == nil || id == m.Lab.NodeID {
		return ErrNotFound
	}
	addr = strings.TrimSpace(addr)
	if addr != "" {
		host, port, err := net.SplitHostPort(addr)
		if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 1 || n > 65535 || host == "" ||
			strings.ContainsAny(host, " /") {
			return usererr.New("Enter the address as host:port, e.g. 192.168.1.20:%d.", agent.WireGuardPort)
		}
	}
	if err := m.Settings.SetSetting(ctx, linkAddrKey+id, addr); err != nil {
		return err
	}
	// A new address gets a new chance.
	for _, p := range m.linkPairs(ctx) {
		if p[0] == id || p[1] == id {
			m.Settings.SetSetting(ctx, linkFailedKey+pairKey(p[0], p[1]), "")
		}
	}
	m.ApplyBecause("A server's address for direct links changed")
	return nil
}
