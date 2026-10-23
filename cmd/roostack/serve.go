package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/cloud"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/hosttime"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/labrestore"
	"roostack.cloud/roostack/internal/mail"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/podman"
	"roostack.cloud/roostack/internal/public"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/setup"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
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
	// On a lab on the internet, an SSH tunnel's requests reach Caddy from
	// this address, which private addresses allow (SPEC.md §6.12).
	lab.HostIP = waitForNetwork(log)
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
	st, err := store.Open(lab.Paths.DatabaseFile())
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	restarts := &restarter{log: log, stop: stop}
	cloudKeys := &cloudBackupKeys{}
	local, agentClient, coord, err := newLocal(ctx, lab, *labPath, id, st, log, restarts.restart, cloudKeys)
	if err != nil {
		return err
	}
	restarts.coord.Store(coord)
	if lab.IsNode() {
		// Run by another server (the controller): no UI of its own, so no
		// setup pages either (one that joined from them leaves their
		// certificate).
		if err := web.RemoveSetupCert(lab.Paths); err != nil {
			log.Error("deleting the setup pages' certificate", "err", err)
		}
		return serveNode(ctx, lab, local, agentClient, restarts.restart, cloudKeys, log)
	}
	c := &controller{ctx: ctx, lab: lab, labPath: *labPath, id: id, st: st, log: log, local: local, agent: agentClient,
		coord: coord, restarts: restarts, cloudKeys: cloudKeys}
	return c.run(*listen)
}

// restarter restarts Roostack, which is how new settings reach every part
// of it: systemd starts it again (Restart=always).
type restarter struct {
	log *slog.Logger
	// stop ends serve.
	stop func()
	// coord is this server's maintenance coordinator, once it's made: a
	// running job (e.g. OS updates) finishes first, and no new one
	// starts; the restart then happens by itself.
	coord atomic.Pointer[maintenance.Coordinator]
	// wizard is the setup wizard, once it's ready. During it, settings wait
	// for one restart at its end (pending): the Owner shouldn't watch
	// Roostack restart after every step. restart is handed to goroutines
	// (the hub, the node) before the wizard is ready, hence the atomics.
	wizard  atomic.Pointer[setup.Setup]
	pending atomic.Bool
}

// now restarts Roostack in a moment, once a running job has finished.
func (r *restarter) now() {
	go func() {
		time.Sleep(time.Second) // let the response reach the browser
		if coord := r.coord.Load(); coord != nil {
			coord.Hold()
			for st := coord.Status(); st.Running != nil; st = coord.Status() {
				r.log.Info("restart waits for a job to finish", "job", st.Running.Name)
				time.Sleep(5 * time.Second)
			}
		}
		r.log.Info("restarting to apply new settings")
		r.stop()
	}()
}

// restart restarts Roostack now, or at the end of the setup wizard while
// it lasts.
func (r *restarter) restart() {
	if w := r.wizard.Load(); w != nil {
		if p, err := w.Progress(context.Background()); err == nil && p.Next() != "" {
			if !r.pending.Swap(true) {
				r.log.Info("restart postponed until setup is finished")
			}
			return
		}
	}
	r.now()
}

// controller is the lab's controller as serve wires it together: the web
// UI, the lab's other servers, Roostack Cloud and the rest. run takes it
// a step at a time, each wiring one part from those before it; the
// fields below the line are what the steps make.
type controller struct {
	ctx       context.Context
	lab       config.Lab
	labPath   string
	id        nodeid.Identity
	st        *store.Store
	log       *slog.Logger
	local     *node.Local
	agent     *agent.Client
	coord     *maintenance.Coordinator
	restarts  *restarter
	cloudKeys *cloudBackupKeys

	// token is the first-run setup code ("" once an account exists), and
	// pages the setup pages on the home network (nil once setup is done).
	token string
	pages *web.SetupListener
	mgr   *manager.Manager
	wiz   *setup.Setup
	srv   *web.Server
	// labName is the lab's name as it is now: the setup wizard names it
	// without a restart (it waits for the wizard's end, if anything asks
	// for one), so Cloud, the network's join codes and other labs' guests
	// would otherwise hear "My lab".
	labName func() string
	// link is Roostack Cloud (nil when it's turned off), and storageKeys
	// asks it for a server's Roostack Cloud backup keys.
	link        *cloud.Service
	storageKeys func(ctx context.Context, server string) (cloudapi.StorageKeys, error)
	// crowdSecUp says whether CrowdSec is up, which public addresses'
	// records wait for.
	crowdSecUp func(ctx context.Context) bool
	hub        *labnet.Hub
	guestHub   *labnet.GuestHub
}

