package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/sleep"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/web"
)

// The controller's parts that its wiring (serve.go) doesn't need to spell
// out: its lab API, how it opens backup repositories, and its recurring
// jobs.

// controllerAPI is the lab API the controller serves to its servers
// (SPEC.md §10.3): joining, assignments, certificate renewal, guests'
// requests forwarded by the server offering storage for others, and
// Roostack Cloud backup's short-lived keys (storageKeys: nil without
// Cloud), each server getting only its own folder's.
func controllerAPI(hub *labnet.Hub, guestHub *labnet.GuestHub, mgr *manager.Manager,
	storageKeys func(ctx context.Context, server string) (cloudapi.StorageKeys, error)) http.Handler {
	api := http.NewServeMux()
	api.Handle("POST /v1/join", hub.JoinHandler())
	// A server fetches its assignment when it starts (SPEC.md §10.3).
	// Everything else is for servers still in the lab, with the
	// certificate the controller last gave them (revocation, renewal).
	api.Handle("GET /v1/assignment", hub.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := mgr.PendingFor(labnet.Caller(r))
		switch {
		case errors.Is(err, manager.ErrPullTooSoon):
			labnet.WriteError(w, http.StatusTooManyRequests, err)
			return
		case err != nil:
			labnet.WriteError(w, http.StatusNotFound, errors.New("no assignment for this server"))
			return
		}
		labnet.WriteJSON(w, http.StatusOK, a)
	})))
	api.Handle("POST /v1/renew", hub.Require(hub.RenewHandler()))
	api.Handle("POST "+labnet.PathStorageKeys, hub.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if storageKeys == nil {
			labnet.WriteError(w, http.StatusNotFound, errors.New("this lab isn't linked to Roostack Cloud"))
			return
		}
		k, err := storageKeys(r.Context(), labnet.Caller(r))
		if err != nil {
			labnet.WriteError(w, http.StatusBadGateway, err)
			return
		}
		labnet.WriteJSON(w, http.StatusOK, k)
	})))
	// Guests' requests, forwarded by the server offering storage for others.
	guestAPI := http.StripPrefix("/v1/guest-api", guestHub.Handler())
	api.Handle("/v1/guest-api/", hub.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if labnet.Caller(r) != mgr.Guests().GuestServer(r.Context()) {
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
		repo := &backup.Repo{Bin: bin, URL: url, PasswordFile: backup.KeyFile(lab.Paths), Env: env,
			Hints: backup.Hints(lab.Paths)}
		// A folder (a drive) is written outside Roostack's sandbox, as backups are.
		if strings.HasPrefix(url, "/") {
			repo.Launch = unshared(userLaunch)
		}
		return repo
	}
}

// startControllerJobs runs the controller's recurring work until ctx ends.
func startControllerJobs(ctx context.Context, mgr *manager.Manager, coord *maintenance.Coordinator, st *store.Store, nodeID string, log *slog.Logger) {
	// Weekly, the controller removes old backups from every server's
	// repository (servers may only add backups), first removing files
	// there restic can't read, which would stop the prune (quarantine.go),
	// at its own few minutes past the hour (maintenance.Offset).
	go maintenance.Every(ctx,
		func(now time.Time) time.Time {
			return maintenance.NextWeekly(now, maintenance.DefaultMaintenanceOn,
				maintenance.DefaultMaintenanceAt+maintenance.Offset(nodeID))
		},
		func() { coord.Submit(mgr.JunkJob(nil), mgr.PruneJob()) })
	// Nightly, after each server's backup: services whose update policy
	// takes their update (SPEC.md §3.2, §7). At 02:00 itself, before any
	// server's backup starts (each starts within maintenance.Spread), since
	// it waits for backups started after it.
	go maintenance.Every(ctx,
		func(now time.Time) time.Time { return maintenance.NextDaily(now, maintenance.DefaultBackupAt) },
		func() { go mgr.AutoUpdate(ctx, time.Now().Add(-time.Minute)) })
	// Hourly, from the start: hear from labs storing this one's backups (a
	// removal, a new name), and delete removed guest labs' backups once
	// their grace ends.
	go func() {
		for {
			// A recovery key change waiting for a backup that couldn't be
			// reached (SPEC.md §6.9) tries again.
			mgr.KeyChanges().ResumeKeyChange()
			mgr.CheckRemotes(ctx)
			// A backup that failed as junk in its repository makes it fail
			// gets that repository checked now, not at the weekly check.
			mgr.CheckForJunk(ctx)
			if err := mgr.Guests().PurgeRemovedGuests(ctx, time.Now()); err != nil {
				log.Error("deleting removed guest labs' backups", "err", err)
			}
			if !sleep.For(ctx, time.Hour) {
				return
			}
		}
	}()
	// Direct links between servers that don't connect fall back to the
	// controller (SPEC.md §10.10).
	go sleep.Every(ctx, time.Minute, func() { mgr.CheckLinks(ctx) })
	go maintenance.Every(ctx, func(now time.Time) time.Time { return now.Add(time.Hour) }, func() {
		// Sessions past their expiry or the idle limit (SPEC.md §5).
		if err := st.DeleteExpiredSessions(ctx, web.IdleLimit(ctx, st)); err != nil {
			log.Error("cleaning sessions", "err", err)
		}
	})
}
