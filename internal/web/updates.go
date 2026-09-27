package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/osupdate"
	"github.com/mylab/mylab/internal/setup"
)

// HostUpdates is what the UI needs from internal/osupdate.
type HostUpdates interface {
	Last(ctx context.Context) (*osupdate.Result, error)
	Pending() bool
	Submit() error
	RestartStatus() osupdate.RestartStatus
	RestartNow(ctx context.Context) error
}

// updatesView is the Settings page's "Updates & restarts" card.
type updatesView struct {
	Last     *osupdate.Result
	Pending  bool
	Restart  osupdate.RestartStatus
	Mode     string // config.RestartAuto or RestartNotify
	Hour     int
	Hours    []int
	ErrorMsg string
	// ActivityData is the live run, for the "activity" template.
	ActivityData map[string]any
}

func (s *Server) updatesView(r *http.Request, errMsg string) *updatesView {
	if s.Updates == nil || !auth.Can(currentUser(r).Role, auth.ManageHost) {
		return nil
	}
	last, err := s.Updates.Last(r.Context())
	if err != nil {
		s.Log.Error("reading the last OS update", "err", err)
	}
	v := &updatesView{Last: last, Pending: s.Updates.Pending(), Restart: s.Updates.RestartStatus(),
		Mode: s.Maintenance.Restart, Hour: s.Maintenance.Hour(), ErrorMsg: errMsg,
		ActivityData: map[string]any{"Activity": s.activityFor(r, activity.OSUpdates)}}
	if a := v.ActivityData["Activity"].(*activityView); a != nil && a.Running() {
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
	if s.Updates == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Updates.Submit(); err != nil && !errors.Is(err, osupdate.ErrAlreadyQueued) {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "updates", "OS updates started")
	redirect(w, r, "/settings#updates")
}

func (s *Server) restartNowSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Updates == nil {
		http.NotFound(w, r)
		return
	}
	err := s.Updates.RestartNow(r.Context())
	var busy osupdate.ErrBusy
	if errors.As(err, &busy) {
		s.renderSettings(w, r, "MyLab can't restart right now: "+busy.Error()+".")
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
		s.renderSettings(w, r, ce.Msg)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "setup", "restart policy changed")
	redirect(w, r, "/restarting")
}