// run wires the controller, then serves the web UI until ctx ends. The
// order matters: each step uses what the steps before it made, and some
// start work that the later ones mustn't miss.
func (c *controller) run(listen string) error {
	// The setup code and the setup pages come before the first apply,
	// whose firewall opens their port.
	if err := c.wireSetupPages(); err != nil {
		return err
	}
	c.wireManager()
	c.wireWizard()
	if err := c.wireWeb(); err != nil {
		return err
	}
	if err := c.wireCloud(); err != nil {
		return err
	}
	c.wirePublic()
	c.wireNetwork()
	c.wireBackups()
	// The lab API needs the manager's backups and the guests' hub.
	c.serveLabAPI()
	if err := c.wireCatalog(); err != nil {
		return err
	}
	// The first apply, and the controller's recurring jobs.
	c.startJobs()
	// The wizard is ready once the setup pages and the first apply are,
	// and Cloud's link starts once the wizard is (a roost.ac name goes to
	// the wizard).
	if err := c.wireSetup(); err != nil {
		return err
	}
	c.startCloud()
	c.wireNotifier()
	c.wireMonitoring()
	c.wireDNS()
	return c.listen(listen)
}

// wireSetupPages decides the setup code, and the setup pages on the home
// network (SPEC.md §6.3.2).
func (c *controller) wireSetupPages() error {
	var err error
	if c.token, err = web.SetupToken(c.ctx, c.st, c.lab.Paths.SetupTokenFile(), c.log); err != nil {
		return err
	}
	c.pages = setupPages(c.ctx, c.lab, c.st, c.token, c.local, c.log)
	return nil
}

// wireManager makes the manager, which runs services on the lab's
// servers (SPEC.md §10), reached over its network.
func (c *controller) wireManager() {
	lab, labPath := c.lab, c.labPath
	fleet := &node.Fleet{Self: labnet.Identity{Dir: labnet.IdentityDir(lab.Paths.StateDir)}, Lab: lab, Log: c.log,
		List: c.st.Servers}
	c.mgr = &manager.Manager{Lab: lab, Log: c.log, Version: version, Node: c.local, Activity: c.local.Activity,
		Others: func() []manager.RemoteServer {
			var out []manager.RemoteServer
			for _, r := range fleet.Remotes() {
				out = append(out, r)
			}
			return out
		},
		UpdateLab: func(change func(*config.Lab)) error { return config.Update(labPath, change) },
		Submit:    func(j maintenance.Job) { c.coord.Submit(j) },
		Settings:  c.st,
		Audit:     func(ctx context.Context, action, detail string) { c.st.Audit(ctx, nil, action, detail) }}
	c.mgr.RenameSelf = func(name string) error {
		self := c.id
		self.Name = name
		if err := self.Save(lab.Paths.StateDir); err != nil {
			return err
		}
		c.restarts.restart()
		return nil
	}
}

// wireWizard makes the setup wizard. The restarter only asks it about
// postponing once it's ready (wireSetup).
func (c *controller) wireWizard() {
	r := c.restarts
	c.wiz = &setup.Setup{
		Lab: c.lab, LabPath: c.labPath, Store: c.st, Secrets: podman.NewSecrets(),

		Restart: r.restart,
		Drives:  c.agent,
		// The wizard's end restarts Roostack if a step asked for it.
		RestartPending: r.pending.Load,
		Finished: func() {
			if c.pages != nil {
				// The setup pages on the home network close a few seconds
				// later: the page saying setup is finished, and its
				// stylesheets, load before the restart ends them.
				c.pages.Finish()
			}
			if !r.pending.Load() {
				return
			}
			if c.pages == nil {
				r.now()
				return
			}
			go func() {
				select {
				case <-c.pages.Closed():
				case <-time.After(30 * time.Second):
				}
				r.now()
			}()
		},
	}
}

