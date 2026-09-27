package manager

import (
	"context"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/node"
	"github.com/mylab/mylab/internal/state"
)

// A lab's machines (SPEC.md §10): this one, the controller, which keeps
// desired state, and any others, which run the services placed on them.
// Each gets its own Assignment.

// RemoteMachine is another machine (node.Remote).
type RemoteMachine interface {
	node.Node
	// Address is where the controller's Caddy reaches its web services.
	Address() string
	// Leave asks it to leave the lab; Rename tells it its new name.
	Leave(ctx context.Context) error
	Rename(ctx context.Context, name string) error
}

// MachineRegistry is the controller's record of the other machines
// (labnet.Hub).
type MachineRegistry interface {
	Remove(ctx context.Context, id string) error
	Rename(ctx context.Context, id, name string) error
}

// Machine is one machine in the lab, for the UI.
type Machine struct {
	ID, Name string
	// Controller holds the lab's desired state and serves the UI.
	Controller bool
	// This is the machine serving this request.
	This bool
	// Services are the names of the services placed on it.
	Services []string
	Status   ApplyStatus
}

func (m *Manager) others() []RemoteMachine {
	if m.Others == nil {
		return nil
	}
	return m.Others()
}

// node returns the machine with a node ID, or nil.
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

// NodeByID returns a machine by its node ID, or nil.
func (m *Manager) NodeByID(id string) node.Node { return m.node(id) }

// NodeFor returns the machine a service runs on, or nil if it isn't
// installed (or its machine has left the lab).
func (m *Manager) NodeFor(service string) node.Node {
	_, in, err := m.installedService(service)
	if err != nil {
		return nil
	}
	return m.node(in.Host)
}

// onNode returns the services placed on a machine.
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

// proxied returns the services on other machines than id whose routes
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

// Apply hands every machine its latest assignment (e.g. at startup).
func (m *Manager) Apply() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startApplyLocked()
}

// startApplyLocked builds each machine's assignment and hands it over;
// machines apply in the background.
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
	lab := node.LabContext{Domain: m.Lab.Domain, Email: m.Lab.Email}
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
			// Other labs' stores live on the controller, and it forwards its
			// machines' backups to other labs (SPEC.md §10.10).
			a.Guests, a.GuestAddr = m.guestStores(context.Background())
			a.ProxyPorts = m.proxyPorts(context.Background())
		}
		a.Storage = m.storageFor(context.Background(), id)
		n.Apply(a)
	}
	m.secrets, m.setUp = nil, nil
}

// allocateAndSave places services that have no host yet on this machine
// and gives every machine's routes their ports (per machine: each has its
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

// Status reports this machine's current or last apply.
func (m *Manager) Status() ApplyStatus {
	st := m.Node.Status()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.assignErr != nil {
		st.LastErr, st.Finished = m.assignErr, true
	}
	return st
}

// Machines lists the lab's machines: this one (the controller) first.
func (m *Manager) Machines() ([]Machine, error) {
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
	machines := []Machine{{ID: id.ID, Name: id.Name, Controller: true, This: true, Status: m.Status(),
		Services: names(m.Lab.NodeID)}}
	others := m.others()
	statuses := make([]ApplyStatus, len(others))
	// Ask every machine at once: an unreachable one takes a few seconds.
	var wg sync.WaitGroup
	for i, r := range others {
		wg.Go(func() { statuses[i] = r.Status() })
	}
	wg.Wait()
	for i, r := range others {
		rid := r.Identity()
		machines = append(machines, Machine{ID: rid.ID, Name: rid.Name, Status: statuses[i], Services: names(rid.ID)})
	}
	return machines, nil
}

// ServiceState is a service's state and where it runs.
type ServiceState struct {
	Status string
	// Machine is the name of the machine it runs on.
	Machine string
}

// ServiceStates reports every service's state, asking each machine.
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
				m.Log.Warn("reading service states", "machine", n.Identity().Name, "err", err)
			}
			results[i] = states
		})
	}
	wg.Wait()
	out := map[string]ServiceState{}
	for i, n := range nodes {
		for name, st := range results[i] {
			out[name] = ServiceState{Status: st, Machine: n.Identity().Name}
		}
	}
	return out
}

// PendingFor returns the assignment a machine fetches when it starts
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

// RemoveMachine takes another machine out of the lab. Its services must be
// moved or removed first. It is asked to leave (it may be switched off);
// either way, it loses its access to the lab's network.
func (m *Manager) RemoveMachine(ctx context.Context, id string) error {
	if m.Registry == nil || id == m.Lab.NodeID {
		return ErrNotFound
	}
	var r RemoteMachine
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
		m.Log.Warn("server didn't answer; removing it anyway", "machine", r.Identity().Name, "err", err)
	}
	m.forgetMachineBackups(ctx, id)
	return m.Registry.Remove(ctx, id)
}

// RenameMachine changes a machine's name. Renaming this machine restarts
// MyLab (RenameSelf), which reads its name at startup.
func (m *Manager) RenameMachine(ctx context.Context, id, name string) error {
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
			m.Log.Warn("server didn't take its new name", "machine", name, "err", err)
		}
		return nil
	}
	return ErrNotFound
}
