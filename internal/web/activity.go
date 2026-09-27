package web

import (
	"net/http"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/auth"
)

// activityPerm is who may see each kind of activity.
var activityPerm = map[string]auth.Permission{
	activity.OSUpdates:   auth.ManageHost,
	activity.Restore:     auth.ManageHost,
	activity.BackupSetup: auth.ManageHost,
	activity.Backup:      auth.ManageServices,
	activity.Apply:       auth.ManageServices,
	activity.Setup:       auth.ManageServices,
}

// activityView is one activity's live status and log (parts.html).
type activityView struct {
	Kind string
	// Machine is the ID of the machine it ran on, if not this one.
	Machine string
	activity.Snapshot
	// Example is set in the design gallery: it never polls.
	Example bool
}

// activityFor returns kind's latest activity for the page, or nil if
// there is none or the user may not see it.
func (s *Server) activityFor(r *http.Request, kind string) *activityView {
	perm, ok := activityPerm[kind]
	if !ok || !auth.Can(currentUser(r).Role, perm) {
		return nil
	}
	n := s.machineOf(r)
	if n == nil {
		return nil
	}
	snap, ok := n.LatestActivity(kind)
	if !ok {
		return nil
	}
	v := &activityView{Kind: kind, Snapshot: snap}
	// Polling asks the same machine.
	if m := s.machineViewOf(r); !m.This {
		v.Machine = m.ID
	}
	return v
}

// activityPart serves an activity's status line or log while it runs.
// When it has finished, the status response tells the page (HX-Trigger
// "activity-done") so cards showing results can refresh.
func (s *Server) activityPart(w http.ResponseWriter, r *http.Request) {
	v := s.activityFor(r, r.PathValue("kind"))
	part := map[string]string{"status": "activity-status", "log": "activity-log"}[r.PathValue("part")]
	if v == nil || part == "" {
		http.NotFound(w, r)
		return
	}
	if part == "activity-status" && !v.Running() && r.URL.Query().Get("was") == "running" {
		w.Header().Set("HX-Trigger", "activity-done")
	}
	w.Header().Set("Cache-Control", "no-store")
	s.renderPart(w, "settings", part, v)
}
