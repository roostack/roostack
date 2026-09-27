package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/osupdate"
	"github.com/mylab/mylab/internal/setup"
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
	if s.updates() == nil || !auth.Can(currentUser(r).Role, auth.ManageHost) {
		return nil
	}
	last, err := s.updates().Last(r.Context())
	if err != nil {
		s.Log.Error("reading the last OS update", "err", err)
	}
	v := &updatesView{Last: last, Pending: s.updates().Pending(), Restart: s.updates().RestartStatus(),
		Mode: s.Maintenance.Restart, Hour: s.Maintenance.Hour(), ErrorMsg: errMsg,
		Activity: s.activityFor(r, activity.OSUpdates)}
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
	if s.updates() == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.updates().Submit(); err != nil && !errors.Is(err, osupdate.ErrAlreadyQueued) {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "updates", "OS updates started")
	redirect(w, r, "/settings/updates")
}

func (s *Server) restartNowSubmit(w http.ResponseWriter, r *http.Request) {
	if s.updates() == nil {
		http.NotFound(w, r)
		return
	}
	err := s.updates().RestartNow(r.Context())
	var busy osupdate.ErrBusy
	if errors.As(err, &busy) {
		s.renderUpdates(w, r, "MyLab can't restart right now: "+busy.Error()+".")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "restart", "machine restarted")
	s.render(w, "restarting", map[string]any{"User": currentUser(r), "BootID": s.bootID, "Host": true})
}

func (s *Server) restartPolicySubmit(w http.ResponseWriter, r *http.Request) {
	hour, err := strconv.Atoi(r.FormValue("hour"))
	if err != nil {
		hour = -1
	}
	err = s.Setup.SaveRestartPolicy(r.Context(), r.FormValue("restart"), hour)
	var ce *setup.CheckError
	if errors.As(err, &ce) {
		s.renderUpdates(w, r, ce.Msg)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "restart policy changed")
	redirect(w, r, "/restarting")
}
