package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/hosttime"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/labrestore"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/osupdate"
	"roostack.cloud/roostack/internal/podman"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/systemd"
	"roostack.cloud/roostack/internal/web"
)

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	listen := fs.String("listen", "", "address to listen on (default <ui_bind>:<ui_port> from lab config)")
	fs.Parse(args)

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	lab, err := config.Load(*labPath)
	if err != nil {
		return err
	}
	lab.HasGPU = config.DetectGPU()
	id, err := withNode(&lab)
	if err != nil {
		return err
	}
	lab.Timezone = hosttime.Zone()
	// Restarting after a lab restore: its database replaces the new one.
	if restored, err := labrestore.StartFromBackup(lab.Paths); err != nil {
		return err
	} else if restored {
		log.Info("starting as the restored lab", "domain", lab.Domain)
	}
	st, err := store.Open(dbPath(lab))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// New settings reach every part of Roostack by restarting it; systemd
	// starts it again (Restart=always).
	// A running job (e.g. OS updates) finishes first, and no new one
	// starts; the restart then happens by itself.
	var coord *maintenance.Coordinator
	restartNow := func() {
		go func() {
			time.Sleep(time.Second) // let the response reach the browser
			if coord != nil {
				coord.Hold()
				for st := coord.Status(); st.Running != nil; st = coord.Status() {
					log.Info("restart waits for a job to finish", "job", st.Running.Name)
					time.Sleep(5 * time.Second)
				}
			}
			log.Info("restarting to apply new settings")
			stop()
		}()
	}
	// During the setup wizard, settings wait for one restart at its end
	// (the Owner shouldn't watch Roostack restart after every step).
	// It's set further down, but restart is handed to goroutines (the
	// hub, the node) before that, hence the atomic.
	var wizard atomic.Pointer[setup.Setup]
	var restartPending atomic.Bool
	restart := func() {
		if w := wizard.Load(); w != nil {
			if p, err := w.Progress(context.Background()); err == nil && p.Next() != "" {
				if !restartPending.Swap(true) {
					log.Info("restart postponed until setup is finished")
				}
				return
			}
		}
		restartNow()
	}
	local, agentClient, coord, err := newLocal(ctx, lab, *labPath, id, st, log, restart)
	if err != nil {
		return err
	}
	if lab.IsNode() {
		// Run by another server (the controller): no UI of its own.
		return serveNode(ctx, lab, local, agentClient, restart, log)
	}

	token, err := setupToken(ctx, lab, st, log)
	if err != nil {
		return err
	}
	var mgrRef *manager.Manager
	srv, err := web.NewServer(st, func(ctx context.Context) ([]web.ServiceView, error) {
		return serviceViews(ctx, lab, mgrRef, log)
	}, token, log)
	if err != nil {
		return err
	}
	srv.OnSetupComplete = func() { os.Remove(setupTokenPath(lab)) }
	srv.Version = version

	// The lab's other servers (SPEC.md §10), reached over its network.
	fleet := &node.Fleet{Self: labnet.Identity{Dir: labnet.IdentityDir(lab.Paths.StateDir)}, Lab: lab, Log: log,
		List: st.Servers}
	mgr := &manager.Manager{Lab: lab, Log: log, Node: local, Activity: local.Activity,
		Others: func() []manager.RemoteServer {
			var out []manager.RemoteServer
			for _, r := range fleet.Remotes() {
				out = append(out, r)
			}
			return out
		},
		UpdateLab: func(change func(*config.Lab)) error { return config.Update(*labPath, change) }}
	mgrRef = mgr
	srv.Manager, srv.Node, srv.Domain, srv.HasGPU, srv.BackupRepo = mgr, local, lab.Domain, lab.HasGPU, lab.Backup.Repository
	// Public access (SPEC.md §6.11): check links from a phone, and whether
	// CrowdSec is up (the records wait for it).
	crowdSecUp := func(ctx context.Context) bool {
		return public.CrowdSecUp(ctx, fmt.Sprintf("127.0.0.1:%d", config.CrowdSecPort))
	}
	srv.Checks = &public.Checks{Record: func(host string, at time.Time, from string) error {
		log.Info("public address checked from outside", "host", host, "from", from)
		return web.RecordPublicCheck(context.Background(), st, host, at, from)
	}}
	srv.Protected = crowdSecUp
	// Roostack's network between servers (SPEC.md §10.2): this server is
	// the hub others join.
	hub := &labnet.Hub{StateDir: lab.Paths.StateDir, LabName: lab.DisplayName(), Self: id, Store: st, Agent: agentClient,
		Log: log, Now: time.Now, Started: restart}
	hub.Start(ctx)
	srv.Network = hub
	mgr.Registry = hub
	// Storage for other labs (SPEC.md §10.10): this lab can keep other
	// labs' backups, and store its own at other labs.
	// The guests' link and stores are on whichever server offers the
	// storage; the controller keeps the records and answers guests (on
	// that server's link, forwarded here when it's another server).
	guestHub := &labnet.GuestHub{LabName: lab.DisplayName(), Store: st, Agent: mgr.GuestLinkAgent(), Log: log, Now: time.Now,
		Usage: func(ctx context.Context, g store.Guest) (int64, error) { return mgr.GuestUsage(ctx, g.User()) }}
	mgr.GuestHub, mgr.GuestRecords, mgr.LinkAgent = guestHub, st, agentClient
	guestHub.Start(ctx)
	go guestHub.ServeGuests(ctx)
	mgr.RemoteAdded = func(r manager.RemoteStorage) { go forwardToRemote(ctx, lab, r, log) }
	// Backups across servers (SPEC.md §10.10): the controller creates
	// each server's repository with the recovery key.
	mgr.BackupStore = st
	// This lab's storage at other labs: forward its servers' backups.
	for _, r := range mgr.RemoteStorages(ctx) {
		go forwardToRemote(ctx, lab, r, log)
	}
	if err := mgr.AdoptBackupDestination(ctx, lab.Backup.Repository); err != nil {
		log.Error("recording this server's backup destination", "err", err)
	}
	mgr.HasRecoveryKey = func() bool {
		_, err := os.Stat(backup.KeyFile(lab.Paths))
		return err == nil
	}
	mgr.EnsureRecoveryKey = func() error { return setup.EnsureRecoveryKey(lab.Paths) }
	mgr.OpenRepo = repoOpener(lab)
	mgr.Drives = agentClient
	mgr.RenameSelf = func(name string) error {
		self := id
		self.Name = name
		if err := self.Save(lab.Paths.StateDir); err != nil {
			return err
		}
		restart()
		return nil
	}
	go labnet.Serve(ctx, lab.Paths.StateDir, controllerAPI(hub, guestHub, mgr), log)
	go labnet.KeepRenewed(ctx, labnet.Identity{Dir: labnet.IdentityDir(lab.Paths.StateDir)}, id.ID, hub.RenewSelf, log)
	startControllerJobs(ctx, mgr, coord, st, log)
	srv.Maintenance = lab.Maintenance
	srv.RunningJob = func() string {
		if st := coord.Status(); st.Running != nil {
			return st.Running.Name
		}
		return ""
	}
	// Converge on startup, e.g. after Roostack itself was upgraded.
	mgr.Apply()
	// After a lab restore, the services' databases go back once they're set
	// up again.
	if r, ok := local.Jobs.Restores.(*backup.Restores); ok {
		assigned := func() (config.Lab, []state.Service, error) {
			l, services := local.Assigned()
			return l, services, nil
		}
		go finishLabRestore(lab, coord, r.Restorer, assigned, mgr.Wait, local.Activity, log)
	}

	wiz := &setup.Setup{
		Lab: lab, LabPath: *labPath, Store: st, Secrets: podman.NewSecrets(),

		Restart: restart,
		Drives:  agentClient,
		// The wizard's end restarts Roostack if a step asked for it.
		RestartPending: restartPending.Load,
		Finished: func() {
			if restartPending.Load() {
				restartNow()
			}
		},
	}
	wizard.Store(wiz)
	if err := wiz.AdoptExisting(ctx); err != nil {
		return err
	}
	srv.Setup, srv.DNSProvider, srv.LabName = wiz, lab.DNS.Provider, lab.DisplayName()
	srv.RestorePending = func() bool { _, ok := labrestore.Pending(lab.Paths); return ok }
	// A new install can instead become a lab restored from its backups.
	if token != "" {
		srv.LabRestore = &labRestorer{lab: lab, drives: agentClient, acts: local.Activity, restart: restartNow, log: log}
	}

	// Keep *.<domain> pointing at this server for devices at home, and
	// check they can see it (SPEC.md §6.5.1).
	if lab.DNS.Provider != "" {
		records := &dnsrecords.Manager{
			Lab: lab, Secrets: podman.NewSecrets(), Store: st, Log: log,
			HTTP:        &http.Client{Timeout: 30 * time.Second},
			LocalLookup: dnsrecords.SystemLookup, PublicLookup: dnsrecords.PublicLookup,
			Wait:      90 * time.Second,
			UpdateLab: func(change func(*config.Lab)) error { return config.Update(*labPath, change) },
			// Public addresses point at the home's public address (port
			// forwarding, the only entry so far), once CrowdSec is up.
			PublicHosts: mgr.PublicHosts, Entry: public.PortForwarding{}, Protected: crowdSecUp,
		}
		go records.Run(ctx, 30*time.Minute)
		srv.DNS = records
	}

	addr := *listen
	if addr == "" {
		addr = fmt.Sprintf("%s:%d", lab.UIBind, lab.UIPort)
	}
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
	}()
	log.Info("roostack listening", "addr", addr)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func dbPath(lab config.Lab) string { return filepath.Join(lab.Paths.StateDir, "roostack.db") }

