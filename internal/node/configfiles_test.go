package node

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/testwait"
)

// A block waits for its app's file (SPEC.md §8.1): the server applies
// again once the app has made it, and install hooks wait until the block
// is in it.
func TestConfigFileBlockWait(t *testing.T) {
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: files
title: Files
version: 1.0.0-roostack.1
containers:
  app:
    image: example.org/files:1@sha256:0000000000000000000000000000000000000000000000000000000000000000
    mounts: [{ storage: data, path: /shared }]
storage:
  data: { class: critical }
config_files:
  web: { title: Web settings, storage: data, path: conf/settings.py, format: python, block: true, content: "A = 1" }
hooks:
  - { on: install, container: app, command: [check] }
`))
	if err != nil {
		t.Fatal(err)
	}
	lab := config.Default()
	lab.Paths.ServicesDir = t.TempDir()
	svc := state.Service{Package: p, Instance: &instance.Instance{Package: "files", Version: p.Version, Updates: instance.UpdatesNotify}}
	ran := 0
	n := &Local{Lab: lab, State: memKV{}, Activity: activity.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		HookRetry: time.Millisecond, HookDeadline: 50 * time.Millisecond,
		Exec: func(context.Context, string, map[string]string, []string) ([]byte, []byte, error) {
			ran++
			return nil, nil, nil
		}}
	n.assignment.Services = []state.Service{svc}
	out, err := generate.Service(lab, p, svc.Instance)
	if err != nil {
		t.Fatal(err)
	}
	f := out.ConfigFiles[0]

	// Not made yet: setup waits for it, then says so.
	if err := n.runInstallHooks(context.Background(), svc); err == nil || !strings.Contains(err.Error(), "Files didn't make") || ran != 0 {
		t.Errorf("without the file: %v, hooks ran %d", err, ran)
	}

	// The app makes it: the server applies again.
	reasons := make(chan string, 1)
	n.cfg.poll, n.cfg.reapply = time.Millisecond, func(r string) { reasons <- r }
	n.waitForConfigs(out.ConfigFiles)
	os.MkdirAll(filepath.Dir(f.HostPath()), 0o755)
	os.WriteFile(f.HostPath(), []byte("KEY = 'k'\n"), 0o644)
	select {
	case r := <-reasons:
		if r != "the app has made conf/settings.py, so Roostack is adding its own settings to it" {
			t.Errorf("reason %q", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no apply once the file was there")
	}
	testwait.For(t, 5*time.Second, func() bool {
		n.cfg.mu.Lock()
		defer n.cfg.mu.Unlock()
		return !n.cfg.running
	})

	// That apply adds the block: then the hooks run.
	block, _ := f.SetBlock("KEY = 'k'\n", f.Fill(nil))
	os.WriteFile(f.HostPath(), []byte(block), 0o644)
	if err := n.runInstallHooks(context.Background(), svc); err != nil || ran != 1 {
		t.Errorf("with the block: %v, hooks ran %d", err, ran)
	}
}