// wireWeb makes the web UI, with what every page relies on, and the
// sign-in page for services. The lab's previous address stops when its
// time is up (SPEC.md §6.6), checked once its DNS records can be deleted
// (wireDNS).
func (c *controller) wireWeb() error {
	lab := c.lab
	srv, err := web.NewServer(c.st, c.log, func(ctx context.Context) ([]web.ServiceView, error) {
		services, err := state.LoadServices(lab)
		if err != nil {
			return nil, err
		}
		return web.ServiceViews(ctx, lab, services, c.mgr, c.log), nil
	}, c.mgr, c.wiz, web.LabSettings{LabName: lab.DisplayName(), Domain: lab.Domain, UIHost: lab.UIHost(), DNSProvider: lab.DNS.Provider,
		OldAddress: lab.OldAddress, BackupRepo: lab.Backup.Repository, HasGPU: lab.HasGPU, Maintenance: lab.Maintenance}, c.token)
	if err != nil {
		return err
	}
	c.srv = srv
	srv.SetupTokenFile = lab.Paths.SetupTokenFile()
	srv.OnSetupComplete = func() {
		os.Remove(lab.Paths.SetupTokenFile())
		// A restore that wasn't finished, and the recovery key typed for it
		// (with the key hints it learned).
		os.Remove(backup.RestoreKeyFile(lab.Paths))
		os.Remove(backup.RestoreStartedFile(lab.Paths))
		os.Remove(backup.RestoreHintsFile(lab.Paths))
	}
	srv.Version = version
	srv.Node = c.local
	srv.Libraries = c.mgr
	// Migrating photos into Immich (SPEC.md §8.5): its pages, and the
	// email when one ends.
	srv.Photos = c.mgr
	c.local.OnPhotos = srv.PhotosEnded
	if err := startSignIn(c.ctx, lab, c.st, srv, c.mgr, c.local, c.log); err != nil {
		return err
	}
	c.labName = config.NameAt(c.labPath, lab)
	return nil
}

