package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/imagecheck"
	"roostack.cloud/roostack/internal/registry"
)

const registryUsage = `Usage: roostack registry <command> [flags]

The service registry's keys and index (SPEC.md §8.3.3, docs/REGISTRY-KEYS.md).

Commands:
  keygen    Make a key pair: the private half to a file, the public half printed
  delegate  Write keys.json naming the signing keys, signed by the root key
  index     Check every package and build the signed index (the registry's CI)
  check     Fetch a registry's index as a lab would, and say what it holds
  watch     Read the sources of security problems and draft advisories (the registry's CI)
`

func registryCmd(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, registryUsage)
		os.Exit(2)
	}
	switch cmd, args := args[0], args[1:]; cmd {
	case "keygen":
		return registryKeygen(args)
	case "delegate":
		return registryDelegate(args)
	case "index":
		return registryIndex(args)
	case "check":
		return registryCheck(args)
	case "watch":
		return registryWatch(args)
	case "-h", "--help", "help":
		fmt.Print(registryUsage)
		return nil
	default:
		return fmt.Errorf("registry: unknown command %q", cmd)
	}
}

func registryKeygen(args []string) error {
	fs := flag.NewFlagSet("registry keygen", flag.ExitOnError)
	out := fs.String("out", "", "file for the private key (required; must not exist)")
	fs.Parse(args)
	if *out == "" {
		return errors.New("registry keygen: -out is required")
	}
	pub, priv, err := registry.NewKey()
	if err != nil {
		return err
	}
	// O_EXCL: never overwrite a key.
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(priv); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Wrote the private key to %s (keep it secret). Its public key:\n", *out)
	fmt.Println(registry.EncodePublic(pub))
	return nil
}

func registryDelegate(args []string) error {
	fs := flag.NewFlagSet("registry delegate", flag.ExitOnError)
	var rootPaths stringList
	fs.Var(&rootPaths, "root", "a root key's private key file (required; give it twice to sign with two root keys)")
	serial := fs.Int64("serial", 0, "the statement's serial: one more than the last (required)")
	out := fs.String("out", ".", "folder for keys.json and keys.json.sig (the registry repository's keys/)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: roostack registry delegate -root FILE -serial N [-out DIR] SIGNING-PUBLIC-KEY...")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if len(rootPaths) == 0 || *serial < 1 || fs.NArg() == 0 {
		fs.Usage()
		os.Exit(2)
	}
	var roots []ed25519.PrivateKey
	for _, path := range rootPaths {
		root, err := registry.LoadPrivate(path)
		if err != nil {
			return err
		}
		roots = append(roots, root)
	}
	var signing []ed25519.PublicKey
	for _, s := range fs.Args() {
		k, err := registry.ParsePublic(s)
		if err != nil {
			return err
		}
		signing = append(signing, k)
	}
	data, sig, err := registry.Delegate(*serial, time.Now(), signing, roots...)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for name, content := range map[string][]byte{registry.KeysFile: data, registry.KeysSigFile: sig} {
		path := filepath.Join(*out, name)
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", path)
	}
	for _, root := range roots {
		fmt.Fprintf(os.Stderr, "Signed by the root key %s.\n", registry.EncodePublic(root.Public().(ed25519.PublicKey)))
	}
	return nil
}

// stringList is a flag given more than once.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(s string) error { *l = append(*l, s); return nil }

func registryIndex(args []string) error {
	fs := flag.NewFlagSet("registry index", flag.ExitOnError)
	repo := fs.String("repo", ".", "a checkout of the registry repository")
	out := fs.String("out", "", "folder to write the files to publish to (required)")
	keyPath := fs.String("key", "", "the signing key's file; default: the REGISTRY_SIGNING_KEY environment variable")
	commit := fs.String("commit", "", "the registry commit being built, recorded in the index")
	noImages := fs.Bool("skip-image-check", false, "don't ask the image registries for architectures (testing only)")
	check := fs.Bool("check", false, "only check the packages (a pull request): nothing will be signed or written")
	fs.Parse(args)
	if *out == "" && !*check {
		return errors.New("registry index: -out is required")
	}
	var key ed25519.PrivateKey
	var err error
	if *check {
		// No key: nothing is signed.
	} else if *keyPath != "" {
		key, err = registry.LoadPrivate(*keyPath)
	} else if env := os.Getenv("REGISTRY_SIGNING_KEY"); env != "" {
		key, err = registry.ParsePrivate(env)
	} else {
		err = errors.New("registry index: give -key, or set REGISTRY_SIGNING_KEY")
	}
	if err != nil {
		return err
	}
	roots, err := registry.RootKeys()
	if err != nil {
		return err
	}
	opts := registry.BuildOptions{Repo: *repo, Out: *out, Key: key, Roots: roots, Commit: *commit,
		Platforms: imagecheck.Platforms, Now: time.Now()}
	if *noImages {
		opts.Platforms = nil
	}
	ix, err := registry.Build(opts)
	if err != nil {
		return err
	}
	versions := 0
	for _, p := range ix.Packages {
		versions += len(p.Versions)
	}
	if *check {
		fmt.Printf("Checked %d packages, %d versions: all fine\n", len(ix.Packages), versions)
		return nil
	}
	fmt.Printf("Built index %d: %d packages, %d versions, in %s\n", ix.Serial, len(ix.Packages), versions, *out)
	return nil
}

func registryCheck(args []string) error {
	fs := flag.NewFlagSet("registry check", flag.ExitOnError)
	url := fs.String("url", registry.DefaultURL, "the registry's address")
	arch := fs.String("arch", runtime.GOARCH, "the processor to check for")
	fs.Parse(args)
	roots, err := registry.RootKeys()
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "roostack-registry-check")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	f := &registry.Fetcher{URL: *url, Dir: dir, Roots: roots, Version: version, Arches: func() []string { return []string{*arch} }}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = f.Fetch(ctx)
	st := f.Status()
	if st.Serial != 0 {
		fmt.Printf("Index %d, signed %s, from registry commit %s\n", st.Serial, st.Date.Format(time.RFC3339), st.Commit)
		fmt.Printf("Key statement %d. %d packages this Roostack (%s, %s) can install, all checked\n", st.KeysSerial, st.Packages, version, *arch)
		for _, name := range slices.Sorted(maps.Keys(st.Skipped)) {
			fmt.Printf("  not offered: %s: %s\n", name, st.Skipped[name])
		}
	}
	return err
}
