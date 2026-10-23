package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/sleep"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/systemd"
)

// serverJobs is this server's maintenance, whether it's the controller or
// another server (SPEC.md §3.2, §6.9): its coordinator, with the nightly
// backup and OS updates, weekly repository maintenance, and restores.
// start makes each part in turn, then runs them.
type serverJobs struct {
	lab   config.Lab
	st    *store.Store
	log   *slog.Logger
	agent *agent.Client
	acts  *activity.Registry
	// services are the services this server runs, which its backups and
	// restores cover.
	services  serverServices
	cloudKeys *cloudBackupKeys

	// Made by start's steps.
	ticker   *time.Ticker
	coord    *maintenance.Coordinator
	backups  *backup.Service
	restores *backup.Restores // nil until backups are set up
	updates  *osupdate.Service
}

// start makes this server's maintenance jobs and runs them until ctx
// ends.
func (j *serverJobs) start(ctx context.Context) (node.Maintenance, error) {
	j.newCoordinator()
	if err := j.newBackups(ctx); err != nil {
		return node.Maintenance{}, err
	}
	j.newUpdates()
	j.run(ctx)
	m := node.Maintenance{Backups: j.backups, Updates: j.updates, Coord: j.coord}
	if j.restores != nil {
		m.Restores = j.restores
	}
	return m, nil
}

// newCoordinator makes the coordinator, which runs one job at a time,
// in the maintenance window the Owner chose.
func (j *serverJobs) newCoordinator() {
	j.ticker = time.NewTicker(time.Minute)
	policy := maintenance.DefaultPolicy()
	policy.WindowStart = time.Duration(j.lab.Maintenance.Hour()) * time.Hour
	if j.lab.Maintenance.Restart == config.RestartNotify {
		policy.Mode = maintenance.RebootNotify
	}
	j.coord = &maintenance.Coordinator{
		Policy:  policy,
		Now:     time.Now,
		Journal: maintenance.FileJournal{Path: filepath.Join(j.lab.Paths.StateDir, "maintenance-journal.json")},
		Ticks:   j.ticker.C,
		Log:     j.log,
		Reboot:  func() error { return j.agent.Reboot(context.Background()) },
	}
}

// newBackups makes the backup job and, once backups are set up, restores.
func (j *serverJobs) newBackups(ctx context.Context) error {
	lab := j.lab
	// Backup runs recorded before they had repositories get theirs, once
	// (SPEC.md §6.9).
	repos := map[string]string{"": lab.Backup.Repository}
	for _, c := range lab.Backup.Copies {
		repos[c.Name] = c.Repository
	}
	if err := j.st.AdoptBackupRuns(ctx, repos); err != nil {
		j.log.Warn("giving older backup runs their repositories", "err", err)
	}
	svc := &backup.Service{
		Coord:      j.coord,
		Store:      j.st,
		Repository: lab.Backup.Repository,
		// Set up by the controller: it removes old backups (SPEC.md §10.10).
		PrunedElsewhere: lab.Backup.PrunedByController,
		NextRun:         func() time.Time { return maintenance.NextDaily(time.Now(), maintenance.DefaultBackupAt) },
		Activity:        j.acts,
	}
	j.backups = svc
	j.coord.Resume = func(name string) (maintenance.Job, bool) {
		if name == backup.JobName && svc.Configured() {
			return svc.Job(), true
		}
		return maintenance.Job{}, false
	}
	if lab.Backup.Repository == "" {
		return nil
	}
	if _, err := os.Stat(backup.KeyFile(lab.Paths)); err != nil {
		j.log.Warn("backups are configured but not initialised; run: roostack backup init")
		return nil
	}
	repo, err := newRepo(lab, userLaunch, j.cloudKeys)
	if err != nil {
		return err
	}
	if svc.Deps, err = newBackupDeps(lab, j.st, repo, userLaunch, j.services, j.cloudKeys); err != nil {
		return err
	}
	// Back up the WireGuard keys too, so a restored lab keeps the keys
	// its peers know.
	dumpDB := svc.Deps.DumpState
	svc.Deps.DumpState = func(ctx context.Context, path string) error {
		if j.agent.KeysFile != "" {
			if err := j.agent.SaveWireGuardKeys(ctx); err != nil {
				return fmt.Errorf("saving the WireGuard keys: %w", err)
			}
		}
		return dumpDB(ctx, path)
	}
	j.restores = &backup.Restores{Coord: j.coord, Repo: repo, Activity: j.acts,
		Restorer: &backup.Restorer{Repo: repo, Units: systemd.New(), Launch: userLaunch,
			RoostackBin: svc.Deps.Preparer.RoostackBin, Now: time.Now},
		Lookup: func(name string) (*generate.Output, string, error) { return lookupService(j.services, name) },
	}
	return nil
}

