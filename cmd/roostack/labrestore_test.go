package main

import (
	"os"
	"path/filepath"
	"testing"

	"roostack.cloud/roostack/internal/backup"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/state"
)

// A full-lab restore that failed after putting the server's own files
// back can be started again (review M25): its restored instance files
// don't count while the restore is unfinished.
func TestUnfinishedRestoreIsFresh(t *testing.T) {
	dir := t.TempDir()
	lab := config.Lab{Paths: config.Paths{StateDir: dir}}
	l := &labRestorer{lab: lab}
	if err := l.fresh(); err != nil || l.Unfinished() {
		t.Fatalf("a new install: %v, unfinished %v", err, l.Unfinished())
	}
	if err := os.MkdirAll(lab.Paths.InstancesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	in := &instance.Instance{Package: "forgejo", Version: "15.0.9-roostack.1"}
	if err := in.Save(state.InstanceFile(lab, "forgejo")); err != nil {
		t.Fatal(err)
	}
	if err := l.fresh(); err == nil {
		t.Error("a server with services isn't fresh")
	}
	if err := os.WriteFile(backup.RestoreStartedFile(lab.Paths), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := l.fresh(); err != nil || !l.Unfinished() {
		t.Errorf("an unfinished restore: %v, unfinished %v", err, l.Unfinished())
	}
	if filepath.Dir(backup.RestoreStartedFile(lab.Paths)) != dir {
		t.Error("the marker is in the state folder")
	}
}