func setupTokenPath(lab config.Lab) string { return filepath.Join(lab.Paths.StateDir, "setup-token") }

// setupToken returns the first-run setup code, or "" if an account exists.
// The code is kept in a file readable only by roostack (and root), so it
// survives restarts until setup is done.
func setupToken(ctx context.Context, lab config.Lab, st *store.Store, log *slog.Logger) (string, error) {
	path := setupTokenPath(lab)
	n, err := st.CountUsers(ctx)
	if err != nil || n > 0 {
		if n > 0 { // e.g. a lab restored from its backups
			os.Remove(path)
		}
		return "", err
	}
	if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		token := strings.TrimSpace(string(data))
		log.Info("waiting for first-run setup", "setup_code", token, "file", path)
		return token, nil
	}
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	log.Info("waiting for first-run setup", "setup_code", token, "file", path)
	return token, nil
}

// serviceViews builds the dashboard list from desired state plus live
// systemd status.
func serviceViews(ctx context.Context, lab config.Lab, mgr *manager.Manager, log *slog.Logger) ([]web.ServiceView, error) {
	all, err := state.LoadServices(lab)
	if err != nil {
		return nil, err
	}
	// Each server reports its own services (SPEC.md §10.3).
	infos := mgr.ServiceInfos(ctx)
	var views []web.ServiceView
	for _, s := range all {
		v := web.ServiceView{Name: s.Package.Name, Title: s.Package.Title, Status: "unknown"}
		if info, ok := infos[s.Package.Name]; ok {
			v.ServiceDetail, v.Status, v.Server, v.LastBackup = info.ServiceDetail, info.Status, info.Server, info.LastBackup
		}
		out, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			log.Error("generating service for status", "service", s.Package.Name, "err", err)
			v.Status = "failed"
			views = append(views, v)
			continue
		}
		for _, h := range out.Hosts {
			v.URLs = append(v.URLs, "https://"+h)
		}
		views = append(views, v)
	}
	return views, nil
}

