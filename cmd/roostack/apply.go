package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/firewall"
	"roostack.cloud/roostack/internal/hosttime"
	"roostack.cloud/roostack/internal/podman"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/systemd"
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
	// As serve does: private addresses on a lab on the internet use it.
	if ip, err := dnsrecords.DetectLANIP(); err == nil {
		lab.HostIP = ip
	}
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
			// A rule for one interface (Roostack's WireGuard network) has
			// no source: anything arriving on that interface may use it.
			if rule.Iface != "" {
				fmt.Printf(", %d/%s on %s", rule.Port, rule.Proto, rule.Iface)
			} else {
				fmt.Printf(", %d/%s from %s", rule.Port, rule.Proto, rule.From)
			}
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
// roostack-caddy-dns-token from ROOSTACK_SECRET_ROOSTACK_CADDY_DNS_TOKEN.
//
// Temporary: the web UI will collect these during setup (TASKS 1.9).
func envSecret(name string) ([]byte, bool) {
	key := "ROOSTACK_SECRET_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(name))
	v, ok := os.LookupEnv(key)
	return []byte(v), ok && v != ""
}
