package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/cloud"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/labrestore"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/systemd"
)

// labRestorer rebuilds a lab from its backups on a fresh install
// (internal/labrestore), for the first-run pages.
type labRestorer struct {
	lab     config.Lab
	drives  *agent.Client
	acts    *activity.Registry
	restart func()
	log     *slog.Logger
	// cloud is the link to Roostack Cloud, for restoring from it (nil:
	// Cloud is turned off).
	cloud *cloud.Service

	mu      sync.Mutex
	running bool
}

// keyFile holds the recovery key while a restore is being prepared.
func (l *labRestorer) keyFile() string { return backup.RestoreKeyFile(l.lab.Paths) }

func (l *labRestorer) openRepo(url string, env []string) *backup.Repo {
	bin, err := exec.LookPath("restic")
	if err != nil {
		bin = "restic"
	}
	return &backup.Repo{Bin: bin, URL: url, Env: env, PasswordFile: l.keyFile(), Launch: unshared(userLaunch),
		Hints: &backup.KeyHints{File: backup.RestoreHintsFile(l.lab.Paths)}}
}

// fresh reports whether nothing has been set up on this server yet. A
// restore that started and didn't finish counts as nothing: its files are
// the lab's, which it puts back again (security review of 2026-10-16,
// M25).
func (l *labRestorer) fresh() error {
	if l.Unfinished() {
		return nil
	}
	services, err := state.LoadServices(l.lab)
	if err != nil {
		return err
	}
	if len(services) > 0 {
		return errors.New("this server already runs services; a lab can only be restored onto a new install")
	}
	return nil
}

// Find looks for the lab's backups, opening them with the recovery key.
func (l *labRestorer) Find(ctx context.Context, src labrestore.Source, key string) ([]labrestore.Found, []labrestore.Unopened, error) {
	if err := l.fresh(); err != nil {
		return nil, nil, err
	}
	if err := fsutil.WritePrivate(l.keyFile(), []byte(key+"\n")); err != nil {
		return nil, nil, err
	}
	f := &labrestore.Finder{OpenRepo: l.openRepo}
	if l.cloud != nil {
		f.CloudAccess = func(ctx context.Context) (cloudapi.RestoreAccess, error) {
			return l.cloud.RestoreAccess(ctx, key)
		}
	}
	return f.Find(ctx, src)
}

