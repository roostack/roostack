package manager

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
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
	// Services are the names of the services placed on it.
	Services []string
	Status   ApplyStatus
	// CertExpires is when its certificate on Roostack's network expires
	// (renewed automatically well before); zero if unknown.
	CertExpires time.Time
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

// onNode returns the services placed on a server.
func (m *Manager) onNode(services []state.Service, id string) []state.Service {
	if id == m.Lab.NodeID {
		return state.OnNode(services, id) // including any with no host yet
	}
	var out []state.Service
	for _, s := range services {
		if s.Instance.Host == id {
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
func (m *Manager) Apply() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startApplyLocked()
}

// startApplyLocked builds each server's assignment and hands it over;
// servers apply in the background.
func (m *Manager) startApplyLocked() {
	services, err := state.LoadServices(m.Lab)
	if err == nil {
		err = m.allocateAndSave(services)
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
	for _, n := range nodes {
		id := n.Identity().ID
		if n == m.Node {
			id = m.Lab.NodeID
		}
		a := node.Assignment{Services: m.onNode(services, id), Lab: lab}
		for _, s := range a.Services {
			name := s.Package.Name
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
		n.Apply(a)
	}
	m.secrets, m.setUp = nil, nil
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
			if !slices.ContainsFunc(changed, func(c state.Service) bool { return c.Package.Name == s.Package.Name }) {
				changed = append(changed, s)
			}
		}
	}
	for _, s := range changed {
		if err := s.Instance.Save(state.InstanceFile(m.Lab, s.Package.Name)); err != nil {
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
			out = append(out, s.Package.Name)
		}
		slices.Sort(out)
		return out
	}
	id := m.Node.Identity()
	servers := []Server{{ID: id.ID, Name: id.Name, Controller: true, This: true, Status: m.Status(),
		Services: names(m.Lab.NodeID)}}
	others := m.others()
	statuses := make([]ApplyStatus, len(others))
	// Ask every server at once: an unreachable one takes a few seconds.
	var wg sync.WaitGroup
	for i, r := range others {
		wg.Go(func() { statuses[i] = r.Status() })
	}
	wg.Wait()
	for i, r := range others {
		rid := r.Identity()
		servers = append(servers, Server{ID: rid.ID, Name: rid.Name, Status: statuses[i], Services: names(rid.ID)})
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

// PendingFor returns the assignment a server fetches when it starts
// (SPEC.md §10.3: nodes pull), building fresh assignments first.
func (m *Manager) PendingFor(id string) (node.WireAssignment, bool) {
	m.Apply()
	for _, r := range m.others() {
		if r.Identity().ID != id {
			continue
		}
		if p, ok := r.(interface {
			Pending() (node.WireAssignment, bool)
		}); ok {
			return p.Pending()
		}
	}
	return node.WireAssignment{}, false
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
			r.Identity().Name, on[0].Package.Title)
	}
	if err := r.Leave(ctx); err != nil {
		m.Log.Warn("server didn't answer; removing it anyway", "server", r.Identity().Name, "err", err)
	}
	m.forgetServerBackups(ctx, id)
	return m.Registry.Remove(ctx, id)
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
	// LastBackup is when its server last finished a backup (zero: never,
	// or backups aren't set up there).
	LastBackup time.Time
}

// ServiceInfos describes every service, asking each server.
func (m *Manager) ServiceInfos(ctx context.Context) map[string]ServiceInfo {
	nodes := m.allNodes()
	type result struct {
		details map[string]node.ServiceDetail
		backup  time.Time
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
				if st, err := b.Status(ctx); err == nil && st.LastOK != nil {
					results[i].backup = st.LastOK.Finished
				}
			}
		})
	}
	wg.Wait()
	out := map[string]ServiceInfo{}
	for i, n := range nodes {
		for name, d := range results[i].details {
			out[name] = ServiceInfo{ServiceDetail: d, Server: n.Identity().Name, ServerID: n.Identity().ID,
				LastBackup: results[i].backup}
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
