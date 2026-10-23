package web

import (
	"context"
	"errors"
	"net/http"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/usererr"
)

// Withdrawals are the service registry's withdrawals of installed
// services' versions, and what Roostack does about them (SPEC.md §8.3.4).
type Withdrawals interface {
	// Withdrawals are by service name; services with none are left out.
	Withdrawals(ctx context.Context) map[string]*manager.Withdrawal
	// StartAnyway runs a service Roostack stopped, making its public
	// addresses and ports private first if private is set.
	StartAnyway(ctx context.Context, name string, private bool) error
	// StopsOn is the Owner's opt-out in Settings → Updates.
	StopsOn() bool
	SetStops(on bool) error
}

// startAnywaySubmit starts a service Roostack stopped, once the Owner has
// confirmed the risk.
func (s *Server) startAnywaySubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	private := r.FormValue("private") == "1"
	err := s.Manager.StartAnyway(r.Context(), name, private)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if msg, ok := usererr.Message(err); ok {
		s.renderServiceWith(w, r, map[string]any{"Error": msg}, true)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	what := "started " + name + " though the service registry says its version is dangerous"
	if private {
		what += ", making it private"
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", what)
	redirect(w, r, "/services/"+name)
}

// registryStopsSubmit saves the Owner's choice whether Roostack may stop
// a service the registry says is dangerous.
func (s *Server) registryStopsSubmit(w http.ResponseWriter, r *http.Request) {
	on := r.FormValue("on") == "1"
	if err := s.Manager.SetStops(on); err != nil {
		s.serverError(w, err)
		return
	}
	what := "turned off stopping services the registry says are dangerous"
	if on {
		what = "turned on stopping services the registry says are dangerous"
	}
	s.Store.Audit(r.Context(), currentUser(r), "updates", what)
	redirect(w, r, "/settings/updates#registry")
}
