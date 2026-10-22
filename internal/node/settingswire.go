package node

import (
	"cmp"
	"context"
	"net/http"
)

// The server's own settings (Settings, settings.go) on the lab API.

// wireSettings is a server's settings at a glance: one read for the
// Settings page.
type wireSettings struct {
	Timezone    string
	Zones       []string
	SSH         SSHAccess
	RestartMode string
	RestartHour int
}

// timezoneRequest is the time zone to change to.
type timezoneRequest struct{ Timezone string }

// timezoneReply says whether the time zone changed.
type timezoneReply struct {
	Changed bool `json:"changed"`
}

// promoteRequest is the extra backup to make the main backup, and the
// main backup as an extra one (PromoteBackupCopy).
type promoteRequest struct {
	Name string     `json:"name"`
	Old  BackupCopy `json:"old"`
}

// restartPolicyRequest is when the server may restart after updates.
type restartPolicyRequest struct {
	Mode string
	Hour int
}

func (r *Remote) Settings() Settings {
	if st, err := r.wireStatus(); err != nil || !st.Settings {
		return nil
	}
	return remoteSettings{r}
}

type remoteSettings struct{ r *Remote }

func (x remoteSettings) get(ctx context.Context) (wireSettings, error) {
	var w wireSettings
	err := x.r.call(ctx, http.MethodGet, "/v1/settings", nil, &w)
	return w, err
}

func (x remoteSettings) Timezones(ctx context.Context) (string, []string, error) {
	w, err := x.get(ctx)
	return w.Timezone, w.Zones, err
}

func (x remoteSettings) SetTimezone(ctx context.Context, name string) (bool, error) {
	var out timezoneReply
	err := x.r.call(ctx, http.MethodPut, "/v1/settings/timezone", timezoneRequest{Timezone: name}, &out)
	return out.Changed, err
}

func (x remoteSettings) SSHAccess(ctx context.Context, fresh bool) (SSHAccess, error) {
	if fresh {
		var a SSHAccess
		err := x.r.call(ctx, http.MethodGet, "/v1/settings/ssh", nil, &a)
		return a, err
	}
	w, err := x.get(ctx)
	return w.SSH, err
}

func (x remoteSettings) SetSSH(ctx context.Context, c SSHChange) error {
	return x.r.call(ctx, http.MethodPut, "/v1/settings/ssh", c, nil)
}

func (x remoteSettings) PromoteBackupCopy(ctx context.Context, name string, old BackupCopy) error {
	return x.r.call(ctx, http.MethodPost, "/v1/settings/backup-copies/promote", promoteRequest{Name: name, Old: old}, nil)
}

func (x remoteSettings) SetBackupCopies(ctx context.Context, copies []BackupCopy) error {
	return x.r.call(ctx, http.MethodPut, "/v1/settings/backup-copies", copies, nil)
}

func (x remoteSettings) SetBackupDestination(ctx context.Context, d BackupDestination) error {
	return x.r.call(ctx, http.MethodPut, "/v1/settings/backup-destination", d, nil)
}

func (x remoteSettings) RestartPolicy(ctx context.Context) (string, int, error) {
	w, err := x.get(ctx)
	return w.RestartMode, w.RestartHour, err
}

func (x remoteSettings) SetRestartPolicy(ctx context.Context, mode string, hour int) error {
	return x.r.call(ctx, http.MethodPut, "/v1/settings/restart-policy", restartPolicyRequest{Mode: mode, Hour: hour}, nil)
}

// settingsRoutes serves Settings on the lab API.
func (s *Server) settingsRoutes(handle handleFunc) {
	settings := capRoute(handle, offered(s.Node.Settings))
	settings("GET /v1/settings", func(x Settings, r *http.Request) (any, error) {
		zone, zones, err := x.Timezones(r.Context())
		if err != nil {
			return nil, err
		}
		ssh, err := x.SSHAccess(r.Context(), false)
		mode, hour, perr := x.RestartPolicy(r.Context())
		return wireSettings{Timezone: zone, Zones: zones, SSH: ssh, RestartMode: mode, RestartHour: hour}, cmp.Or(err, perr)
	})
	settings("PUT /v1/settings/timezone", func(x Settings, r *http.Request) (any, error) {
		req, err := body[timezoneRequest](r)
		if err != nil {
			return nil, err
		}
		changed, err := x.SetTimezone(r.Context(), req.Timezone)
		return timezoneReply{Changed: changed}, err
	})
	// A fresh read of SSH (the remote access page), not cached like
	// /v1/settings.
	settings("GET /v1/settings/ssh", func(x Settings, r *http.Request) (any, error) { return x.SSHAccess(r.Context(), true) })
	settings("PUT /v1/settings/ssh", func(x Settings, r *http.Request) (any, error) {
		c, err := body[SSHChange](r)
		if err != nil {
			return nil, err
		}
		return nil, x.SetSSH(r.Context(), c)
	})
	settings("POST /v1/settings/backup-copies/promote", func(x Settings, r *http.Request) (any, error) {
		req, err := body[promoteRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, x.PromoteBackupCopy(r.Context(), req.Name, req.Old)
	})
	settings("PUT /v1/settings/backup-copies", func(x Settings, r *http.Request) (any, error) {
		copies, err := body[[]BackupCopy](r)
		if err != nil {
			return nil, err
		}
		return nil, x.SetBackupCopies(r.Context(), copies)
	})
	settings("PUT /v1/settings/backup-destination", func(x Settings, r *http.Request) (any, error) {
		d, err := body[BackupDestination](r)
		if err != nil {
			return nil, err
		}
		return nil, x.SetBackupDestination(r.Context(), d)
	})
	settings("PUT /v1/settings/restart-policy", func(x Settings, r *http.Request) (any, error) {
		req, err := body[restartPolicyRequest](r)
		if err != nil {
			return nil, err
		}
		return nil, x.SetRestartPolicy(r.Context(), req.Mode, req.Hour)
	})
}
