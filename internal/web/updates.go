package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/usererr"
)

// updatesView is the Settings page's "Updates and restarts" card.
type updatesView struct {
	Last    *osupdate.Result
	Pending bool
	Restart osupdate.RestartStatus
	Mode    string // config.RestartAuto or RestartNotify
	Hour    int
	Hours   []int
	// Minute is how far past the hour this server restarts
	// (maintenance.Offset).
	Minute   int
	ErrorMsg string
	// Activity is the live (or last) update run.
	Activity *activityView
	// Series is Roostack's own release series and any newer one (SPEC.md
	// §7); nil if it couldn't be read.
	Series *seriesView
}

// seriesView is Roostack's release series on the Updates page.
type seriesView struct {
	osupdate.Series
	// NoBackups: the server isn't backed up through Roostack, so moving to
	// a new series has no backup to go back to.
	NoBackups bool
}

func (s *Server) updatesView(r *http.Request, errMsg string) *updatesView {
	if s.updates(r) == nil || !auth.Can(currentUser(r).Role, auth.ManageHost) {
		return nil
	}
	last, err := s.updates(r).Last(r.Context())
	if err != nil {
		s.Log.Error("reading the last OS update", "err", err)
	}
	v := &updatesView{Last: last, Mode: s.Maintenance.Restart, Hour: s.Maintenance.Hour(), ErrorMsg: errMsg,
		Activity: s.activityFor(r, activity.OSUpdates)}
	if v.Pending, err = s.updates(r).Pending(r.Context()); err != nil {
		s.Log.Error("reading whether OS updates are queued", "err", err)
	}
	if v.Restart, err = s.updates(r).RestartStatus(r.Context()); err != nil {
		s.Log.Error("reading whether a restart is needed", "err", err)
	}
	// Each server has its own policy.
	if m := s.server(r); m != nil {
		mode, hour, err := m.RestartPolicy(r.Context())
		if err != nil {
			s.Log.Error("reading the restart policy", "err", err)
		} else {
			v.Mode, v.Hour = mode, hour
		}
	}
	if a := v.Activity; a != nil && a.Running() {
		v.Pending = true // queued runs show as pending too
	}
	if v.Mode == "" {
		v.Mode = config.RestartAuto
	}
	if ser, err := s.updates(r).Series(r.Context()); err != nil {
		s.Log.Error("reading Roostack's release series", "err", err)
	} else {
		v.Series = &seriesView{Series: ser, NoBackups: true}
		if n := s.serverOf(r); n != nil && n.Backups() != nil {
			if st, err := n.Backups().Status(r.Context()); err == nil {
				v.Series.NoBackups = !st.Configured
			}
		}
	}
	for h := range 24 {
		v.Hours = append(v.Hours, h)
	}
	if n := s.serverOf(r); n != nil {
		v.Minute = restartMinute(n.Identity().ID)
	}
	return v
}

// restartMinute is how far past the chosen hour the server with node ID
// id restarts (maintenance.Offset).
func restartMinute(id string) int { return int(maintenance.Offset(id) / time.Minute) }

func (s *Server) updateNowSubmit(w http.ResponseWriter, r *http.Request) {
	if s.updates(r) == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.updates(r).Submit(r.Context()); err != nil && !errors.Is(err, osupdate.ErrAlreadyQueued) {
		s.serverError(w, err)
		return
	}
	m := s.serverRefOf(r)
	s.Store.Audit(r.Context(), currentUser(r), "updates", "OS updates started on "+m.InSentence())
	redirect(w, r, m.Updates())
}

// seriesAcceptSubmit is the Owner's go-ahead to move a server to a new
// release series of Roostack (SPEC.md §7): its next OS updates install it.
func (s *Server) seriesAcceptSubmit(w http.ResponseWriter, r *http.Request) {
	if s.updates(r) == nil {
		http.NotFound(w, r)
		return
	}
	series := r.FormValue("series")
	err := s.updates(r).AcceptSeries(r.Context(), series)
	if errors.Is(err, osupdate.ErrNoSuchSeries) {
		s.renderUpdates(w, r, node.SeriesGoneMsg)
		return
	}
	if msg, ok := usererr.Message(err); ok {
		s.renderUpdates(w, r, msg)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	m := s.serverRefOf(r)
	s.Store.Audit(r.Context(), currentUser(r), "updates", "Roostack "+series+" accepted for "+m.InSentence())
	redirect(w, r, m.Updates())
}

func (s *Server) restartNowSubmit(w http.ResponseWriter, r *http.Request) {
	if s.updates(r) == nil {
		http.NotFound(w, r)
		return
	}
	err := s.updates(r).RestartNow(r.Context())
	var busy osupdate.ErrBusy
	if errors.As(err, &busy) {
		s.renderUpdates(w, r, "Roostack can't restart right now: "+busy.Error()+".")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	m := s.serverRefOf(r)
	s.Store.Audit(r.Context(), currentUser(r), "restart", m.Label()+" restarted")
	if !m.This {
		// This UI stays up; the other server comes back on its own.
		redirect(w, r, "/servers")
		return
	}
	s.render(w, "restarting", map[string]any{"User": currentUser(r), "BootID": s.bootID, "Host": true})
}

func (s *Server) restartPolicySubmit(w http.ResponseWriter, r *http.Request) {
	hour, err := strconv.Atoi(r.FormValue("hour"))
	if err != nil {
		hour = -1
	}
	if s.server(r) == nil {
		http.NotFound(w, r)
		return
	}
	err = s.server(r).SetRestartPolicy(r.Context(), r.FormValue("restart"), hour)
	if msg, ok := usererr.Message(err); ok {
		s.renderUpdates(w, r, msg)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	m := s.serverRefOf(r)
	s.Store.Audit(r.Context(), currentUser(r), "setup", "restart policy changed on "+m.InSentence())
	if !m.This {
		redirect(w, r, m.Updates())
		return
	}
	redirect(w, r, "/restarting")
}
