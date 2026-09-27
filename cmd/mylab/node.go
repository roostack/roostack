package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"strconv"

	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/labnet"
	"github.com/mylab/mylab/internal/nodeid"
)

const nodeUsage = `Usage:
  mylab node join <code> [-name <name>]   join a lab (the code comes from its Settings → Servers)

Run as root.
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
	say := func(format string, args ...any) { fmt.Printf("==> "+format+"\n", args...) }
	ctx := context.Background()
	if err := labnet.JoinLab(ctx, c, self, lab.Paths.StateDir, agent.NewClient(agent.DefaultSocket), say); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		// Files written as root must belong to the mylab service.
		if err := chownToMylab(labnet.IdentityDir(lab.Paths.StateDir), nodeid.File(lab.Paths.StateDir)); err != nil {
			return err
		}
		say("Restarting MyLab as part of the lab")
		if out, err := exec.Command("systemctl", "restart", "mylab").CombinedOutput(); err != nil {
			return fmt.Errorf("restarting mylab: %w: %s", err, out)
		}
	} else {
		say("Restart MyLab to finish: sudo systemctl restart mylab")
	}
	say("Done: %s is now part of the lab. Manage it from the lab's Settings → Servers.", self.Name)
	return nil
}

func chownToMylab(paths ...string) error {
	u, err := osuser.Lookup("mylab")
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	for _, p := range paths {
		err := filepath.Walk(p, func(path string, _ os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(path, uid, gid)
		})
		if err != nil {
			return err
		}
	}
	return nil
}