// wireCloud links the lab to Roostack Cloud, unless it's turned off
// (SPEC.md §6.6): only the controller talks to it. The link starts later
// (startCloud).
func (c *controller) wireCloud() error {
	lab, mgr, srv := c.lab, c.mgr, c.srv
	if !lab.Cloud.Disabled {
		link, err := cloud.New(lab.Paths.StateDir, lab.Cloud, version, c.labName, c.log)
		if err != nil {
			return err
		}
		c.link = link
		srv.Cloud = link
		link.LabDomain(lab.Domain)
		// The previous address while it works: an earlier roost.ac name
		// Cloud keeps is released once it ends (SPEC.md §6.6), which
		// restarts Roostack.
		link.OldAddress = func() string {
			if o := lab.OldAddress; o != nil && time.Now().Before(o.Until) {
				return o.Domain
			}
			return ""
		}
		// The recovery key's identity, for restoring and relinking.
		link.RecoveryKey = func() (string, error) {
			data, err := os.ReadFile(backup.KeyFile(lab.Paths))
			if errors.Is(err, os.ErrNotExist) {
				return "", nil
			}
			return string(data), err
		}
		// Roostack Cloud's relay as a way in (SPEC.md §10.5): its
		// interface through the agent, its edges for Caddy and DNS.
		link.WireGuard = c.agent
		mgr.Relay = manager.Relay{Set: link.Relay, Stop: link.StopRelay, Edges: func() (relay, pub []string) {
			for _, e := range link.RelayEdges() {
				relay, pub = append(relay, e.Addr), append(pub, e.PublicIPv4)
			}
			return relay, pub
		}}
		// How the relay stands (the own domain's proof, its edges), every
		// 5 minutes; changed edges (one rebuilt) reach Caddy and the
		// firewall with an apply.
		link.EdgesChanged = func() {
			mgr.ApplyBecause("Roostack Cloud's relay changed its edges: changing how public visitors reach the lab")
		}
		go link.RefreshRelay(c.ctx, 5*time.Minute)
		// Roostack Cloud backup as a destination (SPEC.md §6.9).
		mgr.CloudBackup = link
		// What the lab tells Cloud after each check-in (SPEC.md §6.6): who
		// gets its problems by email, whom Cloud's emails about the lab go
		// to too, and each server's last backup there (link adds the lab's
		// name).
		link.Report = func(ctx context.Context) (cloudapi.LabReport, error) {
			emails, err := attention.ProblemRecipients(ctx, c.st)
			if err != nil {
				return cloudapi.LabReport{}, err
			}
			backups, err := mgr.CloudBackups(ctx)
			if err != nil {
				return cloudapi.LabReport{}, err
			}
			return cloudapi.LabReport{Emails: emails, Backups: backups}, nil
		}
		// The lab's own CrowdSec bans, refused at the relay's edges too.
		go link.SendBans(c.ctx, 5*time.Minute, func(ctx context.Context) ([]string, error) {
			// CrowdSec runs while anything is public, its unit there
			// with it (SPEC.md §6.11).
			unit := filepath.Join(lab.Paths.QuadletDir(), generate.CrowdSecUnit+".container")
			if _, err := os.Stat(unit); errors.Is(err, os.ErrNotExist) {
				return nil, cloud.ErrNoCrowdSec
			}
			return public.CrowdSecBans(ctx, userLaunch, generate.CrowdSecUnit)
		})
		// Roostack Cloud backup's keys: other servers ask the controller
		// for theirs (each its own folder's), and the controller's own
		// backups ask Cloud directly (SPEC.md §6.9).
		c.storageKeys = func(ctx context.Context, server string) (cloudapi.StorageKeys, error) {
			return link.StorageKeys(ctx, cloudapi.StorageRequest{Purpose: cloudapi.StorageBackup, Server: server})
		}
		self := lab.NodeID
		c.cloudKeys.set(func(ctx context.Context) (cloudapi.StorageKeys, error) { return c.storageKeys(ctx, self) })
	}
	// The relay in the lab's settings without a link (unlinked before
	// Unrelayed turned it off, or Cloud turned off here): off, so nothing
	// asks Cloud to update it (SPEC.md §10.5).
	if lab.Public.Relay && (c.link == nil || !c.link.Status().Linked) {
		if err := mgr.RelayGone(false); err != nil {
			c.log.Error("turning Roostack Cloud's relay off", "err", err)
		}
	}
	return nil
}

// wirePublic sets up public access (SPEC.md §6.11): checking links from a
// phone, and whether CrowdSec is up (the records wait for it).
func (c *controller) wirePublic() {
	srv, st, log := c.srv, c.st, c.log
	c.crowdSecUp = func(ctx context.Context) bool {
		return public.CrowdSecUp(ctx, fmt.Sprintf("127.0.0.1:%d", config.CrowdSecPort)) &&
			public.AppSecUp(ctx, fmt.Sprintf("127.0.0.1:%d", config.AppSecPort))
	}
	srv.Checks = &public.Checks{Record: func(host string, at time.Time, from string) error {
		log.Info("public address checked from outside", "host", host, "from", from)
		return web.RecordPublicCheck(context.Background(), st, host, at, from)
	}}
	srv.Protected = c.crowdSecUp
	srv.Attacks = func(ctx context.Context, since time.Duration) (public.Attacks, error) {
		return public.CrowdSecAttacks(ctx, userLaunch, generate.CrowdSecUnit, since)
	}
	srv.Blocks = func(ctx context.Context, since time.Duration) ([]public.Block, error) {
		return public.CrowdSecBlocks(ctx, userLaunch, generate.CrowdSecUnit, since)
	}
}

