package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/nodeid"
)

const nodeUsage = `Usage:
  roostack node join <code> [-name <name>]   join a lab (the code comes from its Settings → Servers)

Run as root: the join itself runs as the roostack user, and root restarts
Roostack afterwards.
`

func nodeCmd(args []string) error {
	if len(args) < 2 || args[0] != "join" {
		fmt.Print(nodeUsage)
		return errors.New("node: missing arguments")
	}
	fs := flag.NewFlagSet("node join", flag.ExitOnError)
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	name := fs.String("name", "", "this server's name in the lab (default: its hostname)")
	code := args[1]
	fs.Parse(args[2:])

	c, err := labnet.ParseJoinCode(code)
	if err != nil {
		return err
	}
	say := func(format string, args ...any) { fmt.Printf("==> "+format+"\n", args...) }
	// The join writes the roostack user's files and calls the agent, both
	// of which the roostack user does; root only restarts the service
	// (asroostack.go).
	if os.Geteuid() == 0 {
		if err := runAsRoostack(append([]string{"node"}, args...)); err != nil {
			return err
		}
		say("Restarting Roostack as part of the lab")
		if out, err := exec.Command("systemctl", "restart", "roostack").CombinedOutput(); err != nil {
			return fmt.Errorf("restarting roostack: %w: %s", err, out)
		}
		say("Done. Manage this server from the lab's Settings → Servers.")
		return nil
	}
	lab, err := config.Load(*labPath)
	if err != nil {
		return err
	}
	self, err := nodeid.LoadOrCreate(lab.Paths.StateDir)
	if err != nil {
		return err
	}
	if *name != "" {
		self.Name = *name
		if err := self.Save(lab.Paths.StateDir); err != nil {
			return err
		}
	}
	ctx := context.Background()
	if err := labnet.JoinLab(ctx, c, self, lab.Paths.StateDir, agent.NewClient(agent.DefaultSocket), say); err != nil {
		return err
	}
	say("%s has joined the lab.", self.Name)
	if !fromRoot() {
		say("Restart Roostack to finish: sudo systemctl restart roostack")
	}
	return nil
}
