package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/podman"
	"github.com/mylab/mylab/internal/reconcile"
	"github.com/mylab/mylab/internal/state"
	"github.com/mylab/mylab/internal/systemd"
)

func apply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ExitOnError)
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	dryRun := fs.Bool("dry-run", false, "show the plan without changing anything")
	fs.Parse(args)

	lab, err := config.Load(*labPath)
	if err != nil {
		return err
	}
	services, err := state.LoadServices(lab)
	if err != nil {
		return err
	}
	desired, err := state.Desired(lab, services)
	if err != nil {
		return err
	}

	r := &reconcile.Reconciler{
		Paths:      lab.Paths,
		Systemd:    systemd.New(),
		Secrets:    podman.NewSecrets(),
		UserSecret: envSecret,
	}
	ctx := context.Background()
	plan, err := r.Plan(ctx, desired)
	if err != nil {
		return err
	}
	fmt.Print(plan)
	if *dryRun {
		return nil
	}
	return r.Apply(ctx, plan)
}

// envSecret reads user-provided secrets from the environment, e.g.
// mylab-caddy-dns-token from MYLAB_SECRET_MYLAB_CADDY_DNS_TOKEN.
//
// Temporary: the web UI will collect these during setup (TASKS 1.9).
func envSecret(name string) ([]byte, bool) {
	key := "MYLAB_SECRET_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
	v, ok := os.LookupEnv(key)
	return []byte(v), ok && v != ""
}
