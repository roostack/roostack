package node

import (
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/usererr"
)

// Server serves a server's operations to its controller over Roostack's
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

// Handler returns the node API: each feature's routes are in its wire
// file, beside the controller's side of them (Remote).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	handle := s.handle(mux)
	s.labRoutes(handle)               // labwire.go
	s.hookRoutes(handle)              // hookwire.go
	s.backupRoutes(handle)            // backupwire.go
	s.osUpdateRoutes(handle)          // osupdatewire.go
	s.moveRoutes(mux, handle)         // movewire.go
	s.updateRoutes(handle)            // updatewire.go
	s.settingsRoutes(handle)          // settingswire.go
	s.hostRoutes(handle)              // hostwire.go
	s.guestHostRoutes(handle, decode) // guesthost.go
	s.mediaRoutes(handle, decode)     // media.go
	s.importRoutes(handle)            // importwire.go
	return mux
}

// route answers a lab API request with a value, sent as JSON, or an error
// (writeErr). No value and no error answers okReply.
type route = func(r *http.Request) (any, error)

// handleFunc adds a route to the lab API.
type handleFunc = func(pattern string, fn route)

// okReply is the answer to a request that has nothing more to say.
type okReply struct {
	OK bool `json:"ok"`
}

// handle returns the function that adds routes to mux: only the lab's
// controller may call them.
func (s *Server) handle(mux *http.ServeMux) handleFunc {
	return func(pattern string, fn route) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !s.fromController(w, r) {
				return
			}
			out, err := fn(r)
			if err != nil {
				writeErr(w, err)
				return
			}
			if out == nil {
				out = okReply{true}
			}
			labnet.WriteJSON(w, http.StatusOK, out)
		})
	}
}

// fromController reports whether the lab's controller made the request,
// and refuses it if not.
func (s *Server) fromController(w http.ResponseWriter, r *http.Request) bool {
	if labnet.Caller(r) != s.ControllerID {
		labnet.WriteError(w, http.StatusForbidden, errors.New("only the lab's controller may call this server"))
		return false
	}
	return true
}

// capRoute returns a function that adds routes needing one of the node's
// capabilities: get finds it on each request, and a server without it
// answers "not found" (ErrNotFound).
func capRoute[T any](handle handleFunc, get func() (T, bool)) func(pattern string, fn func(x T, r *http.Request) (any, error)) {
	return func(pattern string, fn func(T, *http.Request) (any, error)) {
		handle(pattern, func(r *http.Request) (any, error) {
			x, ok := get()
			if !ok {
				return nil, ErrNotFound
			}
			return fn(x, r)
		})
	}
}

// offered is a capability a Node method returns, nil when the server
// doesn't have it (Backups, Mover...).
func offered[T any](get func() T) func() (T, bool) {
	return func() (T, bool) {
		x := get()
		return x, any(x) != nil
	}
}

// maxBody is the largest request body the lab API reads.
const maxBody = 32 << 20

// body decodes a request's JSON body.
func body[T any](r *http.Request) (T, error) {
	var v T
	err := decode(r, &v)
	return v, err
}

// decode decodes a request's JSON body into v.
func decode(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBody)).Decode(v)
}

// svcOf is the service a route names.
func svcOf(r *http.Request) string { return r.PathValue("service") }

// writeErr sends an error with its kind, so the controller can rebuild it.
func writeErr(w http.ResponseWriter, err error) {
	var fe *usererr.Error
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
