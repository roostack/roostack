package activity

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

func TestLog(t *testing.T) {
	r := New()
	l := r.Start(OSUpdates, "Checking for updates")
	l.Step("Downloading package lists")
	fmt.Fprint(l, "Get:1 http://deb.debian.org trixie InRelease\nProgress 10%\rProgress 100%\nSetting up cur")
	fmt.Fprint(l, "l (8.14.1)\n")
	snap, ok := r.Latest(OSUpdates)
	if !ok || !snap.Running() || snap.Status != "Downloading package lists" {
		t.Fatalf("snap = %+v", snap)
	}
	want := []string{"==> Downloading package lists", "Get:1 http://deb.debian.org trixie InRelease",
		"Progress 100%", "Setting up curl (8.14.1)"}
	if !slices.Equal(snap.Lines, want) {
		t.Errorf("lines = %q", snap.Lines)
	}
	l.Finish(errors.New("exit status 100"))
	snap, _ = r.Latest(OSUpdates)
	if snap.Running() || snap.Err != "exit status 100" || snap.Lines[len(snap.Lines)-1] != "==> Failed: exit status 100" {
		t.Errorf("finished snap = %+v", snap)
	}

	// A new run replaces the old one.
	r.Start(OSUpdates, "Checking for updates")
	if snap, _ := r.Latest(OSUpdates); len(snap.Lines) != 0 {
		t.Error("new run should start empty")
	}
	if _, ok := r.Latest(Backup); ok {
		t.Error("no backup log yet")
	}
}

func TestLimitAndNil(t *testing.T) {
	l := New().Start(Backup, "Backing up")
	for i := range maxLines + 5 {
		l.Printf("line %d", i)
	}
	if s := l.Snapshot(); len(s.Lines) != maxLines || s.Dropped != 5 || s.Lines[0] != "line 5" {
		t.Errorf("kept %d, dropped %d, first %q", len(s.Lines), s.Dropped, s.Lines[0])
	}
	var nilLog *Log // optional logging
	nilLog.Step("x")
	fmt.Fprintln(nilLog, "y")
	nilLog.Finish(nil)
	var nilReg *Registry
	if nilReg.Start(Apply, "x") != nil {
		t.Error("nil registry should give a nil log")
	}
}
