// Command roostack is the Roostack management service and CLI.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/labnet"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/pkgspec"
)

const usage = `Usage: roostack <command> [flags]

Commands:
  serve    Run the web UI
  apply    Make the host match desired state (use --dry-run to preview)
  user     Add a user or reset a password
  backup   Set up backups, back up now, list backups
  render   Show or write the files generated for a service (no side effects)
  validate Check a package file and the registry's package policy
  node     Join this server to a lab
  public   Pause public access to services, open it again, or see what's paused
  registry The service registry's keys and signed index

Run 'roostack <command> -h' for command flags.
`

// version is set at build time (-ldflags "-X main.version=..."; see the
// Makefile).
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	if asServiceUser[cmd] && os.Geteuid() == 0 {
		err = runAsRoostack(os.Args[1:]) // asroostack.go
	} else {
		setOwnSession()
		err = run(cmd, args)
	}
	var exit exitStatus
	if errors.As(err, &exit) {
		os.Exit(int(exit))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "roostack:", err)
		os.Exit(1)
	}
}

// run runs one command.
func run(cmd string, args []string) error {
	switch cmd {
	case "serve":
		return serve(args)
	case "apply":
		return apply(args)
	case "user":
		return user(args)
	case "backup":
		return backupCmd(args)
	case "backup-helper": // internal: run inside podman unshare
		return backupHelper(args)
	case "render":
		return render(args)
	case "validate":
		return validate(args)
	case "node":
		return nodeCmd(args)
	case "public":
		return publicCmd(args)
	case "registry":
		return registryCmd(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprintf(os.Stderr, "roostack: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
		return nil
	}
}

func render(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	labPath := fs.String("lab", "", "lab config file (required without -package; with it, an example lab when omitted)")
	pkgPath := fs.String("package", "", "package file; omit to render the lab-wide system units")
	instPath := fs.String("instance", "", "instance file; omit for the package's default settings")
	outDir := fs.String("out", "", "write files under this directory instead of printing them")
	gpu := fs.Bool("gpu", false, "pretend the host has a GPU (resolves default: auto)")
	fs.Parse(args)

	// A package renders without a lab of its own, in an example lab, so
	// anyone can see what it generates (SPEC.md §8.3.7).
	lab := generate.ExampleLab()
	switch {
	case *labPath != "":
		var err error
		if lab, err = config.Load(*labPath); err != nil {
			return err
		}
	case *pkgPath == "":
		return errors.New("render: -lab is required without -package")
	}
	lab.HasGPU = *gpu

	if *pkgPath == "" {
		out, err := generate.System(lab, false)
		if err != nil {
			return err
		}
		if *outDir == "" {
			for _, f := range out.Quadlets {
				fmt.Printf("==> quadlet/%s\n%s\n", f.Name, f.Content)
			}
			fmt.Printf("==> caddy/%s\n%s", out.Caddyfile.Name, out.Caddyfile.Content)
			return nil
		}
		files := prefixed("quadlet", out.Quadlets)
		files = append(files, prefixed("caddy", []generate.File{out.Caddyfile})...)
		return writeFiles(*outDir, files)
	}

	p, err := pkgspec.Load(*pkgPath)
	if err != nil {
		return err
	}
	// Still rendered, so an author sees what it would generate.
	for _, problem := range p.Policy() {
		fmt.Fprintf(os.Stderr, "%s: package policy: %s\n", *pkgPath, problem)
	}
	in := generate.ExampleInstance(p)
	if *instPath != "" {
		if in, err = instance.Load(*instPath); err != nil {
			return err
		}
	}
	out, err := generate.Service(lab, p, in)
	if err != nil {
		return err
	}
	if *outDir == "" {
		fmt.Print(generate.Dump(out))
		return nil
	}
	files := prefixed("quadlet", out.Quadlets)
	if out.CaddySite != nil {
		files = append(files, prefixed("caddy/sites", []generate.File{*out.CaddySite})...)
	}
	// Config files with their secrets as placeholders: the server fills
	// them in as it writes them.
	for _, f := range out.ConfigFiles {
		files = append(files, generate.File{Name: filepath.Join("config", f.Name, f.Path), Content: f.Shown()})
	}
	return writeFiles(*outDir, files)
}

func validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	local := fs.Bool("local", false, "a package of your own: report what it asks for that registry packages can't have as warnings, not errors")
	fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("validate: give one or more package files")
	}
	var errs []error
	for _, path := range fs.Args() {
		p, err := pkgspec.Load(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ok := true
		// Registry packages must follow the package policy; one of your own
		// may break it, and leave out the facts registry packages give: what
		// it asks for is shown before installing, as here (SPEC.md §8.3.4).
		problems := p.Policy()
		if *local {
			problems = p.Risks()
		}
		for _, problem := range problems {
			if *local {
				fmt.Printf("%s: warning: package policy: %s\n", path, problem)
				continue
			}
			errs = append(errs, fmt.Errorf("%s: package policy: %s", path, problem))
			ok = false
		}
		if err := generate.Check(p); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			ok = false
		}
		// The logo it names, beside the package file (SPEC.md §8.3.6).
		if err := p.CheckIcon(filepath.Dir(path)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", path, err))
			ok = false
		}
		if ok {
			fmt.Printf("%s: ok\n", path)
		}
	}
	return errors.Join(errs...)
}

func prefixed(dir string, files []generate.File) []generate.File {
	out := make([]generate.File, len(files))
	for i, f := range files {
		out[i] = generate.File{Name: filepath.Join(dir, f.Name), Content: f.Content}
	}
	return out
}

func writeFiles(root string, files []generate.File) error {
	for _, f := range files {
		path := filepath.Join(root, f.Name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(f.Content), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	return nil
}

// withNode loads this server's identity into lab (creating it on first
// use), so only its own services are generated and run (SPEC.md §10.4).
func withNode(lab *config.Lab) (nodeid.Identity, error) {
	id, err := nodeid.LoadOrCreate(lab.Paths.StateDir)
	if err != nil {
		return id, fmt.Errorf("this server's identity: %w", err)
	}
	lab.NodeID = id.ID
	lab.Role = config.RoleController
	// A server in a lab with a network (SPEC.md §10.2) knows its role.
	m, ok, err := labnet.LoadMembership(lab.Paths.StateDir)
	if err != nil {
		return id, err
	}
	if ok {
		m.Apply(lab)
	}
	return id, nil
}
