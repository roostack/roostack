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

	"github.com/mylab/mylab/internal/auth"
	"github.com/mylab/mylab/internal/backup"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/state"
	"github.com/mylab/mylab/internal/store"
	"github.com/mylab/mylab/internal/systemd"
)

const backupUsage = `Usage:
  mylab backup init        Create the recovery key (first time) and the repository
  mylab backup run         Back up now
  mylab backup snapshots   List backups

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
	fs.Parse(args[1:])

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
	restic, err := exec.LookPath("restic")
	if err != nil {
		return errors.New("backup: restic is not installed")
	}
	repo := &backup.Repo{
		Bin: restic, URL: lab.Backup.Repository, PasswordFile: backup.KeyFile(lab.Paths),
		Env: lab.Backup.Env, LimitUploadKiB: lab.Backup.LimitUploadKiB, Launch: unshared(launch),
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
	mylabBin, err := os.Executable()
	if err != nil {
		return err
	}
	st, err := store.Open(dbPath(lab))
	if err != nil {
		return err
	}
	defer st.Close()
	host, _ := os.Hostname()

	sum, err := backup.Run(ctx, backup.JobDeps{
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
	}, &printReporter{})
	fmt.Println()
	if err != nil {
		return err
	}
	fmt.Printf("Backup %s done: %d new files, %d bytes added, took %s.\n",
		sum.SnapshotID[:8], sum.FilesNew, sum.DataAdded, sum.Duration.Round(1e9))
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
	if len(args) == 3 && args[0] == "sqlite-copy" {
		return backup.SQLiteCopy(context.Background(), args[1], args[2])
	}
	return errors.New("backup-helper: usage: sqlite-copy <src> <dst>")
}
