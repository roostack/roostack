package web

import (
	"errors"
	"net/http"
	"strconv"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/osupdate"
)

// updatesView is the Settings page's "Updates and restarts" card.
type updatesView struct {
	Last     *osupdate.Result
	Pending  bool
	Restart  osupdate.RestartStatus
	Mode     string // config.RestartAuto or RestartNotify
	Hour     int
	Hours    []int
	ErrorMsg string
	// Activity is the live (or last) update run.
	Activity *activityView
}

func (s *Server) updatesView(r *http.Request, errMsg string) *updatesView {
	if s.updates(r) == nil || !auth.Can(currentUser(r).Role, auth.ManageHost) {
		return nil
	}
	last, err := s.updates(r).Last(r.Context())
	if err != nil {
		s.Log.Error("reading the last OS update", "err", err)
	}
	v := &updatesView{Last: last, Pending: s.updates(r).Pending(), Restart: s.updates(r).RestartStatus(),
		Mode: s.Maintenance.Restart, Hour: s.Maintenance.Hour(), ErrorMsg: errMsg,
		Activity: s.activityFor(r, activity.OSUpdates)}
	// Each server has its own policy.
	if m := s.server(r); m != nil {
		v.Mode, v.Hour = m.RestartPolicy()
	}
	if a := v.Activity; a != nil && a.Running() {
		v.Pending = true // queued runs show as pending too
	}
	if v.Mode == "" {
		v.Mode = config.RestartAuto
	}
	for h := range 24 {
		v.Hours = append(v.Hours, h)
	}
	return v
}

func (s *Server) updateNowSubmit(w http.ResponseWriter, r *http.Request) {
	if s.updates(r) == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.updates(r).Submit(); err != nil && !errors.Is(err, osupdate.ErrAlreadyQueued) {
		s.serverError(w, err)
		return
	}
	m := s.serverRefOf(r)
	s.Store.Audit(r.Context(), currentUser(r), "updates", "OS updates started on "+m.Label())
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
		redirect(w, r, "/settings/servers")
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
	if msg, ok := userMessage(err); ok {
		s.renderUpdates(w, r, msg)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	m := s.serverRefOf(r)
	s.Store.Audit(r.Context(), currentUser(r), "setup", "restart policy changed on "+m.Label())
	if !m.This {
		redirect(w, r, m.Updates())
		return
	}
	redirect(w, r, "/restarting")
}
