package registry

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
)

// The registry's CI builds and signs the index from a checkout of the
// registry repository (SPEC.md §8.3.3): roostack registry index.

// RegistryFacts is a package's registry.yaml: what the registry's
// maintainers, not the package's author, say about it (SPEC.md §8.3.2).
type RegistryFacts struct {
	Tier        string             `yaml:"tier"`
	Maintainers []string           `yaml:"maintainers"`
	Withdrawals []RegistryWithdraw `yaml:"withdrawals"`
}

// RegistryWithdraw is one withdrawal in registry.yaml: of a version, or
// of the whole package when Version is empty.
type RegistryWithdraw struct {
	Version string `yaml:"version"`
	Level   string `yaml:"level"`
	Reason  string `yaml:"reason"`
	Link    string `yaml:"link"`
}

// BuildOptions says what to build the index from and how to sign it.
type BuildOptions struct {
	// Repo is a checkout of the registry repository: packages/ and keys/.
	Repo string
	// Out is the folder to write the published files to: index.json,
	// keys.json, their signatures, and packages/ (SPEC.md §8.3.3).
	Out string
	// Key is the signing key; keys/keys.json must name it. Nil only
	// checks the packages and builds the index (a pull request's check):
	// nothing is signed or written, and keys/ isn't needed.
	Key ed25519.PrivateKey
	// Roots are the root keys keys/keys.json must be signed by (RootKeys).
	Roots []ed25519.PublicKey
	// Commit is the registry commit being built, for the index.
	Commit string
	// Platforms returns an image's os/architecture pairs
	// (imagecheck.Platforms); nil leaves the architectures out.
	Platforms func(image string) ([]string, error)
	// Now is when it's signed: the index's date and serial.
	Now time.Time
}

// Build checks every package in the repository as roostack validate does
// (the package policy included), builds the index, signs it, and writes
// the files to publish into opts.Out. Any problem in any package fails
// the build: a registry with a broken package must be fixed, not
// published without it.
func Build(opts BuildOptions) (*Index, error) {
	var keysData, keysSig []byte
	if opts.Key != nil {
		var err error
		if keysData, err = os.ReadFile(filepath.Join(opts.Repo, "keys", KeysFile)); err != nil {
			return nil, err
		}
		if keysSig, err = os.ReadFile(filepath.Join(opts.Repo, "keys", KeysSigFile)); err != nil {
			return nil, err
		}
		statement, err := ParseKeys(keysData, keysSig, opts.Roots)
		if err != nil {
			return nil, err
		}
		signing, _ := statement.Keys()
		if pub := opts.Key.Public().(ed25519.PublicKey); !contains(signing, pub) {
			return nil, fmt.Errorf("keys/%s doesn't name the signing key (%s)", KeysFile, EncodePublic(pub))
		}
	}

	now := opts.Now.UTC().Truncate(time.Second)
	ix := &Index{Type: TypeIndex, Format: Format, Serial: now.Unix(), Date: now, Commit: opts.Commit, Packages: map[string]*Package{}}
	files := map[string][]byte{} // published path → contents
	dirs, err := os.ReadDir(filepath.Join(opts.Repo, "packages"))
	if err != nil {
		return nil, err
	}
	platforms := map[string][]string{} // image → its architectures, asked once
	var errs []error
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		p, pkgFiles, err := buildPackage(filepath.Join(opts.Repo, "packages"), d.Name(), opts.Platforms, platforms)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ix.Packages[d.Name()] = p
		maps.Copy(files, pkgFiles)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	if err := ix.Check(); err != nil {
		return nil, err
	}
	if opts.Key == nil {
		return ix, nil
	}

	data, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	files[IndexFile] = data
	files[IndexSigFile] = Sign(data, opts.Key)
	files[KeysFile] = keysData
	files[KeysSigFile] = keysSig
	for path, content := range files {
		dst := filepath.Join(opts.Out, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, content, 0o644); err != nil {
			return nil, err
		}
	}
	return ix, nil
}