// wireNetwork starts Roostack's network between servers (SPEC.md §10.2),
// with this server as the hub others join, and storage for other labs
// (SPEC.md §10.10): this lab can keep other labs' backups, and store its
// own at other labs.
func (c *controller) wireNetwork() {
	ctx, lab, st, mgr, srv, log := c.ctx, c.lab, c.st, c.mgr, c.srv, c.log
	c.hub = &labnet.Hub{StateDir: lab.Paths.StateDir, LabName: c.labName, Self: c.id, Store: st, Agent: c.agent,
		Log: log, Now: time.Now, Started: c.restarts.restart,
		Joined: func(ctx context.Context, id, name string) {
			st.Audit(ctx, nil, attention.AuditServerJoined, name)
			// A new server keeps the lab's time zone (it can change it).
			go mgr.MatchTimezone(context.WithoutCancel(ctx), id)
			// The firewall lets it past the lab API now (SetLabServers).
			c.local.Reapply("A server joined the lab")
		}}
	c.local.SetLabServers(func(ctx context.Context) ([]string, error) {
		servers, err := st.Servers(ctx)
		var addrs []string
		for _, s := range servers {
			addrs = append(addrs, s.Addr)
		}
		return addrs, err
	})
	if lab.Domain != "" {
		c.hub.UI = "https://" + lab.UIHost()
	}
	c.hub.Start(ctx)
	// A fresh server can join an existing lab from its first-run page
	// (SPEC.md §10.2), as `roostack node join` does: undone if it fails,
	// then Roostack restarts as part of the lab.
	srv.JoinLab = func(ctx context.Context, code labnet.JoinCode, name string) (string, error) {
		self, err := nodeid.LoadOrCreate(lab.Paths.StateDir)
		if err != nil {
			return "", err
		}
		self.Name = name
		if err := self.Save(lab.Paths.StateDir); err != nil {
			return "", err
		}
		say := func(format string, args ...any) { log.Info("joining a lab: " + fmt.Sprintf(format, args...)) }
		if err := labnet.JoinLab(ctx, code, self, lab.Paths.StateDir, c.agent, say); err != nil {
			// Undo it: the link, and anything saved for the lab.
			if lerr := labnet.Leave(context.Background(), lab.Paths.StateDir, c.agent); lerr != nil {
				log.Warn("undoing a failed join", "err", lerr)
			}
			return "", err
		}
		st.Audit(ctx, nil, "joined a lab", cmp.Or(code.LabName, code.Endpoint)+" as "+name)
		go func() {
			// The page goes out first. Not restart(): that waits for the setup
			// wizard, which a server joining a lab never finishes.
			time.Sleep(2 * time.Second)
			c.restarts.now()
		}()
		return code.Addr.String(), nil
	}
	srv.Network = c.hub
	mgr.Registry = c.hub
	// The guests' link and stores are on whichever server offers the
	// storage; the controller keeps the records and answers guests (on
	// that server's link, forwarded here when it's another server).
	c.guestHub = &labnet.GuestHub{LabName: c.labName, Store: st, Agent: mgr.GuestLinkAgent(), Log: log, Now: time.Now,
		Usage:  func(ctx context.Context, g store.Guest) (int64, error) { return mgr.GuestUsage(ctx, g.User()) },
		Joined: func(ctx context.Context, name string) { st.Audit(ctx, nil, attention.AuditGuestJoined, name) },
		// A guest server's store user added or removed: the guest's store
		// follows (its password file).
		Changed: func() { mgr.Apply() }}
	mgr.GuestHub, mgr.GuestRecords, mgr.LinkAgent = c.guestHub, st, c.agent
	c.guestHub.Start(ctx)
	go c.guestHub.ServeGuests(ctx)
	mgr.RemoteAdded = func(r manager.RemoteStorage) { go forwardToRemote(ctx, lab, r, log) }
}

