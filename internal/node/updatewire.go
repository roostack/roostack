package node

import (
	"context"
	"net/http"
	"net/url"
)

// Updating a service (Updater, update.go) on the lab API.

func updatePath(id, rest string) string { return "/v1/updates/" + url.PathEscape(id) + rest }

// Updater asks the server to update its services.
func (r *Remote) Updater() Updater { return r }

func (r *Remote) StartUpdate(ctx context.Context, req UpdateRequest) (string, error) {
	var id string
	err := r.call(ctx, http.MethodPost, "/v1/updates", req, &id)
	return id, err
}

func (r *Remote) UpdateStatus(ctx context.Context, id string) (UpdateStatus, error) {
	var out UpdateStatus
	err := r.call(ctx, http.MethodGet, updatePath(id, ""), nil, &out)
	return out, err
}

func (r *Remote) FinishUpdate(ctx context.Context, id string) error {
	return r.call(ctx, http.MethodPost, updatePath(id, "/finish"), struct{}{}, nil)
}

func (r *Remote) CheckHealth(ctx context.Context, service string) (Health, error) {
	var out Health
	err := r.call(ctx, http.MethodGet, servicePath(service, "/health"), nil, &out)
	return out, err
}

// updateRoutes serves Updater on the lab API.
func (s *Server) updateRoutes(handle handleFunc) {
	update := capRoute(handle, offered(s.Node.Updater))
	update("POST /v1/updates", func(u Updater, r *http.Request) (any, error) {
		req, err := body[UpdateRequest](r)
		if err != nil {
			return nil, err
		}
		return u.StartUpdate(r.Context(), req)
	})
	update("GET /v1/updates/{id}", func(u Updater, r *http.Request) (any, error) {
		return u.UpdateStatus(r.Context(), r.PathValue("id"))
	})
	update("POST /v1/updates/{id}/finish", func(u Updater, r *http.Request) (any, error) {
		return nil, u.FinishUpdate(r.Context(), r.PathValue("id"))
	})
	update("GET /v1/services/{service}/health", func(u Updater, r *http.Request) (any, error) {
		return u.CheckHealth(r.Context(), svcOf(r))
	})
}