// Start restores a backup in the background, then restarts Roostack as the
// restored lab. The recovery key is the one given to Find.
func (l *labRestorer) Start(fd labrestore.Found) error {
	if err := l.fresh(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running {
		return errors.New("a restore is already running")
	}
	if err := fsutil.WritePrivate(backup.RestoreStartedFile(l.lab.Paths), []byte(fd.Repository+"\n")); err != nil {
		return err
	}
	l.running = true
	alog := l.acts.Start(activity.LabRestore, "Restoring "+cmp.Or(fd.Name, fd.Domain, "the lab"))
	go func() {
		// What this install asked Cloud stays in the restored lab's
		// request log.
		var keepAgain func() error
		if l.cloud != nil {
			keepAgain = l.cloud.KeepRequests()
		}
		err := labrestore.Restore(context.Background(), l.lab.Paths, l.openRepo(fd.Repository, fd.Env), fd, l.keyFile(), alog)
		if err == nil && keepAgain != nil {
			if kerr := keepAgain(); kerr != nil {
				l.log.Warn("keeping this install's requests to Roostack Cloud in the restored lab's request log", "err", kerr)
			}
		}
		alog.Finish(err)
		l.mu.Lock()
		l.running = false
		l.mu.Unlock()
		if err != nil {
			l.log.Error("restoring a lab", "err", err)
			return
		}
		os.Remove(l.keyFile())
		os.Remove(backup.RestoreStartedFile(l.lab.Paths))
		os.Remove(backup.RestoreHintsFile(l.lab.Paths))
		l.log.Info("lab restored; restarting", "domain", fd.Domain, "server", fd.ServerName)
		l.restart()
	}()
	return nil
}

// Drives lists this server's drives (to look for backups on).
func (l *labRestorer) Drives(ctx context.Context) ([]agent.Drive, error) { return l.drives.Drives(ctx) }

// UseDrive mounts a drive (for good, if it isn't) and says where.
func (l *labRestorer) UseDrive(ctx context.Context, id string) (string, error) {
	u, err := l.drives.UseDrive(ctx, id)
	return u.Dir, err
}

// Unfinished reports a restore that started and didn't finish (it
// failed, or Roostack stopped part-way).
func (l *labRestorer) Unfinished() bool {
	_, err := os.Stat(backup.RestoreStartedFile(l.lab.Paths))
	return err == nil
}

// Running reports whether a restore is running.
func (l *labRestorer) Running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

// Progress is the restore's log.
func (l *labRestorer) Progress() (activity.Snapshot, bool) { return l.acts.Latest(activity.LabRestore) }

// finishLabRestore puts back the databases of the services a lab restore
// put back (labrestore step 3, instance.DumpsPending), once an apply has
// set them up with only their databases running: each is loaded from its
// dumps, then the app starts (Manager.DumpsLoaded). One that's turned off
// waits until it's turned on, which runs this again (Manager.LoadDumps).
// One whose databases don't load is held stopped (SPEC.md §6.9) while the
// others carry on.
func finishLabRestore(lab config.Lab, coord *maintenance.Coordinator, restorer *backup.Restorer, services serverServices,
	wait func(), loaded func(name string, err error) error, acts *activity.Registry, log *slog.Logger) {
	_, restoring := labrestore.Pending(lab.Paths)
	if !restoring && len(dumpsWaiting(lab)) == 0 {
		return
	}
	wait()
	coord.Submit(maintenance.Job{Name: "lab-restore", Run: func(ctx context.Context, h *maintenance.Handle) error {
		h.SetInterruptible(false)
		if len(dumpsWaiting(lab)) == 0 {
			// Already loaded, by a job submitted before this one ran.
			return labrestore.Done(lab.Paths)
		}
		alog := acts.Start(activity.LabRestore, "Putting the services' databases back")
		err := loadAllDumps(ctx, restorer, services, loaded, lab, alog)
		alog.Finish(err)
		if err != nil {
			log.Error("finishing the lab restore", "err", err)
		}
		// What's left waits in each service's instance file, so the lab
		// restore itself is over.
		if derr := labrestore.Done(lab.Paths); derr != nil {
			return derr
		}
		return err
	}})
}

// dumpsWaiting lists the services whose databases wait to be loaded now:
// those a lab restore put back that aren't turned off. The instance files
// say, rather than the server's assignment, which an apply may not have
// caught up with yet.
func dumpsWaiting(lab config.Lab) map[string]bool {
	files, err := filepath.Glob(filepath.Join(lab.Paths.InstancesDir(), "*.yaml"))
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, f := range files {
		in, err := instance.Load(f)
		if err == nil && in.DumpsPending != nil && in.Off == nil && in.HostOr(lab.NodeID) == lab.NodeID {
			out[strings.TrimSuffix(filepath.Base(f), ".yaml")] = true
		}
	}
	return out
}

// loadAllDumps loads each waiting service's databases and starts it, or
// holds it stopped if they don't load. It goes on to the others after a
// failure, and returns every failure.
func loadAllDumps(ctx context.Context, restorer *backup.Restorer, services serverServices, loaded func(name string, err error) error,
	controller config.Lab, alog *activity.Log) error {
	lab, svcs, err := services()
	if err != nil {
		return err
	}
	d, err := state.Desired(lab, svcs)
	if err != nil {
		return err
	}
	waiting := dumpsWaiting(controller)
	units := systemd.New()
	var errs []error
	for _, svc := range d.Services {
		if !waiting[svc.Service] {
			continue
		}
		alog.Step("%s", svc.Service)
		// Only its databases run (state.Output), so nothing uses them
		// while they're loaded.
		err := units.Stop(ctx, svc.Units...)
		if err == nil {
			err = restorer.LoadDumps(ctx, svc)
		}
		if err != nil {
			alog.Printf("%s's databases didn't load, so Roostack will keep it stopped until you choose what to do on its page: %v", svc.Service, err)
			errs = append(errs, fmt.Errorf("%s's database: %w", svc.Service, err))
		}
		if lerr := loaded(svc.Service, err); lerr != nil {
			errs = append(errs, fmt.Errorf("%s: %w", svc.Service, lerr))
		}
	}
	return errors.Join(errs...)
}
