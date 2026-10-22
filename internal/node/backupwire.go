package node

import (
	"context"
	"net/http"
	"net/url"

	"roostack.cloud/roostack/internal/backup"
)

// Backups and restores (maintenance.go) on the lab API.

func restorePath(service, rest string) string {
	return "/v1/restores/" + url.PathEscape(service) + rest
}

// restoreRequest is the backup to restore a service from.
type restoreRequest struct{ Snapshot string }

// pathRequest names a kept copy of a service's data, or a folder a move
// left behind.
type pathRequest struct{ Path string }

// Backups and the other capabilities are offered if the server has them
// (as of its last status).
func (r *Remote) Backups() Backups {
	if st, err := r.wireStatus(); err != nil || !st.Backups {
		return nil
	}
	return remoteBackups{r}
}

func (r *Remote) Restores() Restores {
	if st, err := r.wireStatus(); err != nil || !st.Restores {
		return nil
	}
	return remoteRestores{r}
}

type remoteBackups struct{ r *Remote }

func (b remoteBackups) Status(ctx context.Context) (backup.Status, error) {
	var st backup.Status
	err := b.r.call(ctx, http.MethodGet, "/v1/backups", nil, &st)
	return st, err
}

func (b remoteBackups) RunNow(ctx context.Context) error {
	return b.r.call(ctx, http.MethodPost, "/v1/backups/run", struct{}{}, nil)
}

type remoteRestores struct{ r *Remote }

func (x remoteRestores) Snapshots(ctx context.Context, service string) ([]backup.Snapshot, error) {
	var out []backup.Snapshot
	err := x.r.call(ctx, http.MethodGet, restorePath(service, "/snapshots"), nil, &out)
	return out, err
}

func (x remoteRestores) Restore(ctx context.Context, service, snapshotID string) error {
	return x.r.call(ctx, http.MethodPost, restorePath(service, ""), restoreRequest{Snapshot: snapshotID}, nil)
}

func (x remoteRestores) Pending(ctx context.Context) (bool, error) {
	var out bool
	err := x.r.call(ctx, http.MethodGet, "/v1/restores/pending", nil, &out)
	return out, err
}

func (x remoteRestores) Kept(ctx context.Context, service string) ([]backup.KeptCopy, error) {
	var out []backup.KeptCopy
	err := x.r.call(ctx, http.MethodGet, restorePath(service, "/kept"), nil, &out)
	return out, err
}

func (x remoteRestores) Outcome(ctx context.Context, service string) (backup.RestoreOutcome, error) {
	var out backup.RestoreOutcome
	err := x.r.call(ctx, http.MethodGet, restorePath(service, "/outcome"), nil, &out)
	return out, err
}

func (x remoteRestores) PutBack(ctx context.Context, service, path string) error {
	return x.r.call(ctx, http.MethodPost, restorePath(service, "/kept/put-back"), pathRequest{Path: path}, nil)
}

func (x remoteRestores) RemoveKept(ctx context.Context, service, path string) error {
	return x.r.call(ctx, http.MethodPost, restorePath(service, "/kept/remove"), pathRequest{Path: path}, nil)
}

// backupRoutes serves Backups and Restores on the lab API.
func (s *Server) backupRoutes(handle handleFunc) {
	backups := capRoute(handle, offered(s.Node.Backups))
	backups("GET /v1/backups", func(b Backups, r *http.Request) (any, error) { return b.Status(r.Context()) })
	backups("POST /v1/backups/run", func(b Backups, r *http.Request) (any, error) { return nil, b.RunNow(r.Context()) })

	restores := capRoute(handle, offered(s.Node.Restores))
	restores("GET /v1/restores/pending", func(x Restores, r *http.Request) (any, error) { return x.Pending(r.Context()) })
	restores("GET /v1/restores/{service}/snapshots", func(x Restores, r *http.Request) (any, error) {
		return x.Snapshots(r.Context(), svcOf(r))
	})
	restores("POST /v1/restores/{service}", func(x Restores, r *http.Request) (any, error) {
		req, err := body[restoreRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, x.Restore(r.Context(), svcOf(r), req.Snapshot)
	})
	restores("GET /v1/restores/{service}/outcome", func(x Restores, r *http.Request) (any, error) {
		return x.Outcome(r.Context(), svcOf(r))
	})
	restores("GET /v1/restores/{service}/kept", func(x Restores, r *http.Request) (any, error) {
		return x.Kept(r.Context(), svcOf(r))
	})
	restores("POST /v1/restores/{service}/kept/put-back", func(x Restores, r *http.Request) (any, error) {
		req, err := body[pathRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, x.PutBack(r.Context(), svcOf(r), req.Path)
	})
	restores("POST /v1/restores/{service}/kept/remove", func(x Restores, r *http.Request) (any, error) {
		req, err := body[pathRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, x.RemoveKept(r.Context(), svcOf(r), req.Path)
	})
}
