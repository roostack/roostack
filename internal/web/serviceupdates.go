package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
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

// StatusUpdating is a service updating to a newer version.
const StatusUpdating = "updating"

func init() {
	templateFuncs["appVersion"] = appVersion
	// sentence ends an error's text with a full stop.
	templateFuncs["sentence"] = func(s string) string { return strings.TrimRight(strings.TrimSpace(s), ".") + "." }
	templateFuncs["revision"] = func(v string) string { return strings.TrimPrefix(v[len(upstream(v)):], "-roostack.") }
	templateFuncs["updateKind"] = updateKindText
	templateFuncs["updatePolicyText"] = updatePolicyText
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

// updateShown is how long the service page says a service was updated.
const updateShown = 7 * 24 * time.Hour

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
	if rec := s.Manager.LastUpdate(r.Context(), name); rec != nil && lastUpdateShown(rec, u) {
		data["LastUpdate"] = rec
	}
}

// lastUpdateShown says whether a service's last update is still news: a
// success for a week; a failure while that version is still on offer, or
// for a week if nothing newer has replaced it.
func lastUpdateShown(rec *manager.UpdateRecord, u *manager.Update) bool {
	recent := time.Since(rec.Finished) < updateShown
	if rec.Result == manager.UpdateDone {
		return recent
	}
	return (u != nil && u.To == rec.To) || (u == nil && recent)
}

// updateAttention adds "Needs attention" items for updates: a failed one
// is a problem (emailed like the others); the services with an update
// waiting for a person share one line.
func (s *Server) updateAttention(r *http.Request, services []ServiceView, add func(item attentionItem)) {
	ups := s.Manager.AvailableUpdates(r.Context())
	ws := s.Manager.Withdrawals(r.Context())
	var waiting []string
	link := ""
	for _, svc := range services {
		if s.Manager.Updating(svc.Name) != nil {
			continue
		}
		u := ups[svc.Name]
		if rec := s.Manager.LastUpdate(r.Context(), svc.Name); rec != nil && rec.Result != manager.UpdateDone &&
			lastUpdateShown(rec, u) {
			text := "Updating " + svc.Title + " to " + appVersion(rec.To, rec.From) + " didn't work"
			if rec.Result == manager.UpdateRolledBack {
				text += "; Roostack went back to " + appVersion(rec.From, rec.To) + "."
			} else {
				text += "."
			}
			add(attentionItem{Level: levelProblem, Text: text, Link: "/services/" + svc.Name + "#update",
				LinkText: "See what happened", Key: "update:" + svc.Name + ":" + rec.To})
			continue
		}
		if u == nil || ws[svc.Name] != nil {
			continue // a withdrawal says it already, with the update
		}
		waiting = append(waiting, svc.Title+" ("+u.ToApp()+")")
		link = "/services/" + svc.Name + "#update"
	}
	switch len(waiting) {
	case 0:
		return
	case 1:
		add(attentionItem{Level: levelInfo, Text: "An update is available for " + waiting[0] + ".", Link: link, LinkText: "See it"})
	default:
		add(attentionItem{Level: levelInfo, Text: "Updates are available for " + joinAnd(waiting) + ".", Link: "/services",
			LinkText: "See them"})
	}
}

// upstream is the app's own version in a package version.
func upstream(v string) string {
	if i := strings.LastIndex(v, "-roostack."); i >= 0 {
		return v[:i]
	}
	return v
}

// appVersion is the app's version in a package version, with the
// package revision when other (the version it updated from or to) has
// the same app version, so the two never read the same.
func appVersion(v string, other ...string) string {
	if len(other) == 0 || upstream(v) != upstream(other[0]) {
		return upstream(v)
	}
	return upstream(v) + ", package revision " + strings.TrimPrefix(v[len(upstream(v)):], "-roostack.")
}

// joinAnd joins names as "a, b and c".
func joinAnd(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// updateSubmit starts updating a service (Admins).
func (s *Server) updateSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
	name, to := r.PathValue("name"), r.FormValue("to")
	// Only the Owner accepts what a package of their own asks for, as when
	// installing (SPEC.md §8.3.4).
	accept := r.FormValue("accept_risks") == "on" && auth.Can(currentUser(r).Role, auth.ManageHost)
	err := s.Manager.UpdateService(r.Context(), name, to, currentUser(r).Username, accept)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if msg, ok := userMessage(err); ok {
		s.renderServiceWith(w, r, map[string]any{"UpdateError": msg}, true)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "services", "update "+name+" to "+to)
	redirect(w, r, "/services/"+name+"#update")
}

// updatePolicies are the choices on the service page, in order.
var updatePolicies = []string{"notify", "patch", "minor", "all"}

// updatePolicySubmit saves how a service updates (Admins).
func (s *Server) updatePolicySubmit(w http.ResponseWriter, r *http.Request) {
	if s.Manager == nil {
		http.NotFound(w, r)
		return
	}
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
