package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/web"
)

// The controller's parts that serve doesn't need to spell out: its lab
// API, how it opens backup repositories, and its recurring jobs.

// controllerAPI is the lab API the controller serves to its servers
// (SPEC.md §10.3): joining, assignments, certificate renewal, and guests'
// requests forwarded by the server offering storage for others.
func controllerAPI(hub *labnet.Hub, guestHub *labnet.GuestHub, mgr *manager.Manager) http.Handler {
	api := http.NewServeMux()
	api.Handle("POST /v1/join", hub.JoinHandler())
	// A server fetches its assignment when it starts (SPEC.md §10.3).
	// Everything else is for servers still in the lab, with the
	// certificate the controller last gave them (revocation, renewal).
	api.Handle("GET /v1/assignment", hub.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := mgr.PendingFor(labnet.Caller(r))
		if !ok {
			labnet.WriteError(w, http.StatusNotFound, errors.New("no assignment for this server"))
			return
		}
		labnet.WriteJSON(w, http.StatusOK, a)
	})))
	api.Handle("POST /v1/renew", hub.Require(hub.RenewHandler()))
	// Guests' requests, forwarded by the server offering storage for others.
	guestAPI := http.StripPrefix("/v1/guest-api", guestHub.Handler())
	api.Handle("/v1/guest-api/", hub.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if labnet.Caller(r) != mgr.GuestServer(r.Context()) {
			labnet.WriteError(w, http.StatusForbidden, errors.New("this server doesn't offer storage for others"))
			return
		}
		guestAPI.ServeHTTP(w, r)
	})))
	return api
}

// repoOpener opens a backup repository with the recovery key, for the
// controller's work on every server's backups (creating, pruning).
func repoOpener(lab config.Lab) func(url string, env []string) *backup.Repo {
	return func(url string, env []string) *backup.Repo {
		bin, err := exec.LookPath("restic")
		if err != nil {
			bin = "restic"
		}
		repo := &backup.Repo{Bin: bin, URL: url, PasswordFile: backup.KeyFile(lab.Paths), Env: env}
		// A folder (a drive) is written outside Roostack's sandbox, as backups are.
		if strings.HasPrefix(url, "/") {
			repo.Launch = unshared(userLaunch)
		}
		return repo
	}
}

// startControllerJobs runs the controller's recurring work until ctx ends.
func startControllerJobs(ctx context.Context, mgr *manager.Manager, coord *maintenance.Coordinator, st *store.Store, log *slog.Logger) {
	// Weekly, the controller removes old backups from every server's
	// repository (servers may only add backups).
	go maintenance.Every(ctx,
		func(now time.Time) time.Time {
			return maintenance.NextWeekly(now, maintenance.DefaultMaintenanceOn, maintenance.DefaultMaintenanceAt)
		},
		func() { coord.Submit(mgr.PruneJob()) })
	// Hourly, from the start: hear from labs storing this one's backups (a
	// removal, a new name), and delete removed guest labs' backups once
	// their grace ends.
	go func() {
		for {
			// A recovery key change waiting for a backup that couldn't be
			// reached (SPEC.md §6.9) tries again.
			mgr.ResumeKeyChange()
			mgr.CheckRemotes(ctx)
			if err := mgr.PurgeRemovedGuests(ctx, time.Now()); err != nil {
				log.Error("deleting removed guest labs' backups", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Hour):
			}
		}
	}()
	go maintenance.Every(ctx, func(now time.Time) time.Time { return now.Add(time.Hour) }, func() {
		// Sessions past their expiry or the idle limit (SPEC.md §5).
		if err := st.DeleteExpiredSessions(ctx, web.IdleLimit(ctx, st)); err != nil {
			log.Error("cleaning sessions", "err", err)
		}
	})
}
