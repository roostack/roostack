package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/generate"
)

// Config files a package declares (SPEC.md §8.1): written inside the
// service's storage with its secrets filled in. The app's container runs
// as Roostack's user on the server, so it could put a symlink where the
// file goes to have Roostack overwrite something else, such as a Quadlet
// file. Every read and write therefore goes through an os.Root opened at
// the storage folder, which refuses any path leading out of it.

// configMode is a whole config file's permissions: Roostack's user is the
// container's root (or its user with UserNS=keep-id), so the app reads it,
// and nobody else on the server does.
const configMode = 0o600

// planConfig adds a config file to the plan if it must be written: when
// it's missing, or isn't what it should be with the secrets it holds now.
// A secret this plan makes can't be read yet, so its file is written
// anyway, filled in when the plan is applied.
func (r *Reconciler) planConfig(ctx context.Context, plan *Plan, f generate.ConfigFile, newSecrets map[string]bool, haveQuadlets map[string]string) error {
	have, err := readConfig(f)
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		// Such as a symlink out of its storage: the rest of the apply
		// goes ahead, and the log says why this file didn't change.
		plan.ConfigsLeft = append(plan.ConfigsLeft, fmt.Sprintf("%s: %v", f.HostPath(), err))
		return nil
	}
	if missing && f.Block {
		plan.ConfigsWaiting = append(plan.ConfigsWaiting, f)
		return nil
	}
	write := missing
	if !write {
		values, known, err := r.configValues(ctx, f, nil, newSecrets)
		if err != nil {
			return err
		}
		if !known {
			write = true
		} else {
			want := f.Fill(values)
			if f.Block {
				var ok bool
				if want, ok = f.SetBlock(have, want); !ok {
					plan.ConfigsLeft = append(plan.ConfigsLeft, f.HostPath()+": Roostack's lines in it have lost their last line, "+generate.BlockEnd)
					return nil
				}
			}
			write = want != have
		}
	}
	if !write {
		return nil
	}
	plan.Configs = append(plan.Configs, f)
	for _, q := range f.Restarts {
		if _, ok := haveQuadlets[q]; ok && !slices.Contains(plan.RestartUnits, unitFor(q)) {
			plan.RestartUnits = append(plan.RestartUnits, unitFor(q))
		}
	}
	return nil
}

// configValues are the values of a config file's secrets: one made in
// this apply (created), or the one Podman has. known is false if one is
// new in this plan, so can't be read until it's applied. An install-only
// secret Podman doesn't have was deleted once setup finished, and is
// empty.
func (r *Reconciler) configValues(ctx context.Context, f generate.ConfigFile, created map[string][]byte, newSecrets map[string]bool) (values map[string]string, known bool, err error) {
	values = map[string]string{}
	var have map[string]bool
	for _, s := range f.Secrets {
		if v, ok := created[s.Name]; ok {
			values[s.Name] = strings.TrimSpace(string(v))
			continue
		}
		if newSecrets[s.Name] {
			return nil, false, nil
		}
		if have == nil {
			if have, err = r.Secrets.List(ctx); err != nil {
				return nil, false, err
			}
		}
		if !have[s.Name] {
			if s.InstallOnly {
				values[s.Name] = ""
				continue
			}
			return nil, false, fmt.Errorf("its secret %s isn't there", s.Name)
		}
		v, err := r.Secrets.Read(ctx, s.Name)
		if err != nil {
			return nil, false, fmt.Errorf("reading %s: %w", s.Name, err)
		}
		values[s.Name] = strings.TrimSpace(string(v))
	}
	return values, true, nil
}

// writeConfig writes a config file with its secrets filled in: the whole
// file, or its block into the app's file, read again now in case the app
// changed it since the plan was made.
func (r *Reconciler) writeConfig(ctx context.Context, f generate.ConfigFile, created map[string][]byte) error {
	values, _, err := r.configValues(ctx, f, created, nil)
	if err != nil {
		return err
	}
	text, mode := f.Fill(values), fs.FileMode(configMode)
	if f.Block {
		have, err := readConfig(f)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // written when the app has made it
		}
		if err != nil {
			return err
		}
		var ok bool
		if text, ok = f.SetBlock(have, text); !ok {
			return nil
		}
		// The app's file keeps its own permissions.
		if info, err := statConfig(f); err == nil {
			mode = info.Mode().Perm()
		}
	}
	return writeInRoot(f.Dir, f.Path, []byte(text), mode)
}

func readConfig(f generate.ConfigFile) (string, error) {
	root, err := os.OpenRoot(f.Dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	b, err := root.ReadFile(f.Path)
	return string(b), err
}

func statConfig(f generate.ConfigFile) (fs.FileInfo, error) {
	root, err := os.OpenRoot(f.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Stat(f.Path)
}

// ConfigThere says whether a config file is there, for waiting until an
// app has made the file its block goes in.
func ConfigThere(f generate.ConfigFile) bool {
	_, err := statConfig(f)
	return err == nil
}

// HasBlock says whether a block config file has Roostack's block in it.
func HasBlock(f generate.ConfigFile) bool {
	have, err := readConfig(f)
	if err != nil {
		return false
	}
	_, ok := f.SetBlock(have, "")
	return ok && strings.Contains(have, generate.BlockStart)
}

// writeInRoot replaces rel inside dir atomically, through an os.Root so
// that no symlink can lead the write out of dir: a new file beside it,
// then a rename over it (which replaces a symlink rather than follow it).
func writeInRoot(dir, rel string, data []byte, mode fs.FileMode) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if d := filepath.Dir(rel); d != "." {
		if err := root.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	tmp := filepath.Join(filepath.Dir(rel), ".roostack-"+filepath.Base(rel)+".new")
	root.Remove(tmp)
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Chmod(mode) // whatever the umask
	}
	if err == nil {
		err = file.Sync()
	}
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, rel)
	}
	if err != nil {
		root.Remove(tmp)
	}
	return err
}