// buildPackage reads one package folder: its registry.yaml, each
// <version>.yaml and its icon. It returns the index's entry and the files
// to publish.
func buildPackage(root, name string, lookup func(string) ([]string, error), seen map[string][]string) (*Package, map[string][]byte, error) {
	dir := filepath.Join(root, name)
	fail := func(format string, args ...any) (*Package, map[string][]byte, error) {
		return nil, nil, fmt.Errorf("packages/%s: %s", name, fmt.Sprintf(format, args...))
	}
	if !pkgspec.ValidName(name) {
		return fail("not a valid package name")
	}
	factsData, err := os.ReadFile(filepath.Join(dir, "registry.yaml"))
	if err != nil {
		return fail("%v", err)
	}
	var facts RegistryFacts
	dec := yaml.NewDecoder(bytes.NewReader(factsData))
	dec.KnownFields(true)
	if err := dec.Decode(&facts); err != nil {
		return fail("registry.yaml: %v", err)
	}
	p := &Package{Tier: facts.Tier, Maintainers: facts.Maintainers, Versions: map[string]*Version{}}
	if len(p.Maintainers) == 0 {
		return fail("registry.yaml: no maintainers")
	}
	files := map[string][]byte{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fail("%v", err)
	}
	var newest *pkgspec.Package
	var errs []string
	for _, e := range entries {
		version, ok := strings.CutSuffix(e.Name(), ".yaml")
		if e.IsDir() || !ok || version == "registry" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fail("%v", err)
		}
		pkg, err := pkgspec.Parse(data)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		if pkg.Name != name || pkg.Version != version {
			errs = append(errs, fmt.Sprintf("%s is %s %s: name the file after its version, in a folder named after the package", e.Name(), pkg.Name, pkg.Version))
			continue
		}
		for _, problem := range pkg.Policy() {
			errs = append(errs, fmt.Sprintf("%s: package policy: %s", e.Name(), problem))
		}
		if err := generate.Check(pkg); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", e.Name(), err))
		}
		if err := pkg.CheckIcon(dir); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", e.Name(), err))
		}
		published := "packages/" + name + "/" + e.Name()
		v := &Version{File: fileOf(published, data), Schema: pkg.Schema, RequiresRoostack: pkg.Requires.Roostack}
		if lookup != nil {
			arch, err := architectures(pkg, lookup, seen)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", e.Name(), err))
			}
			v.Architectures = arch
		}
		p.Versions[version] = v
		files[published] = data
		if newest == nil || pkgspec.CompareVersions(version, newest.Version) > 0 {
			newest = pkg
		}
	}
	if len(errs) > 0 {
		return fail("%s", strings.Join(errs, "\n  "))
	}
	if newest == nil {
		return fail("no package file (<version>.yaml)")
	}
	p.Title, p.Description, p.Categories = newest.Title, newest.Description, newest.About.Categories
	if newest.About.Icon != "" {
		data, err := os.ReadFile(filepath.Join(dir, newest.About.Icon))
		if err != nil {
			return fail("%v", err)
		}
		published := "packages/" + name + "/icon.png"
		f := fileOf(published, data)
		p.Icon = &f
		files[published] = data
	}
	for _, w := range facts.Withdrawals {
		wd := &Withdrawal{Level: w.Level, Reason: w.Reason, Link: w.Link}
		if err := wd.check(); err != nil {
			return fail("registry.yaml: %v", err)
		}
		if w.Version == "" {
			p.Withdrawn = wd
			continue
		}
		v, ok := p.Versions[w.Version]
		if !ok {
			return fail("registry.yaml withdraws version %s, which isn't here", w.Version)
		}
		v.Withdrawn = wd
	}
	return p, files, nil
}

// architectures are the processors every image of the package has a
// build for, as GOARCH names them. A database without its own image uses
// Roostack's, which Roostack checks (make check-images).
func architectures(p *pkgspec.Package, lookup func(string) ([]string, error), seen map[string][]string) ([]string, error) {
	var images []string
	for _, c := range p.Containers {
		images = append(images, c.Image)
	}
	for _, d := range p.Databases {
		if d.Image != "" {
			images = append(images, d.Image)
		}
	}
	var common []string
	for i, image := range slices.Sorted(slices.Values(images)) {
		archs, ok := seen[image]
		if !ok {
			platforms, err := lookup(image)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", image, err)
			}
			for _, pl := range platforms {
				if a, ok := strings.CutPrefix(pl, "linux/"); ok && !slices.Contains(archs, a) {
					archs = append(archs, a)
				}
			}
			seen[image] = archs
		}
		if i == 0 {
			common = slices.Clone(archs)
		} else {
			common = slices.DeleteFunc(common, func(a string) bool { return !slices.Contains(archs, a) })
		}
	}
	slices.Sort(common)
	if len(common) == 0 {
		return nil, errors.New("its images have no processor in common")
	}
	return common, nil
}

func fileOf(path string, data []byte) File {
	return File{Path: path, SHA256: sum(data), Size: int64(len(data))}
}
