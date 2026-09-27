package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/firewall"
	"github.com/mylab/mylab/internal/hosttime"
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
	lab.HasGPU = config.DetectGPU()
	lab.Timezone = hosttime.Zone()
	if _, err := withNode(&lab); err != nil {
		return err
	}
	all, err := state.LoadServices(lab)
	if err != nil {
		return err
	}
	// Services get a host (this server, if they have none) and new routes
	// a host port, once; both are saved in the instance file.
	changed := state.AssignHosts(all, lab.NodeID)
	services := state.OnNode(all, lab.NodeID)
	ported, err := state.AllocatePorts(services)
	if err != nil {
		return err
	}
	for _, s := range ported {
		if !slices.ContainsFunc(changed, func(c state.Service) bool { return c.Package.Name == s.Package.Name }) {
			changed = append(changed, s)
		}
	}
	for _, s := range changed {
		if *dryRun {
			fmt.Printf("Would save %s: host %s, ports %v\n", s.Package.Name, s.Instance.Host, s.Instance.Routes)
			continue
		}
		if err := s.Instance.Save(state.InstanceFile(lab, s.Package.Name)); err != nil {
			return err
		}
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
	fw, fwOK := firewall.Desired(lab, desired.Services)
	if fwOK {
		fmt.Printf("Firewall: SSH from %s", fw.SSHFrom)
		for _, rule := range fw.Allow {
			fmt.Printf(", %d/%s from %s", rule.Port, rule.Proto, rule.From)
		}
		fmt.Println()
	}
	if *dryRun {
		return nil
	}
	if err := r.Apply(ctx, plan); err != nil {
		return err
	}
	if fwOK {
		return agent.NewClient(agent.DefaultSocket).ApplyFirewall(ctx, fw)
	}
	return nil
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
