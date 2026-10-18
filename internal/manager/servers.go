package manager

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
)

// A lab's servers (SPEC.md §10): this one, the controller, which keeps
// desired state, and any others, which run the services placed on them.
// Each gets its own Assignment.

// RemoteServer is another server (node.Remote).
type RemoteServer interface {
	node.Node
	// Address is where the controller's Caddy reaches its web services.
	Address() string
	// Leave asks it to leave the lab; Rename tells it its new name.
	Leave(ctx context.Context) error
	Rename(ctx context.Context, name string) error
}

// ServerRegistry is the controller's record of the other servers
// (labnet.Hub).
type ServerRegistry interface {
	Remove(ctx context.Context, id string) error
	Rename(ctx context.Context, id, name string) error
	// CertExpiries is when each server's lab certificate expires.
	CertExpiries(ctx context.Context) map[string]time.Time
}

// Server is one server in the lab, for the UI.
type Server struct {
	ID, Name string
	// Controller holds the lab's desired state and serves the UI.
	Controller bool
	// This is the server serving this request.
	This bool
	// Reachable: it answers (this one always does).
	Reachable bool
	// Services are the names of the services placed on it; Waiting
	// those that were, and wait to be restored (it's gone).
	Services, Waiting []string
	Status            ApplyStatus
	// CertExpires is when its certificate on Roostack's network expires
	// (renewed automatically well before); zero if unknown.
	CertExpires time.Time
	// UnreachableSince is when it was first seen not answering, kept
	// across restarts; zero while it answers.
	UnreachableSince time.Time
}

// unreachableKey + server ID is when it was first seen not answering.
const unreachableKey = "server-unreachable-since."

// unreachableSince records when a server stopped answering the first
// time it's seen so, forgets it once it answers again, and says since
// when.
func (m *Manager) unreachableSince(ctx context.Context, id string, reachable bool) time.Time {
	if m.Settings == nil {
		return time.Time{}
	}
	v := m.setting(ctx, unreachableKey+id)
	if reachable {
		if v != "" {
			m.Settings.SetSetting(ctx, unreachableKey+id, "")
		}
		return time.Time{}
	}
	if t, err := strconv.ParseInt(v, 10, 64); err == nil {
		return time.Unix(t, 0)
	}
	now := time.Now()
	m.Settings.SetSetting(ctx, unreachableKey+id, strconv.FormatInt(now.Unix(), 10))
	return now
}

func (m *Manager) others() []RemoteServer {
	if m.Others == nil {
		return nil
	}
	return m.Others()
}

// node returns the server with a node ID, or nil.
func (m *Manager) node(id string) node.Node {
	if id == "" || id == m.Lab.NodeID {
		return m.Node
	}
	for _, r := range m.others() {
		if r.Identity().ID == id {
			return r
		}
	}
	return nil
}

// NodeByID returns a server by its node ID, or nil.
func (m *Manager) NodeByID(id string) node.Node { return m.node(id) }

// NodeFor returns the server a service runs on, or nil if it isn't
// installed (or its server has left the lab).
func (m *Manager) NodeFor(service string) node.Node {
	_, in, err := m.installedService(service)
	if err != nil {
		return nil
	}
	return m.node(in.Host)
}

// onNode returns the services placed on a server (not those waiting to
// be restored: they run nowhere).
func (m *Manager) onNode(services []state.Service, id string) []state.Service {
	var out []state.Service
	for _, s := range services {
		if s.Instance.Waiting {
			continue
		}
		// The controller also runs any with no host yet.
		if s.Instance.Host == id || (id == m.Lab.NodeID && s.Instance.Host == "") {
			out = append(out, s)
		}
	}
	return out
}

// waitingOn returns the services waiting to be restored that were on a
// server.
func waitingOn(services []state.Service, id string) []state.Service {
	var out []state.Service
	for _, s := range services {
		if s.Instance.Waiting && s.Instance.Host == id {
			out = append(out, s)
		}
	}
	return out
}

// proxied returns the services on other servers than id whose routes
// the controller's Caddy serves. Only the controller proxies.
func (m *Manager) proxied(services []state.Service, id string) []state.Proxied {
	var out []state.Proxied
	for _, r := range m.others() {
		if r.Identity().ID == id {
			continue
		}
		for _, s := range m.onNode(services, r.Identity().ID) {
			out = append(out, state.Proxied{Service: s, Addr: r.Address()})
		}
	}
	return out
}

