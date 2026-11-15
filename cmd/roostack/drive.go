package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/units"
)

const driveUsage = `Usage:
  roostack drive erase <disk>   erase a disk and prepare it for backups, such as /dev/sdb

Run as root. A disk with something on it that isn't a USB drive can only be
erased this way: Roostack's pages show this command for it (SPEC.md §6.1).
`

// driveCmd is `roostack drive`. It talks to roostack-agent as root, which
// is what lets it erase a disk with something on it: the agent erases one
// at once only for root, never for the roostack user alone.
func driveCmd(args []string) error {
	if len(args) != 2 || args[0] != "erase" {
		fmt.Print(driveUsage)
		return errors.New("drive: missing arguments")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("run it as root: sudo roostack drive erase %s", args[1])
	}
	return eraseDrive(context.Background(), agent.NewClient(agent.DefaultSocket), args[1], os.Stdin, os.Stdout)
}

// driveEraser is the agent's side of erasing a drive.
type driveEraser interface {
	Drives(ctx context.Context) ([]agent.Drive, error)
	EraseDrive(ctx context.Context, disk, label string) (agent.EraseDriveAnswer, error)
}

// eraseDrive says what's on the disk, asks for its name to be typed, and
// erases it.
func eraseDrive(ctx context.Context, c driveEraser, disk string, in io.Reader, out io.Writer) error {
	drives, err := c.Drives(ctx)
	if err != nil {
		return err
	}
	var d *agent.Drive
	for i := range drives {
		if drives[i].Disk == disk && drives[i].Erasable {
			d = &drives[i]
		}
	}
	if d == nil {
		return fmt.Errorf("%s can't be erased: it isn't a disk Roostack lists, or it's mounted, in /etc/fstab, "+
			"or has a filesystem backups can use as it is", disk)
	}
	what := d.Name
	if d.Model != "" && d.Model != d.Name {
		what += ", " + d.Model
	}
	fmt.Fprintf(out, "This will erase %s (%s, %s).", disk, what, units.Bytes(d.Size))
	if h := d.HoldsText(); h != "" {
		fmt.Fprintf(out, " It has %s on it.", h)
	}
	name := filepath.Base(disk)
	fmt.Fprintf(out, " Everything on it will be deleted for good, and it will be prepared for Roostack's backups.\n"+
		"Type %s to erase it: ", name)
	line, _ := bufio.NewReader(in).ReadString('\n')
	if strings.TrimSpace(line) != name {
		return errors.New("nothing was erased")
	}
	a, err := c.EraseDrive(ctx, disk, "ROOSTACK")
	if err != nil {
		return err
	}
	if !a.Erased {
		return errors.New("roostack-agent didn't erase it, as it does for root: is this running as root?")
	}
	fmt.Fprintf(out, "==> Erased %s and prepared it for backups: choose it on Roostack's Backups page.\n", disk)
	return nil
}
