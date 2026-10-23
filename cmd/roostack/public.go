package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/words"
)

const publicUsage = `Usage:
  roostack public status
  roostack public pause  [-all] [-built-with php,log4j] [-service lychee,ghost]
  roostack public resume [-all] [-built-with php,log4j] [-service lychee,ghost]

Pausing public access takes services off the internet: visitors from
outside will see a page saying it's closed for a while, and at home
everything will keep working. It's for when the web UI can't be reached.
Run it on the lab's controller as root: the change will be saved as the
roostack user, and root will then tell the running Roostack to apply it.
`

// publicCmd pauses public access, opens it again, or says what's paused,
// from the server's command line (SPEC.md §6.16): the way to do it when
// Roostack's pages can't be reached or don't work.
func publicCmd(args []string) error {
	if len(args) < 1 || (args[0] != "status" && args[0] != "pause" && args[0] != "resume") {
		fmt.Print(publicUsage)
		return errors.New("public: say status, pause or resume")
	}
	sub := args[0]
	fs := flag.NewFlagSet("public "+sub, flag.ExitOnError)
	labPath := fs.String("lab", config.Default().Paths.LabFile(), "lab config file")
	all := fs.Bool("all", false, "everything public")
	builtWith := fs.String("built-with", "", "services built with these names, separated by commas")
	services := fs.String("service", "", "these services, separated by commas")
	fs.Parse(args[1:])
	req := manager.PauseRequest{All: *all, BuiltWith: splitList(*builtWith), Services: splitList(*services)}

	say := func(format string, args ...any) { fmt.Printf("==> "+format+"\n", args...) }
	// The roostack user's files are changed as that user; root only
	// restarts the service (asroostack.go).
	if os.Geteuid() == 0 {
		if err := runAsRoostack(append([]string{"public"}, args...)); errors.Is(err, nothingChanged) {
			return nil
		} else if err != nil {
			return err
		}
		if sub == "status" {
			return nil
		}
		if exec.Command("systemctl", "is-active", "--quiet", "roostack").Run() != nil {
			say("Roostack isn't running, so the change is being applied here (this server's services only)")
			return runAsRoostack([]string{"apply"})
		}
		// The running Roostack reads lab.yaml's pauses again on SIGHUP
		// and applies them at once, without a restart (serve.go).
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
	installed, err := state.LoadServices(lab)
	if err != nil && len(installed) == 0 {
		return err
	}
	// Titles of built-with names, from the registry's index the lab
	// fetched last (none before the first).
	ix, _ := (&registry.Fetcher{Dir: lab.Paths.CatalogDir}).Index()
	pp := manager.PublicPausesOf(lab, installed).WithAdvisories(manager.AdvisoriesOf(lab, ix, installed)).WithTitles(ix)
	if sub == "status" {
		fmt.Print(pauseStatus(pp))
		return nil
	}
	if !req.All && len(req.BuiltWith) == 0 && len(req.Services) == 0 {
		return fmt.Errorf("say what to %s: -all, -built-with or -service", map[string]string{"pause": "pause", "resume": "open again"}[sub])
	}
	if err := req.Check(); err != nil {
		return err
	}
	var named []state.Service
	if sub == "pause" {
		named, err = manager.PublicServicesNamed(lab, installed, req.Services)
	} else {
		named, err = servicesNamed(installed, req.Services)
	}
	if errors.Is(err, manager.ErrNotFound) {
		return fmt.Errorf("there's no service named %s", strings.Join(req.Services, " or "))
	}
	if err != nil {
		return err
	}
	// Only the Owner's choice on the service's page, or an update, ends a
	// pause Roostack placed for an advisory.
	stays := func() {
		for _, s := range pp.Services {
			if s.Advisory != nil && (req.All || slices.Contains(req.Services, s.Name) || slices.ContainsFunc(s.BuiltWith, func(b string) bool { return slices.Contains(req.BuiltWith, b) })) {
				say("%s will stay paused for security advisory %s until it's updated, or you choose Keep it public anyway on its page.", s.Title, s.Advisory.ID)
			}
		}
	}
	if sub == "resume" && !req.All && len(req.BuiltWith) == 0 && !slices.ContainsFunc(named, func(s state.Service) bool { return s.Instance.PublicPause != nil }) {
		// Nothing a person paused: say why each is paused, if it is.
		for _, s := range named {
			if i := slices.IndexFunc(pp.Services, func(ps manager.PublicService) bool { return ps.Name == s.Name() }); i < 0 || !pp.Services[i].Paused() {
				say("%s isn't paused.", s.Title())
			}
		}
		stays()
		if fromRoot() {
			return nothingChanged // root needn't tell Roostack
		}
		return nil
	}
	var pause *config.Pause
	if sub == "pause" {
		pause = &config.Pause{Since: time.Now().UTC().Truncate(time.Second)}
	}
	if req.All || len(req.BuiltWith) > 0 {
		err := config.Update(*labPath, func(l *config.Lab) {
			if pause != nil {
				l.Public.Pause = req.Paused(l.Public.Pause, *pause)
			} else {
				l.Public.Pause = req.Lifted(l.Public.Pause)
			}
		})
		if err != nil {
			return err
		}
	}
	if err := manager.SaveServicePauses(lab, named, pause); err != nil {
		return err
	}
	what := manager.PauseWhat(req, named, pp.Title)
	action, done := attention.AuditPublicPaused, "Paused public access to "+what
	if pause == nil {
		action, done = attention.AuditPublicOpened, "Opened public access to "+what+" again"
	}
	// Recorded like the web UI's changes, so the Owners will hear of it
	// by email (SPEC.md §6.13).
	if st, err := store.Open(lab.Paths.DatabaseFile()); err == nil {
		st.Audit(context.Background(), nil, action, what+" via command line")
		st.Close()
	} else {
		fmt.Fprintf(os.Stderr, "roostack: couldn't record it in the history: %v\n", err)
	}
	say("%s.", done)
	if pause == nil {
		stays()
	}
	if !fromRoot() {
		say("Tell Roostack to apply the change: sudo systemctl kill --signal=HUP --kill-whom=main roostack")
	}
	return nil
}

// nothingChanged is the exit status of roostack public, re-run as the
// roostack user by root, when it changed nothing for Roostack to apply.
const nothingChanged = exitStatus(3)

// servicesNamed are the named services of all.
func servicesNamed(all []state.Service, names []string) ([]state.Service, error) {
	var out []state.Service
	for _, name := range names {
		found := false
		for _, s := range all {
			if s.Name() == name {
				out, found = append(out, s), true
			}
		}
		if !found {
			return nil, manager.ErrNotFound
		}
	}
	return out, nil
}

// pauseStatus says what's public and what's paused, for status.
func pauseStatus(pp manager.PublicPauses) string {
	var b strings.Builder
	if len(pp.Services) == 0 {
		b.WriteString("Nothing is open to the internet.\n")
	}
	if p := pp.All; p != nil {
		fmt.Fprintf(&b, "Everything public is paused, since %s.\n", pauseSince(*p))
	}
	for _, name := range slices.Sorted(maps.Keys(pp.BuiltWith)) {
		fmt.Fprintf(&b, "Services built with %s are paused, since %s.\n", pp.Title(name), pauseSince(pp.BuiltWith[name]))
	}
	for _, s := range pp.Services {
		state := "public"
		if s.Paused() {
			var why []string
			if s.Own != nil {
				why = append(why, "its own pause, since "+pauseSince(*s.Own))
			}
			if s.ByAll {
				why = append(why, "everything")
			}
			if len(s.ByBuiltWith) > 0 {
				var bw []string
				for _, n := range s.ByBuiltWith {
					bw = append(bw, pp.Title(n))
				}
				why = append(why, "built with "+words.And(bw))
			}
			if a := s.Advisory; a != nil {
				why = append(why, fmt.Sprintf("by Roostack for security advisory %s: %s", a.ID, strings.TrimRight(a.Title, ".")))
			}
			state = "paused (" + strings.Join(why, "; ") + ")"
		}
		fmt.Fprintf(&b, "%s (%s): %s\n", s.Title, s.Name, state)
	}
	return b.String()
}

// pauseSince is when and by whom: "2026-10-23 09:14 by tom".
func pauseSince(p config.Pause) string {
	by := " by " + p.By
	if p.By == "" {
		by = " from the command line"
	}
	return p.Since.Local().Format("2006-01-02 15:04") + by
}

// splitList is a comma-separated flag's values.
func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