// Apply hands every server its latest assignment (e.g. at startup).
func (m *Manager) Apply() { m.ApplyBecause("") }

// ApplyBecause starts an apply, saying why in the servers' logs ("" for
// a change to backups, storage or the lab network).
func (m *Manager) ApplyBecause(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.applyReason = reason
	m.startApplyLocked()
}

// startApplyLocked builds each server's assignment and hands it over;
// servers apply in the background.
func (m *Manager) startApplyLocked() {
	reason := cmp.Or(m.applyReason, "A change to backups, storage or the lab's servers")
	m.applyReason = ""
	services, err := state.LoadServices(m.Lab)
	if err == nil {
		err = m.allocateAndSave(services)
	}
	if err == nil {
		// Services the registry withdrew as dangerous (SPEC.md §8.3.4).
		m.stopsKey = m.markStops(context.Background(), services)
	}
	m.assignErr = err
	if err != nil {
		// Typed-in secrets and set-ups wait for the next assignment.
		m.Log.Error("applying changes", "err", err)
		m.Activity.Start(activity.Apply, "Applying changes").Finish(err)
		return
	}
	// CrowdSec shares nothing until the Owner has answered (SPEC.md §6.11):
	// on a lab on the internet it can start with the public UI, before
	// anything asks.
	pub := m.publicLocked()
	lab := node.LabContext{Domain: m.Lab.Domain, Email: m.Lab.Email, NoSharing: pub.NoSharing || !pub.SharingAsked}
	nodes := []node.Node{m.Node}
	for _, r := range m.others() {
		nodes = append(nodes, r)
	}
	infos := m.linkInfos(context.Background())
	// The edge passes public visitors on to the controller's Caddy, which
	// believes it alone about who they are (SPEC.md §10.5).
	edge, edgeAddrs := m.edgeFor(infos)
	lab.EdgeAddrs = append(edgeAddrs, m.relayFor(services)...)
	for _, n := range nodes {
		id := n.Identity().ID
		if n == m.Node {
			id = m.Lab.NodeID
		}
		a := node.Assignment{Services: m.onNode(services, id), Lab: lab, Reason: reason}
		for _, s := range a.Services {
			name := s.Name()
			for k, v := range m.secrets[name] {
				if a.Secrets == nil {
					a.Secrets = map[string][]byte{}
				}
				a.Secrets[k] = v
			}
			if slices.Contains(m.setUp, name) {
				a.SetUp = append(a.SetUp, name)
			}
		}
		if n == m.Node {
			a.Proxied = m.proxied(services, id)
			// The controller forwards its servers' backups to other labs
			// (SPEC.md §10.10).
			a.ProxyPorts = m.proxyPorts(context.Background())
		}
		// Other labs' stores live on the server offering storage.
		if n.Identity().ID == m.GuestServer(context.Background()) {
			a.Guests, a.GuestAddr = m.guestStores(context.Background())
		}
		a.Storage = m.storageFor(context.Background(), id)
		a.Links = m.linksFor(context.Background(), infos, id)
		if id == edge {
			a.Edge = edgeConfig(m.Lab, services)
		}
		n.Apply(a)
	}
	m.secrets, m.setUp = nil, nil
	// Every server has its packages now (Remote.Apply encodes them before
	// returning), so copies no service uses can go (SPEC.md §8.3.1).
	// A service updating keeps its previous version until it's done.
	var keep []string
	for name, version := range m.keptVersions() {
		if i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name }); i >= 0 {
			keep = append(keep, m.Lab.Paths.LabPackageFile(services[i].Instance.Package, version))
		}
	}
	if err := state.PrunePackages(m.Lab.Paths, services, keep...); err != nil {
		m.Log.Error("deleting unused package copies", "err", err)
	}
}