// startMaintenance runs this server's maintenance coordinator with the
// nightly backup and OS updates and weekly repository maintenance
// (SPEC.md §3.2, §6.9).
func startMaintenance(ctx context.Context, lab config.Lab, st *store.Store, log *slog.Logger, ag *agent.Client,
	acts *activity.Registry, services serverServices) (node.Maintenance, *maintenance.Coordinator, error) {
	ticker := time.NewTicker(time.Minute)
	policy := maintenance.DefaultPolicy()
	policy.WindowStart = time.Duration(lab.Maintenance.Hour()) * time.Hour
	if lab.Maintenance.Restart == config.RestartNotify {
		policy.Mode = maintenance.RebootNotify
	}
	coord := &maintenance.Coordinator{
		Policy:  policy,
		Now:     time.Now,
		Journal: maintenance.FileJournal{Path: filepath.Join(lab.Paths.StateDir, "maintenance-journal.json")},
		Ticks:   ticker.C,
		Log:     log,
		Reboot:  func() error { return ag.Reboot(context.Background()) },
	}
	jobs := node.Maintenance{}
	updates := &osupdate.Service{Agent: ag, Store: st, Coord: coord, Now: time.Now, Log: log, Activity: acts}
	svc := &backup.Service{
		Coord:      coord,
		Store:      st,
		Repository: lab.Backup.Repository,
		// Set up by the controller: it removes old backups (SPEC.md §10.10).
		PrunedElsewhere: lab.Backup.PrunedByController,
		NextRun:         func() time.Time { return maintenance.NextDaily(time.Now(), maintenance.DefaultBackupAt) },
		Activity:        acts,
	}
	if lab.Backup.Repository != "" {
		if _, err := os.Stat(backup.KeyFile(lab.Paths)); err != nil {
			log.Warn("backups are configured but not initialised; run: roostack backup init")
		} else {
			repo, err := newRepo(lab, userLaunch)
			if err != nil {
				return jobs, nil, err
			}
			if svc.Deps, err = newBackupDeps(lab, st, repo, userLaunch, services); err != nil {
				return jobs, nil, err
			}
			// Back up the WireGuard keys too, so a restored lab keeps the keys
			// its peers know.
			dumpDB := svc.Deps.DumpState
			svc.Deps.DumpState = func(ctx context.Context, path string) error {
				if ag.KeysFile != "" {
					if err := ag.SaveWireGuardKeys(ctx); err != nil {
						return fmt.Errorf("saving the WireGuard keys: %w", err)
					}
				}
				return dumpDB(ctx, path)
			}
			jobs.Restores = &backup.Restores{Coord: coord, Repo: repo, Activity: acts,
				Restorer: &backup.Restorer{Repo: repo, Units: systemd.New(), Launch: userLaunch,
					RoostackBin: svc.Deps.Preparer.RoostackBin, Now: time.Now},
				Lookup: func(name string) (*generate.Output, string, error) { return lookupService(services, name) },
			}
		}
	}
	coord.Resume = func(name string) (maintenance.Job, bool) {
		if name == backup.JobName && svc.Configured() {
			return svc.Job(), true
		}
		return maintenance.Job{}, false
	}
	go func() {
		coord.Run(ctx)
		ticker.Stop()
	}()
	// Nightly: backup, then OS updates (SPEC.md §3.2). Updates are queued
	// separately, so a failing backup doesn't hold back security fixes.
	go maintenance.Every(ctx,
		func(now time.Time) time.Time { return maintenance.NextDaily(now, maintenance.DefaultBackupAt) },
		func() {
			if svc.Configured() {
				if err := svc.Submit(); err != nil {
					log.Warn("nightly backup not started", "err", err)
				}
			}
			if err := updates.Submit(); err != nil {
				log.Warn("nightly OS updates not started", "err", err)
			}
		})
	// Debian's own apt timers may also install an update that needs a
	// restart; notice it.
	updates.CheckReboot()
	go func() {
		t := time.NewTicker(30 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				updates.CheckReboot()
			}
		}
	}()
	if svc.Configured() {
		go maintenance.Every(ctx,
			func(now time.Time) time.Time {
				return maintenance.NextWeekly(now, maintenance.DefaultMaintenanceOn, maintenance.DefaultMaintenanceAt)
			},
			func() { coord.Submit(svc.MaintenanceJob()) })
	}
	jobs.Backups, jobs.Updates = svc, updates
	return jobs, coord, nil
}

// lookupService generates an installed service's output (e.g. to restore
// it), with its title.
func lookupService(services serverServices, name string) (*generate.Output, string, error) {
	lab, list, err := services()
	if err != nil {
		return nil, "", err
	}
	for _, s := range list {
		if s.Package.Name == name {
			out, err := generate.Service(lab, s.Package, s.Instance)
			return out, s.Package.Title, err
		}
	}
	return nil, "", fmt.Errorf("%s is not installed", name)
}
