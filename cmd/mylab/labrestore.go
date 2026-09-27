package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/labrestore"
	"github.com/mylab/mylab/internal/maintenance"
	"github.com/mylab/mylab/internal/state"
	"github.com/mylab/mylab/internal/systemd"
)

// labRestorer rebuilds a lab from its backups on a fresh install
// (internal/labrestore), for the first-run pages.
type labRestorer struct {
	lab     config.Lab
	acts    *activity.Registry
	restart func()
	log     *slog.Logger

	mu      sync.Mutex
	running bool
}

// keyFile holds the recovery key while a restore is being prepared.
func (l *labRestorer) keyFile() string { return filepath.Join(l.lab.Paths.StateDir, "restore-key") }

func (l *labRestorer) openRepo(url string, env []string) *backup.Repo {
	bin, err := exec.LookPath("restic")
	if err != nil {
		bin = "restic"
	}
	return &backup.Repo{Bin: bin, URL: url, Env: env, PasswordFile: l.keyFile(), Launch: unshared(userLaunch)}
}

// fresh reports whether nothing has been set up on this server yet.
func (l *labRestorer) fresh() error {
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
	if err := os.WriteFile(l.keyFile(), []byte(key+"\n"), 0o600); err != nil {
		return nil, nil, err
	}
	f := &labrestore.Finder{OpenRepo: l.openRepo}
	return f.Find(ctx, src)
}

// Start restores a backup in the background, then restarts MyLab as the
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
	l.running = true
	alog := l.acts.Start(activity.LabRestore, "Restoring "+fd.Domain)
	go func() {
		err := labrestore.Restore(context.Background(), l.lab.Paths, l.openRepo(fd.Repository, fd.Env), fd, l.keyFile(), alog)
		alog.Finish(err)
		l.mu.Lock()
		l.running = false
		l.mu.Unlock()
		if err != nil {
			l.log.Error("restoring a lab", "err", err)
			return
		}
		os.Remove(l.keyFile())
		l.log.Info("lab restored; restarting", "domain", fd.Domain, "server", fd.ServerName)
		l.restart()
	}()
	return nil
}

// Running reports whether a restore is running.
func (l *labRestorer) Running() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

// Progress is the restore's log.
func (l *labRestorer) Progress() (activity.Snapshot, bool) { return l.acts.Latest(activity.LabRestore) }

// finishLabRestore puts the services' databases back after a lab restore
// (labrestore step 3), once the first apply has set the services up
// again: each service is stopped, its databases loaded from their dumps,
// and started. A failure is logged and retried at the next start.
func finishLabRestore(lab config.Lab, coord *maintenance.Coordinator, restorer *backup.Restorer, services serverServices,
	wait func(), acts *activity.Registry, log *slog.Logger) {
	if _, ok := labrestore.Pending(lab.Paths); !ok {
		return
	}
	wait()
	coord.Submit(maintenance.Job{Name: "lab-restore", Run: func(ctx context.Context, h *maintenance.Handle) error {
		h.SetInterruptible(false)
		alog := acts.Start(activity.LabRestore, "Putting the services' databases back")
		err := loadAllDumps(ctx, restorer, services, alog)
		alog.Finish(err)
		if err != nil {
			log.Error("finishing the lab restore", "err", err)
			return err
		}
		log.Info("lab restore finished")
		return labrestore.Done(lab.Paths)
	}})
}

func loadAllDumps(ctx context.Context, restorer *backup.Restorer, services serverServices, alog *activity.Log) error {
	lab, svcs, err := services()
	if err != nil {
		return err
	}
	d, err := state.Desired(lab, svcs)
	if err != nil {
		return err
	}
	units := systemd.New()
	for _, svc := range d.Services {
		if len(svc.Backup.Prepare) == 0 {
			continue
		}
		alog.Step("%s", svc.Service)
		if err := units.Stop(ctx, svc.Units...); err != nil {
			return fmt.Errorf("stopping %s: %w", svc.Service, err)
		}
		if err := restorer.LoadDumps(ctx, svc); err != nil {
			return fmt.Errorf("%s's database: %w", svc.Service, err)
		}
		if err := units.Start(ctx, slices.Clone(svc.Units)...); err != nil {
			return fmt.Errorf("starting %s: %w", svc.Service, err)
		}
	}
	return nil
}
