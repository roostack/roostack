package main

import (
	"context"
	"log/slog"

	"github.com/mylab/mylab/internal/activity"
	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/firewall"
	"github.com/mylab/mylab/internal/hosttime"
	"github.com/mylab/mylab/internal/maintenance"
	"github.com/mylab/mylab/internal/node"
	"github.com/mylab/mylab/internal/nodeid"
	"github.com/mylab/mylab/internal/podman"
	"github.com/mylab/mylab/internal/reconcile"
	"github.com/mylab/mylab/internal/state"
	"github.com/mylab/mylab/internal/store"
	"github.com/mylab/mylab/internal/systemd"
)

// newLocal builds this machine (SPEC.md §10.1): it applies its services,
// runs their hooks, its maintenance jobs and its own settings, whether it
// is the controller or a node.
func newLocal(ctx context.Context, lab config.Lab, labPath string, id nodeid.Identity, st *store.Store,
	log *slog.Logger, restart func()) (*node.Local, *agent.Client, *maintenance.Coordinator, error) {
	agentClient := agent.NewClient(agent.DefaultSocket)
	sd := systemd.New()
	local := &node.Local{
		ID: id,
		// Live logs of host operations, shown in the UI.
		Activity: activity.New(),
		Firewall: agentClient,
		Units:    sd,
		// Moving a service's data runs tar in podman unshare, outside the
		// sandbox like backups.
		Launch:     userLaunch,
		WireGuard:  agentClient,
		Version:    version,
		Lab:        lab,
		Reconciler: &reconcile.Reconciler{Paths: lab.Paths, Systemd: sd, Secrets: podman.NewSecrets()},
		Log:        log,
		// Package hooks (e.g. creating Forgejo's admin) run with podman exec.
		State: st, Secrets: podman.NewSecrets(),
		Exec: func(ctx context.Context, container string, env map[string]string, command []string) ([]byte, error) {
			return podman.Exec(ctx, podman.Run, container, env, command...)
		},
		Machine: &node.HostSettings{
			Lab: lab, LabPath: labPath,
			SetZone: agentClient.SetTimezone,
			Zone:    hosttime.Zone, Zones: hosttime.Zones,
			SSHClients: firewall.SSHClients,
			Restart:    restart,
		},
	}
	// Backups and restores cover the services this machine runs, as its
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
	return local, agentClient, coord, nil
}