// wireBackups sets up backups across servers (SPEC.md §10.10): the
// controller creates each server's repository with the recovery key, and
// forwards its servers' backups to this lab's storage at other labs.
func (c *controller) wireBackups() {
	lab, mgr := c.lab, c.mgr
	mgr.BackupStore = c.st
	mgr.Libraries = c.st
	for _, r := range mgr.RemoteStorages(c.ctx) {
		go forwardToRemote(c.ctx, lab, r, c.log)
	}
	if err := mgr.AdoptBackupDestination(c.ctx, lab.Backup.Repository); err != nil {
		c.log.Error("recording this server's backup destination", "err", err)
	}
	mgr.HasRecoveryKey = func() bool {
		_, err := os.Stat(backup.KeyFile(lab.Paths))
		return err == nil
	}
	mgr.EnsureRecoveryKey = func() error { return setup.EnsureRecoveryKey(lab.Paths) }
	mgr.OpenRepo = repoOpener(lab)
	mgr.Drives = c.agent
}

// serveLabAPI serves the lab API to the lab's servers (controllerAPI), and
// keeps this server's own lab certificate renewed.
func (c *controller) serveLabAPI() {
	stateDir := c.lab.Paths.StateDir
	go labnet.Serve(c.ctx, stateDir, controllerAPI(c.hub, c.guestHub, c.mgr, c.storageKeys), c.log)
	go labnet.KeepRenewed(c.ctx, labnet.Identity{Dir: labnet.IdentityDir(stateDir)}, c.id.ID, c.hub.RenewSelf, c.log)
}

// wireCatalog starts the catalog: the service registry's packages, fetched
// and checked against its signed index (SPEC.md §8.3.3).
func (c *controller) wireCatalog() error {
	lab, mgr := c.lab, c.mgr
	roots, err := registry.RootKeys()
	if err != nil {
		return err
	}
	fetcher := &registry.Fetcher{URL: cmp.Or(lab.Registry.URL, registry.DefaultURL), Dir: lab.Paths.CatalogDir, Roots: roots,
		Version: version, Arches: mgr.LabArches, Client: &http.Client{Timeout: time.Minute}, Log: c.log}
	// Withdrawals reach installed services: after each check, the manager
	// stops or starts what they change (SPEC.md §8.3.4).
	mgr.RegistryIndex, fetcher.Fetched = fetcher.Index, mgr.RegistryChanged
	updater := registry.NewUpdater(fetcher, lab.Registry.Disabled, func(off bool) error {
		return config.Update(c.labPath, func(l *config.Lab) { l.Registry.Disabled = off })
	}, c.log)
	c.srv.Registry = updater
	go updater.Run(c.ctx)
	return nil
}

// startJobs starts the controller's recurring jobs, and the first apply.
func (c *controller) startJobs() {
	startControllerJobs(c.ctx, c.mgr, c.coord, c.st, c.log)
	c.srv.RunningJob = func() string {
		if st := c.coord.Status(); st.Running != nil {
			return st.Running.Name
		}
		return ""
	}
	// Converge on startup, e.g. after Roostack itself was upgraded.
	c.mgr.ApplyBecause("Roostack started (after a restart or an update)")
	// After a lab restore, the services' databases go back once they're set
	// up again.
	if r, ok := c.local.Jobs.Restores.(*backup.Restores); ok {
		assigned := func() (config.Lab, []state.Service, error) {
			l, services := c.local.Assigned()
			return l, services, nil
		}
		go finishLabRestore(c.lab, c.coord, r.Restorer, assigned, c.mgr.Wait, c.local.Activity, c.log)
	}
}

// wireSetup readies the setup wizard and the first-run pages: the setup
// pages on the home network, and a new install becoming a lab restored
// from its backups.
func (c *controller) wireSetup() error {
	lab, srv := c.lab, c.srv
	c.restarts.wizard.Store(c.wiz)
	if err := c.wiz.AdoptExisting(c.ctx); err != nil {
		return err
	}
	// Where the setup pages on the home network send the Owner once setup
	// is finished: the address saved in the wizard, if Roostack's own
	// pages are there.
	srv.FirstRunAddress = func() string {
		l, err := config.Load(c.labPath)
		if err != nil || !l.HasAddress() || (l.OnInternet() && !l.PublicUI()) {
			return ""
		}
		return "https://" + l.UIHost()
	}
	srv.DetectNetwork = public.DetectNetwork
	srv.RestorePending = func() bool { _, ok := labrestore.Pending(lab.Paths); return ok }
	if c.token != "" {
		srv.LabRestore = &labRestorer{lab: lab, drives: c.agent, acts: c.local.Activity, restart: c.restarts.now, log: c.log, cloud: c.link}
	}
	if c.pages != nil {
		go serveSetupPages(c.ctx, c.pages, srv, lab, c.local, c.log)
	}
	return nil
}

