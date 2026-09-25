// Command mylab is the MyLab management service and CLI.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/generate"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/pkgspec"
)

const usage = `Usage: mylab <command> [flags]

Commands:
  serve    Run the web UI
  apply    Make the host match desired state (use --dry-run to preview)
  user     Add a user or reset a password
  backup   Set up backups, back up now, list backups
  render   Show or write the files generated for a service (no side effects)
  validate Check a package file

Run 'mylab <command> -h' for command flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = serve(args)
	case "apply":
		err = apply(args)
	case "user":
		err = user(args)
	case "backup":
		err = backupCmd(args)
	case "backup-helper": // internal: run inside podman unshare
		err = backupHelper(args)
	case "render":
		err = render(args)
	case "validate":
		err = validate(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "mylab: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mylab:", err)
		os.Exit(1)
	}
}

func render(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	labPath := fs.String("lab", "", "lab config file (required)")
	pkgPath := fs.String("package", "", "package file; omit to render the lab-wide system units")
	instPath := fs.String("instance", "", "instance file (required with -package)")
	outDir := fs.String("out", "", "write files under this directory instead of printing them")
	gpu := fs.Bool("gpu", false, "pretend the host has a GPU (resolves default: auto)")
	fs.Parse(args)

	if *labPath == "" {
		return errors.New("render: -lab is required")
	}
	lab, err := config.Load(*labPath)
	if err != nil {
		return err
	}
	lab.HasGPU = *gpu

	if *pkgPath == "" {
		out, err := generate.System(lab)
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

	if *instPath == "" {
		return errors.New("render: -instance is required with -package")
	}
	p, err := pkgspec.Load(*pkgPath)
	if err != nil {
		return err
	}
	in, err := instance.Load(*instPath)
	if err != nil {
		return err
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
	return writeFiles(*outDir, files)
}

func validate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
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
		for _, w := range p.Warnings() {
			fmt.Printf("%s: warning: %s\n", path, w)
		}
		fmt.Printf("%s: ok\n", path)
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
