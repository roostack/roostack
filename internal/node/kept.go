package node

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/kept"
	"roostack.cloud/roostack/internal/usererr"
)

// Kept copies (SPEC.md §8.4): the data this server set aside rather than
// deleting it, one list whatever set it aside (internal/kept). Whole
// folders beside the services' own are a move's, bringing data in's, a put
// back's or a restore's from another server; folders inside a service's
// are what a restore on this server set aside (backup.Restorer).

// KeptCopies lists the data set aside on this server, newest first, with
// how much space each copy takes.
func (n *Local) KeptCopies(ctx context.Context) ([]kept.Copy, error) {
	copies, err := n.keptCopies()
	if err != nil {
		return nil, err
	}
	for i, c := range copies {
		if size, err := n.du(ctx, true, c.Paths...); err == nil {
			copies[i].Size = size
		}
	}
	return copies, nil
}

// keptCopies lists the copies, without their sizes.
func (n *Local) keptCopies() ([]kept.Copy, error) {
	inside := map[string][]string{}
	for _, s := range n.services() {
		if out, err := generate.Service(n.currentLab(), s.Package, s.Instance); err == nil {
			inside[s.Name()] = out.Backup.Include
		}
	}
	return kept.Find([]string{n.Lab.Paths.ServicesDir, n.Lab.Paths.MediaDir}, inside)
}

// keptCopy is the copy a folder belongs to. Only the folders KeptCopies
// lists are accepted.
func (n *Local) keptCopy(path string) (kept.Copy, error) {
	copies, err := n.keptCopies()
	if err != nil {
		return kept.Copy{}, err
	}
	i := slices.IndexFunc(copies, func(c kept.Copy) bool { return c.Has(path) })
	if i < 0 {
		return kept.Copy{}, ErrNotFound
	}
	return copies[i], nil
}

// PutBackKept puts the copy a folder belongs to back in place of its
// service's data, which is kept in turn (kept.BeforePutBack), so changing
// one's mind again is a put back too. Its service must run here. A copy a
// restore here set aside goes back as a piece of maintenance, in the
// background, with its databases loaded from the dumps among it
// (Restores.PutBack); a whole folder goes back at once, its databases'
// own files with it.
func (n *Local) PutBackKept(ctx context.Context, path string) error {
	c, err := n.keptCopy(path)
	if err != nil {
		return err
	}
	switch {
	case c.Why == kept.MovedAway:
		return usererr.New("This is %s's old copy, from before it moved away from this server, so it can't be put back here.", c.Service)
	case !c.Why.CanGoBack():
		return usererr.New("This copy is what something that didn't finish left, so it can't be put back.")
	}
	s, ok := n.service(c.Service)
	if !ok {
		return usererr.New("%s doesn't run on this server, so its data can't be put back here.", c.Service)
	}
	if !c.Whole {
		x := n.Restores()
		if x == nil {
			return usererr.New("Backups aren't set up on this server, so Roostack can't load the databases in this copy. Set them up first, then put it back.")
		}
		return x.PutBack(ctx, c.Service, path)
	}
	f, _ := kept.Parse(c.Paths[0])
	suffix := strings.TrimPrefix(f.Path, f.Of)
	live, err := stateOutput(n, s)
	if err != nil {
		return err
	}
	if err := n.Reconciler.Systemd.Stop(ctx, live.Units...); err != nil {
		return err
	}
	now := kept.Suffix(kept.BeforePutBack, "", time.Now())
	for _, d := range n.DataDirs(c.Service) {
		if _, err := os.Stat(d + suffix); err != nil {
			continue
		}
		if _, err := os.Stat(d); err == nil {
			if err := os.Rename(d, d+now); err != nil {
				return err
			}
		}
		if err := os.Rename(d+suffix, d); err != nil {
			return err
		}
	}
	out, err := generate.Service(n.currentLab(), s.Package, s.Instance)
	if err != nil {
		return err
	}
	for _, d := range out.Dirs {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
	}
	return n.Reconciler.Systemd.Start(ctx, live.Units...)
}

// RemoveKept deletes the copy a folder belongs to, all its folders, for
// good. Files belong to container users, so it runs inside podman
// unshare.
func (n *Local) RemoveKept(ctx context.Context, path string) error {
	c, err := n.keptCopy(path)
	if err != nil {
		return err
	}
	for _, p := range c.Paths {
		if out, err := n.unshare(ctx, "rm", "-rf", "--", p).CombinedOutput(); err != nil {
			return fmt.Errorf("deleting %s: %w: %s", p, err, out)
		}
	}
	return nil
}
