package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/state"
)

// LeftData is a server that can say whether a service's data from before
// is still on it (removing a service keeps its data), for the install page
// to say it will be used (SPEC.md §8.1, "hooks"); list every removed
// service's data still on it; and delete one's for good (SPEC.md §8.1,
// "Data left by removed services").
type LeftData interface {
	// LeftDataDirs lists the service's data folders that have something
	// in them (none: it would start afresh).
	LeftDataDirs(ctx context.Context, name string) ([]string, error)
	// LeftServices lists the data of services no longer on this server.
	LeftServices(ctx context.Context) ([]LeftService, error)
	// DeleteLeftData deletes a removed service's folders and the secrets
	// Roostack kept for it.
	DeleteLeftData(ctx context.Context, name string) error
}

// LeftService is a removed service's data on a server: its folders (the
// ones there are), and their size as last counted (-1 before the first
// count, which then starts), with when.
type LeftService struct {
	Name    string    `json:"name"`
	Dirs    []string  `json:"dirs"`
	Size    int64     `json:"size"`
	Counted time.Time `json:"counted,omitzero"`
}

// LeftData is this server, which looks in its own folders.
func (n *Local) LeftData() LeftData { return n }

var (
	_ LeftData = (*Local)(nil)
	_ LeftData = (*Remote)(nil)
)

// LeftDataDirs looks in the service's folders on this server.
func (n *Local) LeftDataDirs(_ context.Context, name string) ([]string, error) {
	if !instance.ValidName(name) {
		return nil, ErrNotFound
	}
	var out []string
	for _, dir := range n.Lab.Paths.ServiceDataDirs(name) {
		f, err := os.Open(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		_, err = f.Readdirnames(1)
		f.Close()
		if err == nil {
			out = append(out, dir)
		} else if !errors.Is(err, io.EOF) {
			return nil, err
		}
	}
	return out, nil
}

// leftFolders are the folders in the services and media folders named
// like a service that isn't assigned here, by name, each name's that
// exist and have something in them. Kept copies and data being brought
// in have a dot in their names, which no service's name has, so they
// aren't counted (kept.go).
func (n *Local) leftFolders(assigned []state.Service) map[string][]string {
	here := map[string]bool{}
	for _, s := range assigned {
		here[s.Name()] = true
	}
	out := map[string][]string{}
	for _, root := range []string{n.Lab.Paths.ServicesDir, n.Lab.Paths.MediaDir} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !e.IsDir() || here[name] || !instance.ValidName(name) {
				continue
			}
			dirs, err := n.LeftDataDirs(context.Background(), name)
			if err != nil || len(dirs) == 0 {
				continue
			}
			out[name] = dirs
		}
	}
	return out
}

// LeftServices lists the removed services' data here, with their sizes
// from the last count (sizes are counted once a day, details.go).
func (n *Local) LeftServices(context.Context) ([]LeftService, error) {
	_, services := n.Assigned()
	folders := n.leftFolders(services)
	n.sizes.mu.Lock()
	defer n.sizes.mu.Unlock()
	var out []LeftService
	missing := false
	for name, dirs := range folders {
		ls := LeftService{Name: name, Dirs: dirs, Size: -1}
		if size, ok := n.sizes.left[name]; ok {
			ls.Size, ls.Counted = size, n.sizes.at
		} else {
			missing = true
		}
		out = append(out, ls)
	}
	if missing || time.Since(n.sizes.at) > sizeTTL {
		n.countSizesLocked()
	}
	slices.SortFunc(out, func(a, b LeftService) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// DeleteLeftData deletes a removed service's folders here, as the
// roostack user in Podman's user namespace (its files belong to the
// containers' users), and the Podman secrets Roostack kept for it so that
// installing it again over its data would find them. A service assigned
// here is refused: only data nothing uses is deleted.
func (n *Local) DeleteLeftData(ctx context.Context, name string) error {
	if !instance.ValidName(name) {
		return ErrNotFound
	}
	_, services := n.Assigned()
	if slices.ContainsFunc(services, func(s state.Service) bool { return s.Name() == name }) {
		return fmt.Errorf("%s is installed on this server", name)
	}
	// Taken off this server, but its containers not stopped yet: the
	// assignment changes before the apply stops them and removes their
	// files.
	if n.hasQuadlets(name) {
		return fmt.Errorf("%s is still stopping on this server", name)
	}
	folders := n.leftFolders(services)
	dirs, ok := folders[name]
	if !ok {
		return ErrNotFound
	}
	if n.Launch == nil {
		return errors.New("this server can't delete service data")
	}
	for _, d := range dirs {
		if out, err := n.unshare(ctx, "rm", "-rf", "--", d).CombinedOutput(); err != nil {
			return fmt.Errorf("deleting %s: %w: %s", d, err, out)
		}
	}
	// Its empty folders too (a folder found empty isn't listed at all).
	for _, root := range []string{n.Lab.Paths.ServicesDir, n.Lab.Paths.MediaDir} {
		os.Remove(filepath.Join(root, name))
	}
	if n.Secrets != nil {
		if err := n.removeLeftSecrets(ctx, name, services, folders); err != nil {
			return err
		}
	}
	n.sizes.mu.Lock()
	delete(n.sizes.left, name)
	n.sizes.mu.Unlock()
	return nil
}

// removeLeftSecrets removes the secrets named for a removed service
// (roostack-<name>-…), leaving those of another service whose name only
// starts with it (immich-2's, when deleting immich's).
func (n *Local) removeLeftSecrets(ctx context.Context, name string, assigned []state.Service, left map[string][]string) error {
	all, err := n.Secrets.List(ctx)
	if err != nil {
		return err
	}
	var longer []string
	for _, s := range assigned {
		if strings.HasPrefix(s.Name(), name+"-") {
			longer = append(longer, s.Name())
		}
	}
	for other := range left {
		if strings.HasPrefix(other, name+"-") {
			longer = append(longer, other)
		}
	}
	for secret := range all {
		if !strings.HasPrefix(secret, "roostack-"+name+"-") ||
			slices.ContainsFunc(longer, func(o string) bool { return strings.HasPrefix(secret, "roostack-"+o+"-") }) {
			continue
		}
		if err := n.Secrets.Remove(ctx, secret); err != nil {
			return fmt.Errorf("removing %s: %w", secret, err)
		}
	}
	return nil
}

// hasQuadlets reports whether any of the service's container files are
// still there (their io.roostack.service label names it: a file's name
// alone can't tell immich's from immich-2's).
func (n *Local) hasQuadlets(name string) bool {
	files, _ := filepath.Glob(filepath.Join(n.Lab.Paths.QuadletDir(), "roostack-"+name+"-*.container"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for line := range strings.Lines(string(data)) {
			if strings.TrimSpace(line) == "Label=io.roostack.service="+name {
				return true
			}
		}
	}
	return false
}
