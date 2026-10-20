package web

import (
	"net/http"
	"strings"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
)

// activityPerm is who may see each kind of activity.
var activityPerm = map[string]auth.Permission{
	activity.OSUpdates:   auth.ManageHost,
	activity.Restore:     auth.ManageHost,
	activity.BackupSetup: auth.ManageHost,
	activity.BackupKey:   auth.ManageHost,
	activity.Move:        auth.ManageHost,
	activity.Backup:      auth.ManageServices,
	activity.Apply:       auth.ManageServices,
	activity.Setup:       auth.ManageServices,
	activity.Build:       auth.ManageServices,
	// Updating a service (SPEC.md §7).
	activity.ServiceUpdate: auth.ManageServices,
	// Rolling backups back (SPEC.md §10.10).
	activity.BackupRollback: auth.ManageHost,
	// Bringing a service's data in (SPEC.md §8.4).
	activity.Import: auth.ManageHost,
	// A service restarted as its library's drive came back (SPEC.md §8.4).
	activity.LibraryRestart: auth.ManageServices,
}

// activityView is one activity's live status and log (parts.html).
type activityView struct {
	Kind string
	// Server is the ID of the server it ran on, if not this one.
	Server string
	activity.Snapshot
	// Example is set in the design gallery: it never polls.
	Example bool
}

// activityFor returns kind's latest activity for the page, or nil if
// there is none or the user may not see it.
func (s *Server) activityFor(r *http.Request, kind string) *activityView {
	base, forServer, _ := strings.Cut(kind, ".")
	perm, ok := activityPerm[base]
	if !ok || !auth.Can(currentUser(r).Role, perm) || (forServer != "" && base != activity.BackupSetup && base != activity.BackupKey && base != activity.BackupRollback && base != activity.Import) {
		return nil
	}
	n := s.serverOf(r)
	if forServer != "" {
		// Kept by the controller, for the server (or account) named in
		// the kind.
		n = s.Node
	}
	if n == nil {
		return nil
	}
	snap, ok := n.LatestActivity(kind)
	if !ok {
		return nil
	}
	v := &activityView{Kind: kind, Snapshot: snap}
	// Polling asks the same server.
	if m := s.serverRefOf(r); !m.This && forServer == "" {
		v.Server = m.ID
	}
	return v
}

// activityPart serves an activity's status line or log while it runs.
// When it has finished, the status response tells the page (HX-Trigger
// "activity-done") so cards showing results can refresh.
func (s *Server) activityPart(w http.ResponseWriter, r *http.Request) {
	v := s.activityFor(r, r.PathValue("kind"))
	part := map[string]string{"status": "activity-status", "log": "activity-log"}[r.PathValue("part")]
	if v == nil && part == "activity-status" && r.URL.Query().Get("was") == "running" {
		// The log went with a restart (logs live in memory), often one the
		// activity caused itself, like a backup copy's new settings. Not
		// found would leave the page's last answer spinning (htmx doesn't
		// swap an error): the page shows what's true now instead.
		w.Header().Set("HX-Refresh", "true")
		return
	}
	if v == nil || part == "" {
		http.NotFound(w, r)
		return
	}
	if part == "activity-status" && !v.Running() && r.URL.Query().Get("was") == "running" {
		w.Header().Set("HX-Trigger", "activity-done")
	}
	// 204 while it shows the same: redrawing would reset the spinner
	// and lose a selection every 2 seconds.
	s.renderLive(w, r, "settings", part, v)
}
