package node

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"roostack.cloud/roostack/internal/agent"
)

// Storage for other labs can be offered by any of a lab's servers (SPEC.md
// §10.10): the controller keeps the records and answers guests' requests,
// the chosen server runs the guests' link and stores and holds their data.

// GuestHost is a server hosting other labs' backups.
type GuestHost interface {
	// ApplyGuestLink configures its guests' WireGuard interface
	// (agent.GuestsInterface only) and returns its public key.
	ApplyGuestLink(ctx context.Context, wg agent.WireGuard) (string, error)
	GuestLinkStatus(ctx context.Context) (agent.WireGuardStatus, error)
	RemoveGuestLink(ctx context.Context) error
	// GuestUsage is how much a guest stores there; GuestDirs lists the
	// guests with data there; RemoveGuestDir deletes a guest's data.
	GuestUsage(ctx context.Context, user string) (int64, error)
	GuestDirs(ctx context.Context) ([]string, error)
	RemoveGuestDir(ctx context.Context, user string) error
	// GuestSpace is the free and total bytes where guests' data goes.
	GuestSpace(ctx context.Context) (free, total int64, err error)
}

func (n *Local) GuestSpace(context.Context) (int64, int64, error) {
	dir := n.Lab.Paths.BackupStoreDir()
	for {
		var st syscall.Statfs_t
		err := syscall.Statfs(dir, &st)
		if err == nil {
			return int64(st.Bavail) * st.Bsize, int64(st.Blocks) * st.Bsize, nil
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, os.ErrNotExist) || parent == dir {
			return 0, 0, err
		}
		dir = parent // not created yet: the disk it will be on
	}
}

func (r *Remote) GuestSpace(ctx context.Context) (int64, int64, error) {
	var out struct{ Free, Total int64 }
	err := r.call(ctx, http.MethodGet, "/v1/guests/space", nil, &out)
	return out.Free, out.Total, err
}

// LinkAgent is roostack-agent's WireGuard operations.
type LinkAgent interface {
	ApplyWireGuard(ctx context.Context, wg agent.WireGuard) (string, error)
	WireGuardStatus(ctx context.Context, iface string) (agent.WireGuardStatus, error)
	RemoveWireGuard(ctx context.Context, iface string) error
}

var errNotGuestLink = errors.New("only the guests' link can be configured this way")

func (n *Local) ApplyGuestLink(ctx context.Context, wg agent.WireGuard) (string, error) {
	if wg.Iface() != agent.GuestsInterface || wg.JoinKey != "" || wg.NewKey {
		return "", errNotGuestLink
	}
	if n.Links == nil {
		return "", ErrNotFound
	}
	return n.Links.ApplyWireGuard(ctx, wg)
}

func (n *Local) GuestLinkStatus(ctx context.Context) (agent.WireGuardStatus, error) {
	if n.Links == nil {
		return agent.WireGuardStatus{}, ErrNotFound
	}
	return n.Links.WireGuardStatus(ctx, agent.GuestsInterface)
}

func (n *Local) RemoveGuestLink(ctx context.Context) error {
	if n.Links == nil {
		return ErrNotFound
	}
	return n.Links.RemoveWireGuard(ctx, agent.GuestsInterface)
}

// GuestAddr is this server's address on the guests' link, when it offers
// storage for other labs ("" otherwise).
func (n *Local) GuestAddr() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.assignment.GuestAddr
}

// The lab API's guest-host routes (on a node, for the controller).
func (s *Server) guestHostRoutes(handle func(string, func(*http.Request) (any, error)), decode func(*http.Request, any) error) {
	h, ok := s.Node.(GuestHost)
	if !ok {
		return
	}
	handle("PUT /v1/guests/link", func(r *http.Request) (any, error) {
		var wg agent.WireGuard
		if err := decode(r, &wg); err != nil {
			return nil, err
		}
		pub, err := h.ApplyGuestLink(r.Context(), wg)
		return map[string]string{"public_key": pub}, err
	})
	handle("GET /v1/guests/link", func(r *http.Request) (any, error) { return h.GuestLinkStatus(r.Context()) })
	handle("DELETE /v1/guests/link", func(r *http.Request) (any, error) { return nil, h.RemoveGuestLink(r.Context()) })
	handle("GET /v1/guests/dirs", func(r *http.Request) (any, error) { return h.GuestDirs(r.Context()) })
	handle("GET /v1/guests/space", func(r *http.Request) (any, error) {
		free, total, err := h.GuestSpace(r.Context())
		return struct{ Free, Total int64 }{free, total}, err
	})
	handle("GET /v1/guests/{user}/usage", func(r *http.Request) (any, error) {
		used, err := h.GuestUsage(r.Context(), r.PathValue("user"))
		return map[string]int64{"used": used}, err
	})
	handle("DELETE /v1/guests/{user}", func(r *http.Request) (any, error) {
		return nil, h.RemoveGuestDir(r.Context(), r.PathValue("user"))
	})
}

// The controller's side of a node hosting guests.

func (r *Remote) ApplyGuestLink(ctx context.Context, wg agent.WireGuard) (string, error) {
	var out struct {
		PublicKey string `json:"public_key"`
	}
	err := r.call(ctx, http.MethodPut, "/v1/guests/link", wg, &out)
	return out.PublicKey, err
}

func (r *Remote) GuestLinkStatus(ctx context.Context) (agent.WireGuardStatus, error) {
	var st agent.WireGuardStatus
	err := r.call(ctx, http.MethodGet, "/v1/guests/link", nil, &st)
	return st, err
}

func (r *Remote) RemoveGuestLink(ctx context.Context) error {
	return r.call(ctx, http.MethodDelete, "/v1/guests/link", nil, nil)
}

func (r *Remote) GuestUsage(ctx context.Context, user string) (int64, error) {
	var out struct {
		Used int64 `json:"used"`
	}
	err := r.call(ctx, http.MethodGet, "/v1/guests/"+url.PathEscape(user)+"/usage", nil, &out)
	return out.Used, err
}

func (r *Remote) GuestDirs(ctx context.Context) ([]string, error) {
	var out []string
	err := r.call(ctx, http.MethodGet, "/v1/guests/dirs", nil, &out)
	return out, err
}

func (r *Remote) RemoveGuestDir(ctx context.Context, user string) error {
	return r.call(ctx, http.MethodDelete, "/v1/guests/"+url.PathEscape(user), nil, nil)
}
