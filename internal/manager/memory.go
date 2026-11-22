package manager

import (
	"context"
	"slices"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/state"
)

// ServiceMemory is what a service's settings page says about its memory
// (SPEC.md §6.1, "Memory").
type ServiceMemory struct {
	// Choice is the Owner's (instance.MemoryLimit): a size, "none", or
	// "" for Roostack to choose.
	Choice string
	// Figure is what its package expects with its settings, and Known
	// that every part of it says.
	Figure int64
	Known  bool
	// Auto is the limit Roostack would choose now (0: none), and Tight
	// whether the services on its server together expect more than they
	// may use, which is when it chooses one; Server is that server's name,
	// and Total its memory (0: not known).
	Auto   int64
	Tight  bool
	Server string
	Total  int64
}

// ServiceMemory works out a service's memory figures, for its page.
func (m *Manager) ServiceMemory(ctx context.Context, name string) (*ServiceMemory, error) {
	m.mu.Lock()
	services, err := state.LoadServices(m.Lab)
	lab := m.labNow()
	m.mu.Unlock()
	if err != nil && len(services) == 0 {
		return nil, err
	}
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name })
	if i < 0 {
		return nil, ErrNotFound
	}
	s := services[i]
	out := &ServiceMemory{Choice: s.Instance.MemoryLimit}
	if out.Figure, out.Known, err = generate.MemoryFigure(lab, s.Package, s.Instance); err != nil {
		return nil, err
	}
	host := s.Instance.HostOr(m.Lab.NodeID)
	n := m.node(host)
	if n == nil {
		return out, nil
	}
	out.Server = n.Identity().Name
	if nm := node.Capability(n, node.Node.Memory); nm != nil {
		ctx, cancel := context.WithTimeout(ctx, memoryWait)
		defer cancel()
		if info, err := nm.MemoryInfo(ctx); err == nil {
			out.Total = info.Total
		}
	}
	lab.MemoryTotal = out.Total
	out.Tight = state.MemoryTight(lab, m.onNode(services, host))
	auto := *s.Instance
	auto.MemoryLimit = ""
	out.Auto, _ = generate.MemoryLimit(lab, s.Package, &auto, out.Tight)
	return out, nil
}