// allocateAndSave places services that have no host yet on this server
// and gives every server's routes their ports (per server: each has its
// own), saving what changed so ports never change. Callers hold m.mu.
func (m *Manager) allocateAndSave(services []state.Service) error {
	changed := state.AssignHosts(services, m.Lab.NodeID)
	hosts := map[string]bool{}
	for _, s := range services {
		hosts[s.Instance.Host] = true
	}
	for host := range hosts {
		ported, err := state.AllocatePorts(m.onNode(services, host))
		if err != nil {
			return err
		}
		for _, s := range ported {
			if !slices.ContainsFunc(changed, func(c state.Service) bool { return c.Name() == s.Name() }) {
				changed = append(changed, s)
			}
		}
	}
	for _, s := range changed {
		if err := s.Instance.Save(state.InstanceFile(m.Lab, s.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Status reports this server's current or last apply.
func (m *Manager) Status() ApplyStatus {
	st := m.Node.Status()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.assignErr != nil {
		st.LastErr, st.Finished = m.assignErr, true
	}
	return st
}

// Servers lists the lab's servers: this one (the controller) first.
func (m *Manager) Servers() ([]Server, error) {
	all, err := state.LoadServices(m.Lab)
	if err != nil {
		return nil, err
	}
	names := func(id string) []string {
		var out []string
		for _, s := range m.onNode(all, id) {
			out = append(out, s.Name())
		}
		slices.Sort(out)
		return out
	}
	id := m.Node.Identity()
	servers := []Server{{ID: id.ID, Name: id.Name, Controller: true, This: true, Reachable: true, Status: m.Status(),
		Services: names(m.Lab.NodeID)}}
	others := m.others()
	statuses := make([]ApplyStatus, len(others))
	reachable := make([]bool, len(others))
	// Ask every server at once: an unreachable one takes a few seconds.
	var wg sync.WaitGroup
	for i, r := range others {
		wg.Go(func() { statuses[i], reachable[i] = r.Status(), r.Settings() != nil })
	}
	wg.Wait()
	for i, r := range others {
		rid := r.Identity()
		var waiting []string
		for _, w := range waitingOn(all, rid.ID) {
			waiting = append(waiting, w.Name())
		}
		servers = append(servers, Server{ID: rid.ID, Name: rid.Name, Status: statuses[i], Reachable: reachable[i],
			Services: names(rid.ID), Waiting: waiting,
			UnreachableSince: m.unreachableSince(context.Background(), rid.ID, reachable[i])})
	}
	if m.Registry != nil {
		certs := m.Registry.CertExpiries(context.Background())
		for i := range servers {
			servers[i].CertExpires = certs[servers[i].ID]
		}
	}
	return servers, nil
}

// ServiceState is a service's state and where it runs.
type ServiceState struct {
	Status string
	// Server is the name of the server it runs on.
	Server string
}

// ServiceStates reports every service's state, asking each server.
func (m *Manager) ServiceStates(ctx context.Context) map[string]ServiceState {
	nodes := []node.Node{m.Node}
	for _, r := range m.others() {
		nodes = append(nodes, r)
	}
	results := make([]map[string]string, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() {
			states, err := n.ServiceStates(ctx)
			if err != nil {
				m.Log.Warn("reading service states", "server", n.Identity().Name, "err", err)
			}
			results[i] = states
		})
	}
	wg.Wait()
	out := map[string]ServiceState{}
	for i, n := range nodes {
		for name, st := range results[i] {
			out[name] = ServiceState{Status: st, Server: n.Identity().Name}
		}
	}
	return out
}

// pullGap is the least time between two assignment fetches by one server
// (M26 of the 2026-10-16 review). A server fetches when it starts, every
// 15 minutes, and every 30 seconds while fetching fails.
var pullGap = 10 * time.Second

// ErrPullTooSoon refuses a server fetching its assignment again within
// pullGap.
var ErrPullTooSoon = errors.New("this server fetched its assignment moments ago; try again shortly")

// pullTimes are when each server last fetched its assignment.
type pullTimes struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// PendingFor returns the assignment a server fetches when it starts and
// every so often (SPEC.md §10.3: nodes pull): the latest one built for
// it. Only when there's none yet (the controller restarted since) does it
// build them, which applies on every server, so a taken-over server
// asking again and again can't keep the controller reloading its
// firewall; and a server asking within pullGap of its last fetch is
// refused (ErrPullTooSoon). ErrNotFound for a server not in the lab.
func (m *Manager) PendingFor(id string) (node.WireAssignment, error) {
	m.pulls.mu.Lock()
	if last, ok := m.pulls.last[id]; ok && time.Since(last) < pullGap {
		m.pulls.mu.Unlock()
		return node.WireAssignment{}, ErrPullTooSoon
	}
	if m.pulls.last == nil {
		m.pulls.last = map[string]time.Time{}
	}
	m.pulls.last[id] = time.Now()
	m.pulls.mu.Unlock()
	type pending interface {
		Pending() (node.WireAssignment, bool)
	}
	var p pending
	for _, r := range m.others() {
		if r.Identity().ID == id {
			p, _ = r.(pending)
		}
	}
	if p == nil {
		return node.WireAssignment{}, ErrNotFound
	}
	if w, ok := p.Pending(); ok {
		return w, nil
	}
	m.Apply()
	if w, ok := p.Pending(); ok {
		return w, nil
	}
	return node.WireAssignment{}, ErrNotFound
}

// RemoveServer takes another server out of the lab. Its services must be
// moved or removed first. It is asked to leave (it may be switched off);
// either way, it loses its access to the lab's network.
func (m *Manager) RemoveServer(ctx context.Context, id string) error {
	if m.Registry == nil || id == m.Lab.NodeID {
		return ErrNotFound
	}
	var r RemoteServer
	for _, o := range m.others() {
		if o.Identity().ID == id {
			r = o
		}
	}
	if r == nil {
		return ErrNotFound
	}
	all, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	if on := m.onNode(all, id); len(on) > 0 {
		return formErrorf("%s still runs %s. Remove it first, or move it to another server.",
			r.Identity().Name, on[0].Title())
	}
	if w := waitingOn(all, id); len(w) > 0 {
		// Its backups are how they come back: keep it until they have.
		return formErrorf("%s waits to be restored from %s's backups. Restore it or remove it first.",
			w[0].Title(), r.Identity().Name)
	}
	reachable := r.Settings() != nil
	if err := r.Leave(ctx); err != nil || !reachable {
		if err != nil {
			m.Log.Warn("server didn't answer; removing it anyway", "server", r.Identity().Name, "err", err)
		}
		// If it comes back, it finds this in its backups and stands down
		// rather than backing up stale copies (backup/fence.go).
		m.markRemoved(ctx, id)
	}
	m.forgetServerBackups(ctx, id)
	if err := m.Registry.Remove(ctx, id); err != nil {
		return err
	}
	if m.Settings != nil {
		m.Settings.SetSetting(ctx, unreachableKey+id, "")
	}
	// It's out of the lab: stop retrying anything to it.
	if c, ok := r.(interface{ Close() }); ok {
		c.Close()
	}
	return nil
}

// RenameServer changes a server's name. Renaming this server restarts
// Roostack (RenameSelf), which reads its name at startup.
func (m *Manager) RenameServer(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 40 || strings.ContainsFunc(name, unicode.IsControl) {
		return formErrorf("Choose a name of up to 40 characters.")
	}
	if id == m.Lab.NodeID {
		if m.RenameSelf == nil {
			return ErrNotFound
		}
		return m.RenameSelf(name)
	}
	for _, o := range m.others() {
		if o.Identity().ID != id {
			continue
		}
		if err := m.Registry.Rename(ctx, id, name); err != nil {
			return err
		}
		if err := o.Rename(ctx, name); err != nil {
			m.Log.Warn("server didn't take its new name", "server", name, "err", err)
		}
		return nil
	}
	return ErrNotFound
}

// ServiceInfo is everything the UI shows about a service: where it runs
// and what its server reports about it.
type ServiceInfo struct {
	node.ServiceDetail
	// Server names the server it runs on; ServerID is its ID.
	Server, ServerID string
	// LastBackup is when its server last finished a backup with it in
	// (zero: never, it was installed since, or backups aren't set up
	// there).
	LastBackup time.Time
}

// ServiceInfos describes every service, asking each server.
func (m *Manager) ServiceInfos(ctx context.Context) map[string]ServiceInfo {
	nodes := m.allNodes()
	type result struct {
		details map[string]node.ServiceDetail
		backup  *store.BackupRun
	}
	results := make([]result, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() {
			d, err := n.ServiceDetails(ctx)
			if err != nil {
				m.Log.Warn("reading service details", "server", n.Identity().Name, "err", err)
			}
			results[i].details = d
			if b := n.Backups(); b != nil {
				if st, err := b.Status(ctx); err == nil {
					results[i].backup = st.LastOK
				}
			}
		})
	}
	wg.Wait()
	out := map[string]ServiceInfo{}
	for i, n := range nodes {
		for name, d := range results[i].details {
			info := ServiceInfo{ServiceDetail: d, Server: n.Identity().Name, ServerID: n.Identity().ID}
			// A backup from before runs listed their services counts.
			if b := results[i].backup; b != nil && (b.Services == nil || slices.Contains(b.Services, name)) {
				info.LastBackup = b.Finished
			}
			out[name] = info
		}
	}
	return out
}

// ServerBuiltIns is Roostack's own parts on one server.
type ServerBuiltIns struct {
	Server string
	Items  []node.BuiltIn
	// Err is why the server couldn't say (e.g. unreachable).
	Err error
}

// AllBuiltIns lists Roostack's own parts on every server.
func (m *Manager) AllBuiltIns(ctx context.Context) []ServerBuiltIns {
	nodes := m.allNodes()
	out := make([]ServerBuiltIns, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() {
			items, err := n.BuiltIns(ctx)
			out[i] = ServerBuiltIns{Server: n.Identity().Name, Items: items, Err: err}
		})
	}
	wg.Wait()
	return out
}

// allNodes is this server, then the others.
func (m *Manager) allNodes() []node.Node {
	nodes := []node.Node{m.Node}
	for _, r := range m.others() {
		nodes = append(nodes, r)
	}
	return nodes
}

// joinedWait is how long the controller waits for a server that just
// joined to answer (it restarts as part of the lab first).
var joinedWait = 10 * time.Minute

// MatchTimezone gives a server that just joined the controller's time
// zone, so the lab's schedules (backups, updates) and logs line up. It
// waits for the server to answer; the Owner can change it afterwards.
func (m *Manager) MatchTimezone(ctx context.Context, id string) {
	if m.Node == nil || m.Node.Settings() == nil {
		return
	}
	zone, _, err := m.Node.Settings().Timezones()
	if err != nil || zone == "" {
		return
	}
	deadline := time.Now().Add(joinedWait)
	for time.Now().Before(deadline) {
		if n := m.node(id); n != nil {
			if set := n.Settings(); set != nil {
				changed, err := set.SetTimezone(ctx, zone)
				switch {
				case err != nil:
					m.Log.Warn("giving a new server the lab's time zone", "server", m.nameOf(id), "err", err)
				case changed:
					m.Log.Info("gave a new server the lab's time zone", "server", m.nameOf(id), "zone", zone)
				}
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	m.Log.Warn("a new server didn't answer, so it keeps its own time zone", "server", m.nameOf(id))
}

// markRemoved leaves the removed mark in each of a removed server's
// repositories the controller can write to.
func (m *Manager) markRemoved(ctx context.Context, id string) {
	if m.BackupStore == nil {
		return
	}
	targets, _ := m.BackupStore.BackupTargets(ctx)
	copies, _ := m.BackupStore.BackupCopies(ctx)
	for _, t := range append(targets, copies...) {
		if t.ServerID != id || strings.HasPrefix(t.Provider, "setup") {
			continue
		}
		repo, err := m.adminRepo(ctx, t)
		if err == nil {
			err = repo.MarkRemoved(ctx, m.nameOf(id))
		}
		if err != nil {
			m.Log.Warn("marking a removed server's backups", "server", m.nameOf(id), "err", err)
		}
	}
}

// ServiceFiles are the files Roostack writes for a service, for its page's
// technical side: its Quadlet units (on the server it runs on) and its
// Caddy site (on the controller, which forwards to the service's server
// when it's another one). Generation is pure, so these are what the
// servers have, made again here rather than read from them.
type ServiceFiles struct {
	QuadletDir, CaddySitesDir string
	Quadlets                  []generate.File
	CaddySite                 *generate.File
}

// ServiceFiles makes a service's files as its servers write them.
func (m *Manager) ServiceFiles(name string) (*ServiceFiles, error) {
	p, in, err := m.installedService(name)
	if err != nil {
		return nil, err
	}
	out, err := generate.Service(m.Lab, p, in)
	if err != nil {
		return nil, err
	}
	f := &ServiceFiles{QuadletDir: m.Lab.Paths.QuadletDir(), CaddySitesDir: m.Lab.Paths.CaddySitesDir(),
		Quadlets: out.Quadlets, CaddySite: out.CaddySite}
	if in.Host != "" && in.Host != m.Lab.NodeID {
		f.CaddySite = nil
		for _, r := range m.others() {
			if r.Identity().ID == in.Host {
				if proxy, err := generate.ProxySite(m.Lab, p, in, r.Address()); err == nil {
					f.CaddySite = proxy.CaddySite
				}
			}
		}
	}
	return f, nil
}
