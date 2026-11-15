package manager

import (
	"context"

	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/vitals"
)

// VitalsServers are the lab's servers, for how each is doing (SPEC.md
// §6.15.1): this one, and the others as their status last said, each with
// the services placed on it.
func (m *Manager) VitalsServers(context.Context) []vitals.Server {
	type reader interface {
		Vitals(context.Context) (node.Vitals, error)
	}
	services, err := state.LoadServices(m.labNow())
	if err != nil {
		m.Log.Error("reading the services for servers' readings", "err", err)
	}
	var out []vitals.Server
	add := func(id, name string, n any) {
		if r, ok := n.(reader); ok {
			var names []string
			for _, s := range state.OnNode(services, id) {
				names = append(names, s.Name())
			}
			out = append(out, vitals.Server{ID: id, Name: name, Services: names, Read: r.Vitals})
		}
	}
	add(m.Lab.NodeID, m.Node.Identity().Name, m.Node)
	for _, r := range m.others() {
		add(r.Identity().ID, r.Identity().Name, r)
	}
	return out
}
