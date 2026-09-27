package node

import (
	"context"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/labnet"
	"github.com/mylab/mylab/internal/nodeid"
	"github.com/mylab/mylab/internal/store"
)

// Fleet is the controller's other servers: one Remote each, kept so their
// pending assignments and status survive between requests.
type Fleet struct {
	Self labnet.Identity
	Lab  config.Lab
	Log  *slog.Logger
	// List reads the servers that have joined (store.Servers).
	List func(ctx context.Context) ([]store.Server, error)

	mu      sync.Mutex
	remotes map[string]*Remote
}

// Remotes returns the lab's other servers.
func (f *Fleet) Remotes() []*Remote {
	servers, err := f.List(context.Background())
	if err != nil {
		f.Log.Error("listing servers", "err", err)
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remotes == nil {
		f.remotes = map[string]*Remote{}
	}
	var out []*Remote
	for _, m := range servers {
		addr, err := netip.ParseAddr(m.Addr)
		if err != nil {
			continue
		}
		r := f.remotes[m.ID]
		if r == nil || r.Addr != addr || r.ID.Name != m.Name {
			if r, err = NewRemote(f.Self, nodeid.Identity{ID: m.ID, Name: m.Name}, addr, f.Lab, f.Log); err != nil {
				f.Log.Error("reaching a server", "server", m.Name, "err", err)
				continue
			}
			f.remotes[m.ID] = r
		}
		out = append(out, r)
	}
	return out
}