// startCloud starts the link to Roostack Cloud. A roost.ac name from it
// becomes the lab's address, and goes with the link (SPEC.md §6.6).
func (c *controller) startCloud() {
	link := c.link
	if link == nil {
		return
	}
	link.UseName = func(ctx context.Context, n cloudapi.NameClaim) error {
		return c.wiz.UseRoostAc(ctx, n.Name, n.Credentials)
	}
	link.Dropped = func(ctx context.Context) {
		if err := c.wiz.DropRoostAc(context.WithoutCancel(ctx)); err != nil {
			c.log.Error("taking away the lab's roost.ac address", "err", err)
		}
	}
	link.Unrelayed = func(context.Context) {
		if err := c.mgr.RelayGone(true); err != nil {
			c.log.Error("turning Roostack Cloud's relay off", "err", err)
		}
	}
	go link.Run(c.ctx)
}

// wireNotifier sends email through the lab's SMTP server (SPEC.md §6.13),
// and the notifier the emails about problems, from what needs attention
// as the pages have the lab.
func (c *controller) wireNotifier() {
	m := &mail.Service{LabPath: c.labPath, PasswordFile: c.lab.Paths.MailPasswordFile()}
	c.srv.Mail = m
	n := &attention.Notifier{Store: c.st, Log: c.log, Mail: m, Lab: c.srv.AttentionLab}
	go n.Run(c.ctx)
}

// wireMonitoring starts monitoring: every service's address, the servers,
// the lab's address and people's own checks, every minute (SPEC.md
// §6.15); and culls history older than the Owner's choice, on every
// server (SPEC.md §6.14).
func (c *controller) wireMonitoring() {
	srv := c.srv
	srv.AutoChecks = c.mgr.MonitorChecks
	srv.StatusPage, srv.StatusPageDir = c.mgr, c.lab.Paths.StatusPageDir()
	srv.Monitor = &monitor.Monitor{Checks: srv.MonitorChecks, Store: c.st, Log: c.log,
		Enabled: srv.MonitorOn, KeepDays: srv.HistoryKeepDays, Changed: srv.WriteStatusPage}
	go srv.Monitor.Run(c.ctx)
	go srv.RunHistoryCull(c.ctx)
}

