package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/registry"
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

// registryReason is the registry's reason as the end of a sentence.
func registryReason(w *manager.Withdrawal) string {
	return strings.TrimRight(strings.TrimSpace(w.Reason), ".") + "."
}

// withdrawalAttention adds a "Needs attention" item for each installed
// service whose version the registry withdrew. Stops are problems (and
// emailed like other problems); warnings, a stop the Owner turned off
// or overrode, and notices say what's wrong and what to do.
func (s *Server) withdrawalAttention(ctx context.Context, services []ServiceView, l *attentionList) {
	ws := s.Manager.Withdrawals(ctx)
	for _, svc := range services {
		w := ws[svc.Name]
		if w == nil {
			continue
		}
		link := "/services/" + svc.Name + "#withdrawn"
		item := attentionItem{Link: link, Key: "withdrawn:" + svc.Name + ":" + w.Version + ":" + w.Shown}
		switch {
		case w.Stopped:
			item.Level, item.LinkText = levelProblem, "See what to do"
			item.Text = "Roostack stopped " + svc.Title + ": the service registry says the version it has is dangerous. Its data is kept."
		case w.StartedAnyway:
			item.Level, item.LinkText = levelWarning, "View"
			item.Text = svc.Title + " is running a version the service registry says is dangerous: you started it anyway."
		case w.Level == registry.LevelStop:
			item.Level, item.LinkText = levelWarning, "What to do"
			item.Text = "The service registry says the version of " + svc.Title + " you have is dangerous: " + registryReason(w) +
				" Roostack would stop it, but you've turned that off."
		case w.Shown == registry.LevelWarning:
			item.Level, item.LinkText = levelWarning, "What to do"
			item.Text = "The version of " + svc.Title + " you have has been withdrawn by the service registry: " + registryReason(w)
		default:
			item.Level, item.LinkText = levelInfo, "Read it"
			item.Text = "The service registry has a notice about " + svc.Title + " " + w.Version + ": " + registryReason(w)
		}
		l.add(item)
	}
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
