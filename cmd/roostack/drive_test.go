package main

import (
	"context"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/agent"
)

type fakeEraser struct {
	drives []agent.Drive
	erased string
}

func (f *fakeEraser) Drives(context.Context) ([]agent.Drive, error) { return f.drives, nil }
func (f *fakeEraser) EraseDrive(_ context.Context, disk, _ string) (agent.EraseDriveAnswer, error) {
	f.erased = disk
	return agent.EraseDriveAnswer{OK: true, Erased: true}, nil
}

// `roostack drive erase` says what's on the disk and erases it only once
// its name is typed, and never a disk Roostack wouldn't erase.
func TestEraseDriveCommand(t *testing.T) {
	f := &fakeEraser{drives: []agent.Drive{
		{Disk: "/dev/sdd", Name: "Old disk", Size: 500e9, Erasable: true, Holds: []string{"hfsplus"}, Confirm: agent.ConfirmRoot},
		{Disk: "/dev/sdb", Name: "BACKUP", Usable: true},
	}}
	var out strings.Builder
	if err := eraseDrive(context.Background(), f, "/dev/sdd", strings.NewReader("sdx\n"), &out); err == nil || f.erased != "" {
		t.Errorf("erased without its name: %v", err)
	}
	if !strings.Contains(out.String(), "It has a Mac filesystem (hfsplus) on it.") || !strings.Contains(out.String(), "Type sdd to erase it") {
		t.Errorf("said %q", out.String())
	}
	if err := eraseDrive(context.Background(), f, "/dev/sdd", strings.NewReader("sdd\n"), &out); err != nil || f.erased != "/dev/sdd" {
		t.Errorf("erase: %v %q", err, f.erased)
	}
	if err := eraseDrive(context.Background(), f, "/dev/sdb", strings.NewReader("sdb\n"), &out); err == nil {
		t.Error("erased a drive backups can use as it is")
	}
}
