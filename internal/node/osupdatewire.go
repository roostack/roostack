package node

import (
	"context"
	"errors"
	"net/http"

	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/usererr"
)

// OS updates and restarts (maintenance.go) on the lab API.

// wireUpdates is a server's OS updates at a glance: one read for the
// pages that show all three.
type wireUpdates struct {
	Last    *osupdate.Result
	Pending bool
	Restart osupdate.RestartStatus
}

// seriesRequest is the Roostack release series the Owner accepted.
type seriesRequest struct{ Series string }

func (r *Remote) Updates() Updates {
	if st, err := r.wireStatus(); err != nil || !st.Updates {
		return nil
	}
	return remoteUpdates{r}
}

type remoteUpdates struct{ r *Remote }

func (u remoteUpdates) get(ctx context.Context) (wireUpdates, error) {
	var w wireUpdates
	err := u.r.call(ctx, http.MethodGet, "/v1/updates", nil, &w)
	return w, err
}

func (u remoteUpdates) Last(ctx context.Context) (*osupdate.Result, error) {
	w, err := u.get(ctx)
	return w.Last, err
}

func (u remoteUpdates) Pending(ctx context.Context) (bool, error) {
	w, err := u.get(ctx)
	return w.Pending, err
}

func (u remoteUpdates) Submit(ctx context.Context) error {
	return u.r.call(ctx, http.MethodPost, "/v1/updates/run", struct{}{}, nil)
}

func (u remoteUpdates) RestartStatus(ctx context.Context) (osupdate.RestartStatus, error) {
	w, err := u.get(ctx)
	return w.Restart, err
}

func (u remoteUpdates) HostChanges(ctx context.Context) (osupdate.HostCheck, error) {
	var c osupdate.HostCheck
	err := u.r.call(ctx, http.MethodGet, "/v1/host/changes", nil, &c)
	return c, err
}

func (u remoteUpdates) AcknowledgeHostChanges(ctx context.Context) error {
	return u.r.call(ctx, http.MethodPost, "/v1/host/changes/known", struct{}{}, nil)
}

func (u remoteUpdates) Series(ctx context.Context) (osupdate.Series, error) {
	var s osupdate.Series
	err := u.r.call(ctx, http.MethodGet, "/v1/updates/series", nil, &s)
	return s, err
}

func (u remoteUpdates) AcceptSeries(ctx context.Context, series string) error {
	return u.r.call(ctx, http.MethodPost, "/v1/updates/series", seriesRequest{Series: series}, nil)
}

func (u remoteUpdates) RestartNow(ctx context.Context) error {
	return u.r.call(ctx, http.MethodPost, "/v1/restart", struct{}{}, nil)
}

// osUpdateRoutes serves Updates on the lab API.
func (s *Server) osUpdateRoutes(handle handleFunc) {
	updates := capRoute(handle, offered(s.Node.Updates))
	updates("GET /v1/updates", func(u Updates, r *http.Request) (any, error) {
		last, err := u.Last(r.Context())
		if err != nil {
			return nil, err
		}
		pending, err := u.Pending(r.Context())
		if err != nil {
			return nil, err
		}
		restart, err := u.RestartStatus(r.Context())
		return wireUpdates{Last: last, Pending: pending, Restart: restart}, err
	})
	updates("GET /v1/host/changes", func(u Updates, r *http.Request) (any, error) { return u.HostChanges(r.Context()) })
	updates("POST /v1/host/changes/known", func(u Updates, r *http.Request) (any, error) {
		return nil, u.AcknowledgeHostChanges(r.Context())
	})
	updates("GET /v1/updates/series", func(u Updates, r *http.Request) (any, error) { return u.Series(r.Context()) })
	updates("POST /v1/updates/series", func(u Updates, r *http.Request) (any, error) {
		req, err := body[seriesRequest](r)
		if err != nil {
			return nil, err
		}
		err = u.AcceptSeries(r.Context(), req.Series)
		if errors.Is(err, osupdate.ErrNoSuchSeries) {
			return nil, &usererr.Error{Msg: SeriesGoneMsg}
		}
		return nil, err
	})
	updates("POST /v1/updates/run", func(u Updates, r *http.Request) (any, error) { return nil, u.Submit(r.Context()) })
	updates("POST /v1/restart", func(u Updates, r *http.Request) (any, error) { return nil, u.RestartNow(r.Context()) })
}