// wireDNS keeps *.<domain> pointing at this server for devices at home,
// and checks they can see it (SPEC.md §6.5.1). The records of an old
// address follow while it works, and go when it ends (SPEC.md §6.6), even
// if the lab has no address now.
func (c *controller) wireDNS() {
	lab, mgr, link := c.lab, c.mgr, c.link
	if lab.DNS.Provider != "" || lab.OldAddress != nil {
		// A roost.ac name's records are set through Roostack Cloud: its
		// request log shows them (SPEC.md §6.6).
		recordsHTTP := &http.Client{Timeout: 30 * time.Second}
		if link != nil {
			recordsHTTP.Transport = link.RecordsTransport(nil)
		}
		records := &dnsrecords.Manager{
			Lab: lab, Secrets: podman.NewSecrets(), Store: c.st, Log: c.log,
			HTTP:        recordsHTTP,
			LocalLookup: dnsrecords.SystemLookup, PublicLookup: dnsrecords.PublicLookup,
			Wait:      90 * time.Second,
			UpdateLab: func(change func(*config.Lab)) error { return config.Update(c.labPath, change) },
			// The old address as lab.yaml has it now: it's kept after it
			// stops working, until its records are deleted.
			OldAddress: func() *config.OldAddress {
				l, err := config.Load(c.labPath)
				if err != nil {
					return lab.OldAddress
				}
				return l.OldAddress
			},
			// Public addresses point at the lab's edge, if it has one
			// (SPEC.md §10.5), else the home's public address (port
			// forwarding), once CrowdSec is up.
			PublicHosts: mgr.PublicHosts, Protected: c.crowdSecUp,
			Entry: public.Chosen{Home: public.PortForwarding{}, Edge: func(ctx context.Context) (public.Edge, bool) {
				if mgr.RelayOn() && mgr.Relay.Edges != nil {
					// Cloud points relayed names at its edges itself.
					_, pub := mgr.Relay.Edges()
					return public.Edge{Name: "Roostack Cloud", Addr: cmp.Or(append(pub, "")...)}, true
				}
				e, ok := mgr.Edge(ctx)
				return public.Edge{Name: cmp.Or(e.Name, e.ID), Addr: e.Address}, ok
			}},
			// link.<domain>, once chosen for joining or storage for others.
			LinkWanted: func() bool { v, _ := c.st.Setting(context.Background(), web.LinkWantedKey); return v == "on" },
			// Another copy of this lab runs it now: leave its records alone.
			Paused: func() bool { v, _ := c.st.Setting(context.Background(), backup.FenceKey); return v != "" },
			// The lab's own domain relayed through Roostack Cloud: public
			// addresses are CNAMEs to its relay name, with the proof's TXT.
			Relay: func() (dnsrecords.Relayed, bool) {
				if link == nil || !mgr.RelayOn() || lab.RoostAc() {
					return dnsrecords.Relayed{}, false
				}
				name, d := link.RelayName()
				r := dnsrecords.Relayed{Name: name}
				if d != nil {
					r.ProofName, r.ProofValue = d.ProofName, d.ProofValue
				}
				return r, name != ""
			},
			// The proof is in place: have Cloud look for it now, while
			// it hasn't found it (it asks no more often than every 5 s).
			ProofSet: func() {
				if link == nil {
					return
				}
				if _, d := link.RelayName(); d != nil && (!d.Verified || d.Missing) {
					go func() {
						if err := mgr.CheckRelay(context.Background()); err != nil {
							c.log.Warn("asking Roostack Cloud to look for the domain's proof", "err", err)
						}
					}()
				}
			},
		}
		c.wiz.RemoveOldRecords = records.RemoveOld
		if lab.DNS.Provider != "" {
			go records.Run(c.ctx, 30*time.Minute)
			c.srv.DNS = records
		}
	}
	go c.wiz.RunOldAddress(c.ctx, c.log)
}

// listen serves the web UI on addr (default: the lab's UI address and
// port) until ctx ends.
func (c *controller) listen(addr string) error {
	if addr == "" {
		addr = fmt.Sprintf("%s:%d", c.lab.UIBind, c.lab.UIPort)
	}
	httpSrv := &http.Server{Addr: addr, Handler: c.srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-c.ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
	}()
	c.log.Info("roostack listening", "addr", addr)
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// waitForNetwork waits, up to a minute, until this server has a route to
// the internet, and returns the address it uses (lab.HostIP). At boot,
// network-online.target can come before the network is usable: on
// Linode's Debian image, before DHCP has given the server its address.
// Starting then made the first apply and the DNS check fail ("network is
// unreachable"), and Caddy's private addresses miss this server's own
// (docs/ASSUMPTIONS.md C11).
func waitForNetwork(log *slog.Logger) string {
	for i := 0; ; i++ {
		ip, err := dnsrecords.DetectLANIP()
		if err == nil {
			if i > 0 {
				log.Info("the network is up", "waited", fmt.Sprintf("%ds", i))
			}
			return ip
		}
		if i == 60 {
			log.Warn("no network after a minute; carrying on", "err", err)
			return ""
		}
		if i == 0 {
			log.Info("waiting for the network", "err", err)
		}
		time.Sleep(time.Second)
	}
}
