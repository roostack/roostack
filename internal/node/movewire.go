package node

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"roostack.cloud/roostack/internal/kept"
	"roostack.cloud/roostack/internal/labnet"
)

// Moving a service's data (Mover, move.go) on the lab API.

// dataPath is a route about a service's data folder dir (an index into
// DataDirs).
func dataPath(service, rest string, dir int) string {
	return servicePath(service, "/data"+rest) + "?dir=" + strconv.Itoa(dir)
}

// noteRequest says why a service's folders are set aside (SetAsideData).
type noteRequest struct{ Note string }

// commitRequest says why the data already there is kept (CommitData). A
// controller from before 2026-10-23 sends none: kept.BeforeMove.
type commitRequest struct{ Why kept.Why }

// Mover asks the server to move its services' data.
func (r *Remote) Mover() Mover { return r }

// streamClient is for data streams, which can take hours: no overall
// timeout (the caller's context stops them).
func (r *Remote) streamClient() *http.Client {
	return &http.Client{Transport: r.Client.Transport}
}

func (r *Remote) DataDirs(string) []string { return nil } // only the server itself knows

func (r *Remote) ExportData(ctx context.Context, service string, dir int, since time.Time, w io.Writer) error {
	u := dataPath(service, "", dir)
	if !since.IsZero() {
		u += "&since=" + strconv.FormatInt(since.Unix(), 10)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL+u, nil)
	if err != nil {
		return err
	}
	resp, err := r.streamClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return wireError(labnet.DecodeResponse(resp, nil))
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

func (r *Remote) ImportData(ctx context.Context, service string, dir int, body io.Reader, strip int) error {
	u := r.URL + dataPath(service, "", dir) + "&strip=" + strconv.Itoa(strip)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := r.streamClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return wireError(labnet.DecodeResponse(resp, nil))
}

func (r *Remote) ListData(ctx context.Context, service string, dir int) ([]string, error) {
	var out []string
	err := r.call(ctx, http.MethodGet, dataPath(service, "/list", dir), nil, &out)
	return out, err
}

func (r *Remote) PruneData(ctx context.Context, service string, dir int, keep []string) error {
	return r.call(ctx, http.MethodPost, dataPath(service, "/prune", dir), keep, nil)
}

func (r *Remote) CommitData(ctx context.Context, service string, why kept.Why) ([]string, error) {
	var out []string
	err := r.call(ctx, http.MethodPost, servicePath(service, "/data/commit"), commitRequest{Why: why}, &out)
	return out, err
}

func (r *Remote) SetAsideData(ctx context.Context, service, note string) ([]string, error) {
	var out []string
	err := r.call(ctx, http.MethodPost, servicePath(service, "/data/set-aside"), noteRequest{Note: note}, &out)
	return out, err
}

func (r *Remote) DataSize(ctx context.Context, service string) (int64, error) {
	var out int64
	err := r.call(ctx, http.MethodGet, servicePath(service, "/data/size"), nil, &out)
	return out, err
}

func (r *Remote) LoadDumps(ctx context.Context, service string) error {
	return r.call(ctx, http.MethodPost, servicePath(service, "/data/load-dumps"), struct{}{}, nil)
}

func (r *Remote) StopService(ctx context.Context, service string) error {
	return r.call(ctx, http.MethodPost, servicePath(service, "/stop"), struct{}{}, nil)
}

func (r *Remote) StartService(ctx context.Context, service string) error {
	return r.call(ctx, http.MethodPost, servicePath(service, "/start"), struct{}{}, nil)
}

func (r *Remote) ExportSecrets(ctx context.Context, service string) (map[string][]byte, error) {
	var out map[string][]byte
	err := r.call(ctx, http.MethodGet, servicePath(service, "/secrets"), nil, &out)
	return out, err
}

func (r *Remote) ImportSecrets(ctx context.Context, secrets map[string][]byte) error {
	return r.call(ctx, http.MethodPut, "/v1/secrets", secrets, nil)
}

// dirOf is the data folder a route names (-1: none, which the node
// refuses).
func dirOf(r *http.Request) int {
	d, err := strconv.Atoi(r.URL.Query().Get("dir"))
	if err != nil {
		return -1
	}
	return d
}

// moveRoutes serves Mover on the lab API. The data itself goes as a tar
// stream, out and in, straight on mux.
func (s *Server) moveRoutes(mux *http.ServeMux, handle handleFunc) {
	mover := offered(s.Node.Mover)
	stream := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, mv Mover) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !s.fromController(w, r) {
				return
			}
			mv, ok := mover()
			if !ok {
				writeErr(w, ErrNotFound)
				return
			}
			if err := fn(w, r, mv); err != nil {
				writeErr(w, err)
			}
		})
	}
	stream("GET /v1/services/{service}/data", func(w http.ResponseWriter, r *http.Request, mv Mover) error {
		var since time.Time
		if v := r.URL.Query().Get("since"); v != "" {
			sec, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return err
			}
			since = time.Unix(sec, 0)
		}
		w.Header().Set("Content-Type", "application/x-tar")
		// Errors after the stream started can only cut it short; the
		// receiving tar then fails, which fails the move.
		return mv.ExportData(r.Context(), svcOf(r), dirOf(r), since, w)
	})
	stream("PUT /v1/services/{service}/data", func(w http.ResponseWriter, r *http.Request, mv Mover) error {
		strip, _ := strconv.Atoi(r.URL.Query().Get("strip"))
		if err := mv.ImportData(r.Context(), svcOf(r), dirOf(r), r.Body, strip); err != nil {
			return err
		}
		labnet.WriteJSON(w, http.StatusOK, okReply{true})
		return nil
	})

	move := capRoute(handle, mover)
	move("GET /v1/services/{service}/data/list", func(mv Mover, r *http.Request) (any, error) {
		return mv.ListData(r.Context(), svcOf(r), dirOf(r))
	})
	move("POST /v1/services/{service}/data/prune", func(mv Mover, r *http.Request) (any, error) {
		keep, err := body[[]string](r)
		if err != nil {
			return nil, err
		}
		return nil, mv.PruneData(r.Context(), svcOf(r), dirOf(r), keep)
	})
	move("POST /v1/services/{service}/data/commit", func(mv Mover, r *http.Request) (any, error) {
		req, err := body[commitRequest](r)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return mv.CommitData(r.Context(), svcOf(r), req.Why)
	})
	move("POST /v1/services/{service}/data/set-aside", func(mv Mover, r *http.Request) (any, error) {
		req, err := body[noteRequest](r)
		if err != nil {
			return nil, err
		}
		return mv.SetAsideData(r.Context(), svcOf(r), req.Note)
	})
	move("GET /v1/services/{service}/data/size", func(mv Mover, r *http.Request) (any, error) {
		return mv.DataSize(r.Context(), svcOf(r))
	})
	move("POST /v1/services/{service}/data/load-dumps", func(mv Mover, r *http.Request) (any, error) {
		return nil, mv.LoadDumps(r.Context(), svcOf(r))
	})
	move("POST /v1/services/{service}/stop", func(mv Mover, r *http.Request) (any, error) {
		return nil, mv.StopService(r.Context(), svcOf(r))
	})
	move("POST /v1/services/{service}/start", func(mv Mover, r *http.Request) (any, error) {
		return nil, mv.StartService(r.Context(), svcOf(r))
	})
	move("GET /v1/services/{service}/secrets", func(mv Mover, r *http.Request) (any, error) {
		return mv.ExportSecrets(r.Context(), svcOf(r))
	})
	move("PUT /v1/secrets", func(mv Mover, r *http.Request) (any, error) {
		secrets, err := body[map[string][]byte](r)
		if err != nil {
			return nil, err
		}
		return nil, mv.ImportSecrets(r.Context(), secrets)
	})
	keptRoutes(move)
}
