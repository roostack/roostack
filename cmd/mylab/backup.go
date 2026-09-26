package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/state"
	"github.com/mylab/mylab/internal/store"
	"github.com/mylab/mylab/internal/systemd"
)

const backupUsage = `Usage:
  mylab backup init                          Create the recovery key (first time) and the repository
  mylab backup run                           Back up now
  mylab backup snapshots                     List backups
  mylab backup restore <service> [-snapshot ID]
                                             Restore a service (latest backup by default).
                                             Current data is moved aside, never deleted.

The repository is set in lab.yaml (backup.repository).
`

func backupCmd(args []string) error {
	if len(args) == 0 {
		fmt.Print(backupUsage)
		return errors.New("backup: missing command")
	}
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	direct := fs.Bool("direct", false, "run restic and podman directly instead of via systemd-run --user (development)")
	snapshot := fs.String("snapshot", "", "backup to restore (ID from 'mylab backup snapshots'; default latest)")
	rest := args[1:]
	var target string
	if args[0] == "restore" && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		target, rest = rest[0], rest[1:]
	}
	fs.Parse(rest)

	lab, err := config.Load(*labPath)
	if err != nil {
		return err
	}
	if lab.Backup.Repository == "" {
		return errors.New("backup: no repository configured (backup.repository in lab.yaml)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	launch := userLaunch
	if *direct {
		launch = backup.DirectLaunch
	}
	repo, err := newRepo(lab, launch)
	if err != nil {
		return err
	}

	switch args[0] {
	case "init":
		return backupInit(ctx, lab, repo)
	case "snapshots":
		snaps, err := repo.Snapshots(ctx, "mylab")
		if err != nil {
			return err
		}
		for _, s := range snaps {
			fmt.Printf("%s  %s  %s\n", s.Short, s.Time.Local().Format("2006-01-02 15:04"), strings.Join(s.Paths, " "))
		}
		return nil
	case "run":
		return backupRun(ctx, lab, repo, launch)
	case "restore":
		if target == "" {
			return errors.New("backup: restore needs a service name")
		}
		return backupRestore(ctx, lab, repo, launch, target, *snapshot)
	}
	fmt.Print(backupUsage)
	return fmt.Errorf("backup: unknown command %q", args[0])
}

// backupInit creates the recovery key if needed and initialises the
// repository. The key is shown once so it can be written down (SPEC.md §6.9).
func backupInit(ctx context.Context, lab config.Lab, repo *backup.Repo) error {
	keyFile := backup.KeyFile(lab.Paths)
	if _, err := os.Stat(keyFile); errors.Is(err, os.ErrNotExist) {
		key, err := auth.NewToken()
		if err != nil {
			return err
		}
		if err := os.WriteFile(keyFile, []byte(key+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println("Recovery key (write it down or print it; without it, backups can't be restored):")
		fmt.Println()
		fmt.Println("    " + key)
		fmt.Println()
	} else if err != nil {
		return err
	}
	if err := repo.Init(ctx); err != nil {
		return err
	}
	fmt.Println("Backup repository ready:", lab.Backup.Repository)
	return nil
}

func backupRun(ctx context.Context, lab config.Lab, repo *backup.Repo, launch backupLaunch) error {
	st, err := store.Open(dbPath(lab))
	if err != nil {
		return err
	}
	defer st.Close()
	deps, err := newBackupDeps(lab, st, repo, launch)
	if err != nil {
		return err
	}
	sum, err := backup.Run(ctx, *deps, &printReporter{})
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Printf("Backup %s done: %d new files, %d bytes added, took %s.\n",
		sum.SnapshotID[:8], sum.FilesNew, sum.DataAdded, sum.Duration.Round(1e9))
	return nil
}

// newRepo returns the configured restic repository, run inside podman
// unshare so it can read and restore files of container users.
func newRepo(lab config.Lab, launch backupLaunch) (*backup.Repo, error) {
	restic, err := exec.LookPath("restic")
	if err != nil {
		return nil, errors.New("backup: restic is not installed")
	}
	return &backup.Repo{
		Bin: restic, URL: lab.Backup.Repository, PasswordFile: backup.KeyFile(lab.Paths),
		Env: lab.Backup.Env, LimitUploadKiB: lab.Backup.LimitUploadKiB, Launch: unshared(launch),
	}, nil
}

// newBackupDeps wires a backup job to this lab. Used by the CLI and by
// mylab serve's nightly backups.
func newBackupDeps(lab config.Lab, st *store.Store, repo *backup.Repo, launch backupLaunch) (*backup.JobDeps, error) {
	mylabBin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	return &backup.JobDeps{
		Repo:      repo,
		Preparer:  &backup.Preparer{Launch: launch, MylabBin: mylabBin},
		DumpState: st.Backup,
		Paths:     lab.Paths,
		Host:      host,
		Plan: func() (backup.Plan, error) {
			services, err := state.LoadServices(lab)
			if err != nil {
				return backup.Plan{}, err
			}
			d, err := state.Desired(lab, services)
			if err != nil {
				return backup.Plan{}, err
			}
			return backup.LabPlan(lab.Paths, d.Services), nil
		},
	}, nil
}

func backupRestore(ctx context.Context, lab config.Lab, repo *backup.Repo, launch backupLaunch, name, snapshot string) error {
	services, err := state.LoadServices(lab)
	if err != nil {
		return err
	}
	var svc *state.Service
	for i := range services {
		if services[i].Package.Name == name {
			svc = &services[i]
		}
	}
	if svc == nil {
		return fmt.Errorf("restore: %s is not installed", name)
	}
	out, err := generate.Service(lab, svc.Package, svc.Instance)
	if err != nil {
		return err
	}
	mylabBin, err := os.Executable()
	if err != nil {
		return err
	}
	r := &backup.Restorer{Repo: repo, Units: systemd.New(), Launch: launch, MylabBin: mylabBin, Now: time.Now}
	fmt.Printf("Restoring %s...\n", svc.Package.Title)
	res, err := r.RestoreService(ctx, out, snapshot)
	if res != nil {
		fmt.Printf("Restored from the backup taken %s (%s).\n", res.Snapshot.Time.Local().Format("2006-01-02 15:04"), res.Snapshot.Short)
		for dir, aside := range res.MovedAside {
			fmt.Printf("  previous %s kept in %s\n", dir, aside)
		}
	}
	if err != nil {
		return err
	}
	fmt.Println("Done. Remove the kept copies once you're happy with the restore.")
	return nil
}

type backupLaunch = func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd

var unitSeq atomic.Int64

// userLaunch runs a command through the mylab user's systemd manager, so
// podman never runs inside mylab's sandbox (SPEC.md §6.1).
func userLaunch(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	unit := fmt.Sprintf("mylab-backup-%d-%d", os.Getpid(), unitSeq.Add(1))
	return systemd.UserCommand(ctx, unit, env, name, args...)
}

// unshared runs commands inside podman unshare, so restic can read (and
// restore with the right owners) files belonging to container users
// (docs/ASSUMPTIONS.md B2).
func unshared(launch backupLaunch) backupLaunch {
	return func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
		return launch(ctx, env, "podman", append([]string{"unshare", name}, args...)...)
	}
}

// printReporter shows progress on the terminal.
type printReporter struct{ last int }

func (*printReporter) SetInterruptible(bool) {}

func (p *printReporter) SetProgress(pct int) {
	if pct != p.last {
		fmt.Printf("\rBacking up: %3d%%", pct)
		p.last = pct
	}
}

// backupHelper holds commands run by MyLab itself inside podman unshare.
func backupHelper(args []string) error {
	if len(args) == 3 {
		switch args[0] {
		case "sqlite-copy":
			return backup.SQLiteCopy(context.Background(), args[1], args[2])
		case "sqlite-restore":
			return backup.SQLiteRestore(context.Background(), args[1], args[2])
		}
	}
	return errors.New("backup-helper: usage: sqlite-copy|sqlite-restore <src> <dst>")
}
