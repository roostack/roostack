package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/mylab/mylab/internal/labnet"
	"github.com/mylab/mylab/internal/osupdate"
)

// Server serves a server's operations to its controller over MyLab's
// network (SPEC.md §10.3). Only the controller's certificate is accepted.
type Server struct {
	Node         Node
	ControllerID string
	// Saved is called with every new assignment (without its secrets),
	// so the node can keep running it after a restart (optional).
	Saved func(WireAssignment)
	// Leave takes the server out of the lab, after the reply has gone
	// (optional).
	Leave func()
	// Rename saves the server's new name (optional).
	Rename func(name string) error

	mu      sync.Mutex
	version int64
}

// Version is the assignment version the node has.
func (s *Server) Version() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// Take applies a wire assignment if it's newer than the node's.
func (s *Server) Take(w WireAssignment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w.Version <= s.version {
		return nil
	}
	a, err := Decode(w)
	if err != nil {
		return err
	}
	s.version = w.Version
	s.Node.Apply(a)
	if s.Saved != nil {
		w.Secrets = nil // never on disk
		s.Saved(w)
	}
	return nil
}

// Handler returns the node API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	n := s.Node
	handle := func(pattern string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if labnet.Caller(r) != s.ControllerID {
				labnet.WriteError(w, http.StatusForbidden, errors.New("only the lab's controller may call this server"))
				return
			}
			out, err := fn(r)
			if err != nil {
				writeErr(w, err)
				return
			}
			if out == nil {
				out = map[string]bool{"ok": true}
			}
			labnet.WriteJSON(w, http.StatusOK, out)
		})
	}
	decode := func(r *http.Request, v any) error {
		return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 32<<20)).Decode(v)
	}
	ctx := func(r *http.Request) context.Context { return r.Context() }
	svc := func(r *http.Request) string { return r.PathValue("service") }

	handle("GET /v1/status", func(r *http.Request) (any, error) {
		st := n.Status()
		w := WireStatus{Running: st.Running, LastRun: st.LastRun, Finished: st.Finished, Version: s.Version(),
			Backups: n.Backups() != nil, Restores: n.Restores() != nil, Updates: n.Updates() != nil,
			Settings: n.Settings() != nil}
		if st.LastErr != nil {
			w.LastErr = st.LastErr.Error()
		}
		return w, nil
	})
	handle("PUT /v1/assignment", func(r *http.Request) (any, error) {
		var wa WireAssignment
		if err := decode(r, &wa); err != nil {
			return nil, err
		}
		return nil, s.Take(wa)
	})
	handle("GET /v1/services", func(r *http.Request) (any, error) { return n.ServiceStates(ctx(r)) })
	handle("GET /v1/services/details", func(r *http.Request) (any, error) { return n.ServiceDetails(ctx(r)) })
	handle("GET /v1/builtins", func(r *http.Request) (any, error) { return n.BuiltIns(ctx(r)) })
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
		var req struct{ Name string }
		if err := decode(r, &req); err != nil {
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

	// Package hooks.
	handle("GET /v1/services/{service}/setup", func(r *http.Request) (any, error) {
		return n.InstallStatus(ctx(r), svc(r)), nil
	})
	handle("POST /v1/services/{service}/setup/retry", func(r *http.Request) (any, error) {
		return nil, n.RetryInstall(ctx(r), svc(r))
	})
	handle("POST /v1/services/{service}/actions/{action}", func(r *http.Request) (any, error) {
		var inputs map[string]string
		if err := decode(r, &inputs); err != nil {
			return nil, err
		}
		out, err := n.RunAction(ctx(r), svc(r), r.PathValue("action"), inputs)
		return map[string]string{"output": out}, err
	})
	handle("POST /v1/services/{service}/secrets/{key}", func(r *http.Request) (any, error) {
		v, err := n.RevealSecret(ctx(r), svc(r), r.PathValue("key"))
		return map[string]string{"value": v}, err
	})

	// Maintenance.
	backups := func() (Backups, error) {
		if b := n.Backups(); b != nil {
			return b, nil
		}
		return nil, ErrNotFound
	}
	handle("GET /v1/backups", func(r *http.Request) (any, error) {
		b, err := backups()
		if err != nil {
			return nil, err
		}
		return b.Status(ctx(r))
	})
	handle("POST /v1/backups/run", func(r *http.Request) (any, error) {
		b, err := backups()
		if err != nil {
			return nil, err
		}
		return nil, b.RunNow()
	})
	restores := func() (Restores, error) {
		if x := n.Restores(); x != nil {
			return x, nil
		}
		return nil, ErrNotFound
	}
	handle("GET /v1/restores/pending", func(r *http.Request) (any, error) {
		x, err := restores()
		if err != nil {
			return nil, err
		}
		return x.Pending(), nil
	})
	handle("GET /v1/restores/{service}/snapshots", func(r *http.Request) (any, error) {
		x, err := restores()
		if err != nil {
			return nil, err
		}
		return x.Snapshots(ctx(r), svc(r))
	})
	handle("POST /v1/restores/{service}", func(r *http.Request) (any, error) {
		x, err := restores()
		if err != nil {
			return nil, err
		}
		var req struct{ Snapshot string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, x.Restore(svc(r), req.Snapshot)
	})
	handle("GET /v1/restores/{service}/kept", func(r *http.Request) (any, error) {
		x, err := restores()
		if err != nil {
			return nil, err
		}
		return x.Kept(svc(r))
	})
	handle("POST /v1/restores/{service}/kept/remove", func(r *http.Request) (any, error) {
		x, err := restores()
		if err != nil {
			return nil, err
		}
		var req struct{ Path string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, x.RemoveKept(ctx(r), svc(r), req.Path)
	})
	updates := func() (Updates, error) {
		if u := n.Updates(); u != nil {
			return u, nil
		}
		return nil, ErrNotFound
	}
	handle("GET /v1/updates", func(r *http.Request) (any, error) {
		u, err := updates()
		if err != nil {
			return nil, err
		}
		last, err := u.Last(ctx(r))
		if err != nil {
			return nil, err
		}
		return wireUpdates{Last: last, Pending: u.Pending(), Restart: u.RestartStatus()}, nil
	})
	handle("GET /v1/host/changes", func(r *http.Request) (any, error) {
		u, err := updates()
		if err != nil {
			return nil, err
		}
		return u.HostChanges(ctx(r))
	})
	handle("POST /v1/host/changes/known", func(r *http.Request) (any, error) {
		u, err := updates()
		if err != nil {
			return nil, err
		}
		return nil, u.AcknowledgeHostChanges(ctx(r))
	})
	handle("POST /v1/updates/run", func(r *http.Request) (any, error) {
		u, err := updates()
		if err != nil {
			return nil, err
		}
		return nil, u.Submit()
	})
	handle("POST /v1/restart", func(r *http.Request) (any, error) {
		u, err := updates()
		if err != nil {
			return nil, err
		}
		return nil, u.RestartNow(ctx(r))
	})

	// Moving a service's data (move.go).
	mover := func() (Mover, error) {
		if mv, ok := n.(Mover); ok {
			return mv, nil
		}
		return nil, ErrNotFound
	}
	dirOf := func(r *http.Request) int {
		d, err := strconv.Atoi(r.URL.Query().Get("dir"))
		if err != nil {
			return -1
		}
		return d
	}
	// Streams: tar out, tar in.
	stream := func(pattern string, fn func(w http.ResponseWriter, r *http.Request, mv Mover) error) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if labnet.Caller(r) != s.ControllerID {
				labnet.WriteError(w, http.StatusForbidden, errors.New("only the lab's controller may call this server"))
				return
			}
			mv, err := mover()
			if err == nil {
				err = fn(w, r, mv)
			}
			if err != nil {
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
		return mv.ExportData(ctx(r), svc(r), dirOf(r), since, w)
	})
	stream("PUT /v1/services/{service}/data", func(w http.ResponseWriter, r *http.Request, mv Mover) error {
		strip, _ := strconv.Atoi(r.URL.Query().Get("strip"))
		if err := mv.ImportData(ctx(r), svc(r), dirOf(r), r.Body, strip); err != nil {
			return err
		}
		labnet.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return nil
	})
	handle("GET /v1/services/{service}/data/list", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		return mv.ListData(ctx(r), svc(r), dirOf(r))
	})
	handle("POST /v1/services/{service}/data/prune", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		var keep []string
		if err := decode(r, &keep); err != nil {
			return nil, err
		}
		return nil, mv.PruneData(ctx(r), svc(r), dirOf(r), keep)
	})
	handle("POST /v1/services/{service}/data/commit", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		return mv.CommitData(ctx(r), svc(r))
	})
	handle("POST /v1/services/{service}/data/set-aside", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		var req struct{ Note string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return mv.SetAsideData(ctx(r), svc(r), req.Note)
	})
	handle("GET /v1/services/{service}/data/size", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		return mv.DataSize(ctx(r), svc(r))
	})
	handle("POST /v1/services/{service}/data/load-dumps", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		return nil, mv.LoadDumps(ctx(r), svc(r))
	})
	handle("GET /v1/kept", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		return mv.KeptData(ctx(r))
	})
	handle("POST /v1/kept/remove", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		var req struct{ Path string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, mv.RemoveKeptData(ctx(r), req.Path)
	})
	handle("POST /v1/services/{service}/stop", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		return nil, mv.StopService(ctx(r), svc(r))
	})
	handle("POST /v1/services/{service}/start", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		return nil, mv.StartService(ctx(r), svc(r))
	})
	handle("GET /v1/services/{service}/secrets", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		return mv.ExportSecrets(ctx(r), svc(r))
	})
	handle("PUT /v1/secrets", func(r *http.Request) (any, error) {
		mv, err := mover()
		if err != nil {
			return nil, err
		}
		var secrets map[string][]byte
		if err := decode(r, &secrets); err != nil {
			return nil, err
		}
		return nil, mv.ImportSecrets(ctx(r), secrets)
	})

	// The server's own settings.
	settings := func() (Settings, error) {
		if x := n.Settings(); x != nil {
			return x, nil
		}
		return nil, ErrNotFound
	}
	handle("GET /v1/settings", func(r *http.Request) (any, error) {
		x, err := settings()
		if err != nil {
			return nil, err
		}
		zone, zones, err := x.Timezones()
		if err != nil {
			return nil, err
		}
		ssh, err := x.SSHAccess()
		mode, hour := x.RestartPolicy()
		return wireSettings{Timezone: zone, Zones: zones, SSH: ssh, RestartMode: mode, RestartHour: hour}, err
	})
	handle("PUT /v1/settings/timezone", func(r *http.Request) (any, error) {
		x, err := settings()
		if err != nil {
			return nil, err
		}
		var req struct{ Timezone string }
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		changed, err := x.SetTimezone(ctx(r), req.Timezone)
		return map[string]bool{"changed": changed}, err
	})
	handle("PUT /v1/settings/ssh", func(r *http.Request) (any, error) {
		x, err := settings()
		if err != nil {
			return nil, err
		}
		var req struct {
			Choice    string
			Confirmed bool
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, x.SetSSH(ctx(r), req.Choice, req.Confirmed)
	})
	handle("PUT /v1/settings/backup-copies", func(r *http.Request) (any, error) {
		x, err := settings()
		if err != nil {
			return nil, err
		}
		var copies []BackupCopy
		if err := decode(r, &copies); err != nil {
			return nil, err
		}
		return nil, x.SetBackupCopies(ctx(r), copies)
	})
	handle("PUT /v1/settings/backup-destination", func(r *http.Request) (any, error) {
		x, err := settings()
		if err != nil {
			return nil, err
		}
		var d BackupDestination
		if err := decode(r, &d); err != nil {
			return nil, err
		}
		return nil, x.SetBackupDestination(ctx(r), d)
	})
	handle("PUT /v1/settings/restart-policy", func(r *http.Request) (any, error) {
		x, err := settings()
		if err != nil {
			return nil, err
		}
		var req struct {
			Mode string
			Hour int
		}
		if err := decode(r, &req); err != nil {
			return nil, err
		}
		return nil, x.SetRestartPolicy(ctx(r), req.Mode, req.Hour)
	})
	return mux
}

type wireUpdates struct {
	Last    *osupdate.Result
	Pending bool
	Restart osupdate.RestartStatus
}

type wireSettings struct {
	Timezone    string
	Zones       []string
	SSH         SSHAccess
	RestartMode string
	RestartHour int
}

// writeErr sends an error with its kind, so the controller can rebuild it.
func writeErr(w http.ResponseWriter, err error) {
	var fe *FormError
	var ae *ActionError
	switch {
	case errors.Is(err, ErrNotFound):
		labnet.WriteJSON(w, http.StatusNotFound, labnet.RemoteError{Status: http.StatusNotFound, Msg: err.Error(), Kind: kindNotFound})
	case errors.As(err, &fe):
		labnet.WriteJSON(w, http.StatusBadRequest, labnet.RemoteError{Status: http.StatusBadRequest, Msg: fe.Msg, Kind: kindForm})
	case errors.As(err, &ae):
		labnet.WriteJSON(w, http.StatusUnprocessableEntity, labnet.RemoteError{Status: http.StatusUnprocessableEntity,
			Msg: ae.Err.Error(), Kind: kindAction, Title: ae.Title, Output: ae.Output})
	default:
		labnet.WriteError(w, http.StatusInternalServerError, err)
	}
}
