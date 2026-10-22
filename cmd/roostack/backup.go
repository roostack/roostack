package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/cloudkeys"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/podman"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/systemd"
)

const backupUsage = `Usage:
  roostack backup init                          Create the recovery key (first time) and the repository
  roostack backup run                           Back up now
  roostack backup snapshots                     List backups
  roostack backup restore <service> [-snapshot ID]
                                             Restore a service (latest backup by default).
                                             Current data is moved aside, never deleted.

The repository is set in lab.yaml (backup.repository). Run as root or the
roostack user: started as root, it runs as the roostack user.
`

func backupCmd(args []string) error {
	if len(args) == 0 {
		fmt.Print(backupUsage)
		return errors.New("backup: missing command")
	}
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	direct := fs.Bool("direct", false, "run restic and podman directly instead of via systemd-run --user (development)")
	snapshot := fs.String("snapshot", "", "backup to restore (ID from 'roostack backup snapshots'; default latest)")
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
	if _, err := withNode(&lab); err != nil {
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
		snaps, err := repo.Snapshots(ctx, backup.TagRoostack)
		if err != nil {
			return err
		}
		for _, s := range snaps {
			fmt.Printf("%s  %s  %-12s  %s\n", s.Short, s.Time.Local().Format("2006-01-02 15:04"),
				cmp.Or(s.Service(), "(server)"), strings.Join(s.Paths, " "))
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
		key, err := backup.NewRecoveryKey()
		if err != nil {
			return err
		}
		if err := fsutil.WritePrivate(keyFile, []byte(key+"\n")); err != nil {
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
	deps, err := newBackupDeps(lab, st, repo, launch, diskServices(lab))
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
// unshare so it can read and restore files of container users. In
// Roostack Cloud backup, its keys are fetched before each command.
func newRepo(lab config.Lab, launch backupLaunch) (*backup.Repo, error) {
	restic, err := exec.LookPath("restic")
	if err != nil {
		return nil, errors.New("backup: restic is not installed")
	}
	r := &backup.Repo{
		Bin: restic, URL: lab.Backup.Repository, PasswordFile: backup.KeyFile(lab.Paths),
		Env: lab.Backup.Env, LimitUploadKiB: lab.Backup.LimitUploadKiB, Launch: unshared(launch),
		Hints: backup.Hints(lab.Paths),
	}
	if lab.Backup.Cloud {
		r.Credentials = cloudBackupCache(lab).Env
	}
	return r, nil
}

// cloudBackupSource asks for this server's Roostack Cloud backup keys:
// set when Roostack runs (the controller asks Cloud; another server asks
// the controller). Unset (the command line), only keys already kept work.
var (
	cloudBackupMu     sync.Mutex
	cloudBackupSource func(ctx context.Context) (cloudapi.StorageKeys, error)
)

func setCloudBackupKeys(f func(ctx context.Context) (cloudapi.StorageKeys, error)) {
	cloudBackupMu.Lock()
	defer cloudBackupMu.Unlock()
	cloudBackupSource = f
}

// cloudBackupCache keeps this server's keys (state/cloud-backup-keys.json).
func cloudBackupCache(lab config.Lab) *cloudkeys.Cache {
	return &cloudkeys.Cache{Path: cloudkeys.File(lab.Paths.StateDir, "backup"),
		Source: func(ctx context.Context) (cloudapi.StorageKeys, error) {
			cloudBackupMu.Lock()
			f := cloudBackupSource
			cloudBackupMu.Unlock()
			if f == nil {
				return cloudapi.StorageKeys{}, errors.New("Roostack Cloud backup's keys come through Roostack while it runs")
			}
			return f(ctx)
		}}
}

// drivePrecheck checks the backup drive is connected, when backups go on
// one (a USB drive or second disk).
func drivePrecheck(d config.BackupDrive) func() error {
	if d.UUID == "" {
		return nil
	}
	return func() error { return backup.CheckDrive(d.Mount, d.UUID, d.Name) }
}

// copyRepos opens this server's extra backup copies (SPEC.md §6.9).
func copyRepos(lab config.Lab, launch backupLaunch) []backup.Copy {
	restic, err := exec.LookPath("restic")
	if err != nil {
		restic = "restic"
	}
	var out []backup.Copy
	for _, c := range lab.Backup.Copies {
		keyFile := cmp.Or(c.KeyFile, backup.KeyFile(lab.Paths))
		r := &backup.Repo{Bin: restic, URL: c.Repository, PasswordFile: keyFile, Env: c.Env,
			LimitUploadKiB: lab.Backup.LimitUploadKiB, Launch: unshared(launch), Hints: backup.Hints(lab.Paths)}
		if c.Cloud {
			r.Credentials = cloudBackupCache(lab).Env
		}
		out = append(out, backup.Copy{Name: c.Name, Title: c.Title, Precheck: drivePrecheck(c.Drive), Repo: r})
	}
	return out
}

// newBackupDeps wires a backup job to this lab. Used by the CLI and by
// roostack serve's nightly backups.
// serverServices returns this server's services and the lab config to
// generate them with.
type serverServices func() (config.Lab, []state.Service, error)

// diskServices reads the services placed on this server from the
// instance files (on the controller, which keeps them).
func diskServices(lab config.Lab) serverServices {
	return func() (config.Lab, []state.Service, error) {
		all, err := state.LoadServices(lab)
		return lab, state.OnNode(all, lab.NodeID), err
	}
}

func newBackupDeps(lab config.Lab, st *store.Store, repo *backup.Repo, launch backupLaunch, services serverServices) (*backup.JobDeps, error) {
	roostackBin, err := os.Executable()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	return &backup.JobDeps{
		Repo:       repo,
		Preparer:   &backup.Preparer{Launch: launch, RoostackBin: roostackBin, QuadletDir: lab.Paths.QuadletDir()},
		DumpState:  st.Backup,
		Copies:     copyRepos(lab, launch),
		Precheck:   drivePrecheck(lab.Backup.Drive),
		Paths:      lab.Paths,
		Host:       host,
		ReadSecret: podman.NewSecrets().Read,
		Generation: backup.ReadGeneration(lab.Paths),
		Plan: func() (backup.Plan, error) {
			lab, services, err := services()
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
	all, err := state.LoadServices(lab)
	if err != nil {
		return err
	}
	services := state.OnNode(all, lab.NodeID)
	var svc *state.Service
	for i := range services {
		if services[i].Name() == name {
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
	roostackBin, err := os.Executable()
	if err != nil {
		return err
	}
	r := &backup.Restorer{Repo: repo, Units: systemd.New(), Launch: launch, RoostackBin: roostackBin, Now: time.Now}
	fmt.Printf("Restoring %s...\n", svc.Title())
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

// userLaunch runs a command through the roostack user's systemd manager, so
// podman never runs inside roostack's sandbox (SPEC.md §6.1).
func userLaunch(ctx context.Context, env []string, name string, args ...string) *exec.Cmd {
	unit := fmt.Sprintf("roostack-backup-%d-%d", os.Getpid(), unitSeq.Add(1))
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

// backupHelper holds commands run by Roostack itself inside podman unshare.
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
