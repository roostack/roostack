package node

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/store"
)

// What the controller reads about a server on the lab API: its disks
// (disk.go), memory (memory.go), data services left behind (leftdata.go)
// and history (history.go).

// cullRequest forgets history from before a time (Unix seconds).
type cullRequest struct {
	Before int64 `json:"before"`
}

// cullReply is how many events were forgotten.
type cullReply struct{ Deleted int64 }

// DiskSpaces asks the server.
func (r *Remote) DiskSpaces(ctx context.Context) ([]DiskSpace, error) {
	var out []DiskSpace
	err := r.call(ctx, http.MethodGet, "/v1/disks", nil, &out)
	return out, err
}

// Memory asks the server.
func (r *Remote) Memory(ctx context.Context) (MemoryInfo, error) {
	var out MemoryInfo
	err := r.call(ctx, http.MethodGet, "/v1/memory", nil, &out)
	return out, err
}

// LeftData asks the server over the lab API.
func (r *Remote) LeftData(ctx context.Context, name string) ([]string, error) {
	var out []string
	err := r.call(ctx, http.MethodGet, servicePath(name, "/left-data"), nil, &out)
	// A server from before answers the route it doesn't know 404.
	if re := (*labnet.RemoteError)(nil); errors.As(err, &re) && re.Status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	return out, err
}

// History asks the server.
func (r *Remote) History(ctx context.Context, before time.Time, kind string, limit int) ([]store.Event, error) {
	q := url.Values{"kind": {kind}, "limit": {strconv.Itoa(limit)}}
	if !before.IsZero() {
		q.Set("before", strconv.FormatInt(before.Unix(), 10))
	}
	var out []store.Event
	err := r.call(ctx, http.MethodGet, "/v1/history?"+q.Encode(), nil, &out)
	return out, err
}

func (r *Remote) HistoryEvent(ctx context.Context, id int64) (store.Event, error) {
	var out store.Event
	err := r.call(ctx, http.MethodGet, "/v1/history/"+strconv.FormatInt(id, 10), nil, &out)
	return out, err
}

func (r *Remote) CullHistory(ctx context.Context, before time.Time) (int64, error) {
	var out cullReply
	err := r.call(ctx, http.MethodPost, "/v1/history/cull", cullRequest{Before: before.Unix()}, &out)
	return out.Deleted, err
}

// hostRoutes serves Disks, Memory, LeftData and History on the lab API.
func (s *Server) hostRoutes(handle handleFunc) {
	n := s.Node
	disks := capRoute(handle, implemented[Disks](n))
	disks("GET /v1/disks", func(d Disks, r *http.Request) (any, error) { return d.DiskSpaces(r.Context()) })
	memory := capRoute(handle, implemented[Memory](n))
	memory("GET /v1/memory", func(m Memory, r *http.Request) (any, error) { return m.Memory(r.Context()) })
	leftData := capRoute(handle, implemented[LeftData](n))
	leftData("GET /v1/services/{service}/left-data", func(l LeftData, r *http.Request) (any, error) {
		return l.LeftData(r.Context(), svcOf(r))
	})

	history := capRoute(handle, implemented[History](n))
	history("GET /v1/history", func(h History, r *http.Request) (any, error) {
		var before time.Time
		if v, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64); err == nil {
			before = time.Unix(v, 0)
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		return h.History(r.Context(), before, r.URL.Query().Get("kind"), min(max(limit, 1), 200))
	})
	history("GET /v1/history/{id}", func(h History, r *http.Request) (any, error) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			return nil, ErrNotFound
		}
		return h.HistoryEvent(r.Context(), id)
	})
	history("POST /v1/history/cull", func(h History, r *http.Request) (any, error) {
		req, err := body[cullRequest](r)
		if err != nil {
			return nil, err
		}
		deleted, err := h.CullHistory(r.Context(), time.Unix(req.Before, 0))
		return cullReply{Deleted: deleted}, err
	})
}
