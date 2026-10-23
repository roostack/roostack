package web

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/usererr"
)

// ServiceUpdates are updates of installed services to newer versions
// from the service registry (SPEC.md §7).
type ServiceUpdates interface {
	// AvailableUpdates are by service name; services with none are left out.
	AvailableUpdates(ctx context.Context) map[string]*manager.Update
	// UpdateService starts updating a service to version to, in the
	// background; by is who asked. acceptRisks: the Owner accepted what a
	// newer version of their own package asks for.
	UpdateService(ctx context.Context, name, to, by string, acceptRisks bool) error
	// Updating is a service's update queued or in progress (nil if none).
	Updating(name string) *manager.ServiceUpdate
	// LastUpdate is a service's last update (nil if none).
	LastUpdate(ctx context.Context, name string) *manager.UpdateRecord
	// UpdatePolicy is how a service updates (instance.Updates*).
	UpdatePolicy(name string) string
	SetUpdatePolicy(ctx context.Context, name, policy string) error
}

// updatePolicyText says how a service updates (its update policy), as
// its settings page offers it.
func updatePolicyText(policy string) string {
	switch policy {
	case "patch":
		return "Fix releases automatically"
	case "minor":
		return "Fix and feature releases automatically"
	case "all":
		return "Every update automatically"
	}
	return "Tell me, and I'll update it"
}

// updateKindText says what kind of update it is (pkgspec.UpdateKind).
func updateKindText(kind string) string {
	switch kind {
	case "revision":
		return "an improved Roostack package for the same version"
	case "patch":
		return "a fix release"
	case "minor":
		return "a feature release"
	case "major":
		return "a new major version"
	}
	return "a new version"
}

// addUpdateData adds a service's update, its progress and its last
// update to its page's data.
func (s *Server) addUpdateData(r *http.Request, data map[string]any, name string) {
	data["UpdatePolicy"] = s.Manager.UpdatePolicy(name)
	if u := s.Manager.Updating(name); u != nil {
		data["Updating"] = u
		if a := s.activityFor(r, u.Progress.Log); a != nil {
			data["UpdateActivity"] = a
		}
	} else if r.URL.Query().Get("updating") == "1" && r.Header.Get("HX-Request") == "true" {
		// Done: reload the page for its new status and notices.
		data["UpdateDone"] = true
	}
	u := s.Manager.AvailableUpdates(r.Context())[name]
	if u != nil {
		data["Update"] = u
	}
	if rec := s.Manager.LastUpdate(r.Context(), name); rec != nil && rec.Shown(u) {
		data["LastUpdate"] = rec
	}
}

// updateSubmit starts updating a service (Admins).
func (s *Server) updateSubmit(w http.ResponseWriter, r *http.Request) {
	name, to := r.PathValue("name"), r.FormValue("to")
	// Only the Owner accepts what a package of their own asks for, as when
	// installing (SPEC.md §8.3.4).
	accept := r.FormValue("accept_risks") == "on" && auth.Can(currentUser(r).Role, auth.ManageHost)
	err := s.Manager.UpdateService(r.Context(), name, to, currentUser(r).Username, accept)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if msg, ok := usererr.Message(err); ok {
		s.renderServiceWith(w, r, map[string]any{"UpdateError": msg}, true)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	detail := "update " + name + " to " + to
	if accept {
		detail += ", accepting what the Owner's own package asks for" // SPEC.md §8.3.4
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", detail)
	redirect(w, r, "/services/"+name+"#update")
}

// updatePolicies are the choices on the service page, in order.
var updatePolicies = []string{"notify", "patch", "minor", "all"}

// updatePolicySubmit saves how a service updates (Admins).
func (s *Server) updatePolicySubmit(w http.ResponseWriter, r *http.Request) {
	name, policy := r.PathValue("name"), r.FormValue("updates")
	if !slices.Contains(updatePolicies, policy) {
		http.Error(w, "unknown update policy", http.StatusBadRequest)
		return
	}
	err := s.Manager.SetUpdatePolicy(r.Context(), name, policy)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", name+" updates: "+policy)
	redirect(w, r, "/services/"+name)
}
