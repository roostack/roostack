package node

import (
	"context"
	"net/http"
	"net/url"

	"roostack.cloud/roostack/internal/activity"
)

// The server itself on the lab API: its status, its assignment, its
// services, leaving the lab and its name.

const (
	statusPath     = "/v1/status"
	assignmentPath = "/v1/assignment"
)

// servicePath is a route about one of the server's services.
func servicePath(service, rest string) string {
	return "/v1/services/" + url.PathEscape(service) + rest
}

// nameRequest is a server's new name.
type nameRequest struct{ Name string }

// Leave asks the server to leave the lab: it takes down its WireGuard
// interface and becomes a lab of its own.
func (r *Remote) Leave(ctx context.Context) error {
	return r.call(ctx, http.MethodPost, "/v1/leave", struct{}{}, nil)
}

// Rename tells the server its new name.
func (r *Remote) Rename(ctx context.Context, name string) error {
	return r.call(ctx, http.MethodPut, "/v1/name", nameRequest{Name: name}, nil)
}

func (r *Remote) ServiceStates(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var out map[string]string
	err := r.call(ctx, http.MethodGet, "/v1/services", nil, &out)
	return out, err
}

func (r *Remote) ServiceDetails(ctx context.Context) (map[string]ServiceDetail, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var out map[string]ServiceDetail
	err := r.call(ctx, http.MethodGet, "/v1/services/details", nil, &out)
	return out, err
}

func (r *Remote) BuiltIns(ctx context.Context) ([]BuiltIn, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	var out []BuiltIn
	err := r.call(ctx, http.MethodGet, "/v1/builtins", nil, &out)
	return out, err
}

func (r *Remote) LatestActivity(kind string) (activity.Snapshot, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	var snap activity.Snapshot
	err := r.call(ctx, http.MethodGet, "/v1/activity/"+url.PathEscape(kind), nil, &snap)
	return snap, err == nil
}

// labRoutes serves the server itself on the lab API.
func (s *Server) labRoutes(handle handleFunc) {
	n := s.Node
	handle("GET "+statusPath, func(r *http.Request) (any, error) {
		st := n.Status()
		w := WireStatus{Running: st.Running, LastRun: st.LastRun, Finished: st.Finished, Doing: st.Doing, Waiting: st.Waiting, Version: s.Version(),
			Backups: n.Backups() != nil, Restores: n.Restores() != nil, Updates: n.Updates() != nil,
			Settings: n.Settings() != nil, Imports: n.Imports() != nil, Kept: n.Mover() != nil}
		if p := n.Processor(); p != nil {
			w.Arch = p.Arch()
		}
		if st.LastErr != nil {
			w.LastErr = st.LastErr.Error()
		}
		if l := n.Linked(); l != nil {
			w.Addrs, w.Links = l.LinkStatus(r.Context())
		}
		return w, nil
	})
	handle("PUT "+assignmentPath, func(r *http.Request) (any, error) {
		wa, err := body[WireAssignment](r)
		if err != nil {
			return nil, err
		}
		return nil, s.Take(wa)
	})
	handle("GET /v1/services", func(r *http.Request) (any, error) { return n.ServiceStates(r.Context()) })
	handle("GET /v1/services/details", func(r *http.Request) (any, error) { return n.ServiceDetails(r.Context()) })
	handle("GET /v1/builtins", func(r *http.Request) (any, error) { return n.BuiltIns(r.Context()) })
	handle("POST /v1/leave", func(r *http.Request) (any, error) {
		if s.Leave == nil {
			return nil, ErrNotFound
		}
		s.Leave()
		return nil, nil
	})
	handle("PUT /v1/name", func(r *http.Request) (any, error) {
		if s.Rename == nil {
			return nil, ErrNotFound
		}
		req, err := body[nameRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, s.Rename(req.Name)
	})
	handle("GET /v1/activity/{kind}", func(r *http.Request) (any, error) {
		snap, ok := n.LatestActivity(r.PathValue("kind"))
		if !ok {
			return nil, ErrNotFound
		}
		return snap, nil
	})
}
