package node

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/reconcile"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/words"
)

// Config file blocks (SPEC.md §8.1): Roostack's lines at the end of a file
// the app makes itself on its first start (Seafile's seahub_settings.py).
// Until the app has made it, an apply leaves the block waiting
// (reconcile.Plan.ConfigsWaiting); the server then looks for the file
// every few seconds, and once it's there applies again, which adds the
// block and restarts the app. Install hooks wait for that first, so they
// run against the app with Roostack's settings.

// configPoll is how often the server looks for files blocks wait for.
const configPoll = 5 * time.Second

// configWaitFor is how long it looks: an app that hasn't made its file by
// then has failed to start, and the next apply looks again.
const configWaitFor = 30 * time.Minute

// configWait is the look for files blocks wait for.
type configWait struct {
	mu      sync.Mutex
	waiting []generate.ConfigFile // the last apply's
	running bool
	// poll is how often it looks (zero: configPoll), and reapply what it
	// calls once a file is there (nil: Local.Reapply): for tests.
	poll    time.Duration
	reapply func(reason string)
}

// waitForConfigs looks for the files an apply's blocks wait for, until
// they're there or configWaitFor has passed, unless it's looking already.
func (n *Local) waitForConfigs(files []generate.ConfigFile) {
	w := &n.cfg
	w.mu.Lock()
	defer w.mu.Unlock()
	w.waiting = files
	if len(files) == 0 || w.running {
		return
	}
	w.running = true
	poll, reapply := w.poll, w.reapply
	if poll == 0 {
		poll = configPoll
	}
	if reapply == nil {
		reapply = n.Reapply
	}
	go func() {
		deadline := time.Now().Add(configWaitFor)
		seen := map[string]bool{}
		for {
			time.Sleep(poll)
			w.mu.Lock()
			files := w.waiting
			if len(files) == 0 || time.Now().After(deadline) {
				w.running = false
				w.mu.Unlock()
				return
			}
			// Seen twice in a row, so that the app has finished making it.
			var ready []string
			for _, f := range files {
				there := reconcile.ConfigThere(f)
				if there && seen[f.HostPath()] {
					ready = append(ready, f.Path)
				}
				seen[f.HostPath()] = there
			}
			if len(ready) == 0 {
				w.mu.Unlock()
				continue
			}
			w.running, w.waiting = false, nil
			w.mu.Unlock()
			reapply(fmt.Sprintf("the app has made %s, so Roostack is adding its own settings to it", words.And(ready)))
			return
		}
	}()
}

// waitForBlocks waits until a service's config file blocks are in its
// app's files, and no apply is running (one may be restarting the app
// with them), before its install hooks run.
func (n *Local) waitForBlocks(ctx context.Context, act *activity.Log, s state.Service, retry time.Duration) error {
	if len(s.Package.ConfigFiles) == 0 {
		return nil
	}
	out, err := generate.Service(n.currentLab(), s.Package, s.Instance)
	if err != nil {
		return err
	}
	for _, f := range out.ConfigFiles {
		if !f.Block {
			continue
		}
		act.Step("Waiting for %s to make %s, so that Roostack can add its own settings to it", s.Title(), f.Path)
		for !reconcile.HasBlock(f) || n.applying() {
			select {
			case <-ctx.Done():
				return fmt.Errorf("%s didn't make %s, which Roostack adds its own settings to", s.Title(), f.HostPath())
			case <-time.After(retry):
			}
		}
	}
	return nil
}

// applying says whether an apply is running or waiting to.
func (n *Local) applying() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.status.Running
}

// installOnlyInConfig says whether a package's config files hold an
// install-only secret, so that they're written again without it once
// setup has finished and the secret is deleted.
func installOnlyInConfig(p *pkgspec.Package) bool {
	for _, f := range p.ConfigFiles {
		for _, s := range p.Settings {
			if s.Type == pkgspec.SettingSecret && s.InstallOnly && strings.Contains(f.Content, ".Secrets."+s.Key) {
				return true
			}
		}
	}
	return false
}
