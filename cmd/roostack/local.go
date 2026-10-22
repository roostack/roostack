package main

import (
	"context"
	"io"
	"log/slog"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/firewall"
	"roostack.cloud/roostack/internal/hosttime"
	"roostack.cloud/roostack/internal/maintenance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/podman"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/systemd"
)

// newLocal builds this server (SPEC.md §10.1): it applies its services,
// runs their hooks, its maintenance jobs and its own settings, whether it
// is the controller or a node.
func newLocal(ctx context.Context, lab config.Lab, labPath string, id nodeid.Identity, st *store.Store,
	log *slog.Logger, restart func()) (*node.Local, *agent.Client, *maintenance.Coordinator, error) {
	agentClient := agent.NewClient(agent.DefaultSocket)
	agentClient.KeysFile = backup.WireGuardKeysFile(lab.Paths)
	sd := systemd.New()
	local := &node.Local{
		ID: id,
		// Live logs of host operations, shown in the UI.
		Activity: activity.New(),
		Firewall: agentClient,
		Units:    sd,
		// Moving a service's data runs tar in podman unshare, outside the
		// sandbox like backups.
		Launch:    userLaunch,
		WireGuard: agentClient,
		Links:     agentClient,
		Libraries: agentClient,
		// Bringing a service's data in (SPEC.md §8.4).
		ImportAgent: agentClient,
		// Migrating photos into Immich (SPEC.md §8.5): the export mounted
		// by the agent, immich-go run in a unit that outlives Roostack.
		PhotoAgent: agentClient,
		PhotoUnits: sd,
		Version:    version,
		Lab:        lab,
		Reconciler: &reconcile.Reconciler{Paths: lab.Paths, Systemd: sd, Secrets: podman.NewSecrets(),
			// New containers' images are downloaded as their own step, with
			// podman's output in the apply's log (SPEC.md §6.3.3).
			Pull: func(ctx context.Context, image string, w io.Writer) error {
				return podman.Pull(ctx, userLaunch, image, w)
			}},
		Log: log,
		// Package hooks (e.g. creating Forgejo's admin) run with podman exec.
		State: st, Events: st, Secrets: podman.NewSecrets(),
		Exec: func(ctx context.Context, container string, env map[string]string, command []string) ([]byte, []byte, error) {
			return podman.Exec(ctx, podman.RunSplit, container, env, command...)
		},
		Host: &node.HostSettings{
			Lab: lab, LabPath: labPath,
			SetZone: agentClient.SetTimezone,
			Zone:    hosttime.Zone, Zones: hosttime.Zones,
			SSHClients: firewall.SSHClients,
			SSHServer:  agentClient,
			UseZone: func(name string) {
				if loc, err := time.LoadLocation(name); err == nil {
					time.Local = loc
				}
			},
			Restart: restart,
		},
	}
	// A new SSH choice rebuilds the firewall at once, without a restart.
	if h, ok := local.Host.(*node.HostSettings); ok {
		h.UseSSH = local.UseSSH
	}
	// Finished logs are kept as history (SPEC.md §6.14).
	local.KeepHistory()
	// Services restart when a library they bind at a path of its own comes
	// back (SPEC.md §8.4).
	go local.WatchLibraries(ctx)
	// Backups and restores cover the services this server runs, as its
	// assignment says (a node keeps no instance files of its own).
	assigned := func() (config.Lab, []state.Service, error) {
		lab, services := local.Assigned()
		return lab, services, nil
	}
	jobs, coord, err := startMaintenance(ctx, lab, st, log, agentClient, local.Activity, assigned)
	if err != nil {
		return nil, nil, nil, err
	}
	local.Jobs = jobs
	// Disk use is counted right after the nightly backup, while restic's
	// reads of the files are still cached (SPEC.md §6.7).
	if b, ok := jobs.Backups.(*backup.Service); ok {
		b.AfterBackup = local.CountSizes
	}
	return local, agentClient, coord, nil
}