// newUpdates makes the OS updates job. A new release series of Roostack
// waits for the Owner, then for a backup that worked within the last day
// (SPEC.md §7).
func (j *serverJobs) newUpdates() {
	j.updates = &osupdate.Service{Agent: j.agent, Store: j.st, Coord: j.coord, Now: time.Now, Log: j.log, Activity: j.acts,
		HTTP: &http.Client{Timeout: time.Minute}}
	svc := j.backups
	j.updates.BackupCheck = func(ctx context.Context) string {
		if !svc.Configured() {
			return "" // the offer said there's no backup to go back to
		}
		bs, err := svc.Status(ctx)
		switch {
		case err != nil:
			return "Roostack couldn't read this server's backups"
		case bs.Last == nil || bs.Last.Finished.IsZero():
			return "there's no backup yet"
		case !bs.Last.OK:
			return "the last backup didn't work"
		case time.Since(bs.Last.Finished) > 26*time.Hour:
			return "the last backup is more than a day old"
		}
		return ""
	}
}

// run runs the coordinator and the jobs' schedules until ctx ends.
func (j *serverJobs) run(ctx context.Context) {
	svc, updates := j.backups, j.updates
	go func() {
		j.coord.Run(ctx)
		j.ticker.Stop()
	}()
	// Nightly: backup, then OS updates (SPEC.md §3.2). Updates are queued
	// separately, so a failing backup doesn't hold back security fixes.
	go maintenance.Every(ctx,
		func(now time.Time) time.Time { return maintenance.NextDaily(now, maintenance.DefaultBackupAt) },
		func() {
			if svc.Configured() {
				if err := svc.Submit(); err != nil {
					j.log.Warn("nightly backup not started", "err", err)
				}
			}
			if err := updates.Submit(ctx); err != nil {
				j.log.Warn("nightly OS updates not started", "err", err)
			}
		})
	go updates.WatchSeries(ctx, 5*time.Minute, 6*time.Hour)
	// A run of OS updates that upgraded Roostack restarted it halfway
	// through: record how that run ends (SPEC.md §7).
	updates.Resume(ctx)
	// Debian's own apt timers may also install an update that needs a
	// restart; notice it.
	updates.CheckReboot()
	go sleep.Every(ctx, 30*time.Minute, func() { updates.CheckReboot() })
	// Failed backups are tried again (backup/retry.go).
	go svc.WatchRetries(ctx)
	go j.catchUp(ctx)
	if svc.Configured() {
		go maintenance.Every(ctx,
			func(now time.Time) time.Time {
				return maintenance.NextWeekly(now, maintenance.DefaultMaintenanceOn, maintenance.DefaultMaintenanceAt)
			},
			func() { j.coord.Submit(svc.MaintenanceJob()) })
	}
}

// catchUp catches up a night the server was off, and makes a new lab's
// first backup: a while after starting (once the services have), then
// hourly (SPEC.md §6.9).
func (j *serverJobs) catchUp(ctx context.Context) {
	wait := catchUpDelay
	for {
		j.backups.CatchUpAt(time.Now().Add(wait))
		if !sleep.For(ctx, wait) {
			return
		}
		wait = time.Hour
		why, err := j.backups.CatchUp(ctx)
		switch {
		case err != nil:
			j.log.Warn("catching up a missed backup", "err", err)
		case why != "":
			j.log.Info("catching up a missed backup: " + why)
		}
	}
}

// catchUpDelay is how long after starting Roostack checks for a missed
// nightly backup.
const catchUpDelay = 10 * time.Minute

// lookupService generates an installed service's output (e.g. to restore
// it), with its title.
func lookupService(services serverServices, name string) (*generate.Output, string, error) {
	lab, list, err := services()
	if err != nil {
		return nil, "", err
	}
	for _, s := range list {
		if s.Name() == name {
			// What runs of it now: only its databases while it's held after
			// a restore that didn't finish, so restoring it again stops
			// and starts only units that exist (SPEC.md §6.9).
			out, err := state.Output(lab, s)
			return out, s.Title(), err
		}
	}
	return nil, "", fmt.Errorf("%s is not installed", name)
}
