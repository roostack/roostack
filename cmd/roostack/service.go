package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
)

const serviceUsage = `Usage:
  roostack service off <name>
  roostack service on  <name>

Turning a service off stops it and keeps it stopped, after restarts too,
until it's turned on again: its data, settings and last backup are kept,
and its address will show a page saying it's turned off. It's the same as
"Turn it off for a while" on the service's page. Run it on the lab's
controller as root: the change will be saved as the roostack user, and root
will then tell the running Roostack to apply it.
`

// serviceCmd turns a service off for a while, or on again, from the
// server's command line (SPEC.md §6.3.5).
func serviceCmd(args []string) error {
	if len(args) < 2 || (args[0] != "off" && args[0] != "on") {
		fmt.Print(serviceUsage)
		return errors.New("service: say off or on, and the service's name")
	}
	sub, name := args[0], args[1]
	fs := flag.NewFlagSet("service "+sub, flag.ExitOnError)
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	fs.Parse(args[2:])
	say := func(format string, args ...any) { fmt.Printf("==> "+format+"\n", args...) }

	// As in roostack public: the roostack user's files are changed as
	// that user, and root tells Roostack (asroostack.go).
	if os.Geteuid() == 0 {
		if err := runAsRoostack(append([]string{"service"}, args...)); errors.Is(err, nothingChanged) {
			return nil
		} else if err != nil {
			return err
		}
		if exec.Command("systemctl", "is-active", "--quiet", "roostack").Run() != nil {
			say("Roostack isn't running, so the change is being applied here (this server's services only)")
			return runAsRoostack([]string{"apply"})
		}
		// The running Roostack applies again on SIGHUP (serve.go), which
		// reads the service's file afresh.
		if out, err := exec.Command("systemctl", "kill", "--signal=HUP", "--kill-whom=main", "roostack").CombinedOutput(); err != nil {
			return fmt.Errorf("telling Roostack: %w: %s", err, out)
		}
		say("Done: Roostack is applying it now, which usually takes a few seconds.")
		return nil
	}

	lab, err := config.Load(*labPath)
	if err != nil {
		return err
	}
	if lab.IsNode() {
		return errors.New("this server isn't the lab's controller: run it on the controller, which keeps the lab's settings")
	}
	if !instance.ValidName(name) {
		return fmt.Errorf("there's no service named %s", name)
	}
	path := state.InstanceFile(lab, name)
	in, err := instance.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("there's no service named %s", name)
	}
	if err != nil {
		return err
	}
	title := in.Title
	if title == "" {
		title = name
	}
	off := sub == "off"
	switch {
	case off && in.Off != nil:
		say("%s is already off.", title)
		return nothingChangedIfRoot()
	case !off && in.Off == nil:
		say("%s isn't off.", title)
		return nothingChangedIfRoot()
	case off && (in.AwaitingData || in.Held != nil || in.Waiting):
		return fmt.Errorf("%s can't be turned off now: see its page", title)
	}
	if off {
		in.Off = &config.Pause{Since: time.Now()}
	} else {
		in.Off = nil
	}
	if err := in.Save(path); err != nil {
		return err
	}
	if st, err := store.Open(lab.Paths.DatabaseFile()); err == nil {
		st.Audit(context.Background(), nil, "service "+sub, name+" via command line")
		st.Close()
	} else {
		fmt.Fprintf(os.Stderr, "roostack: couldn't record it in the history: %v\n", err)
	}
	say("Turned %s %s.", title, sub)
	if !fromRoot() {
		say("Tell Roostack to apply the change: sudo systemctl kill --signal=HUP --kill-whom=main roostack")
	}
	return nil
}

// nothingChangedIfRoot tells root there's nothing to apply.
func nothingChangedIfRoot() error {
	if fromRoot() {
		return nothingChanged
	}
	return nil
}
