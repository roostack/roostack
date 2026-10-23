package attention

import (
	"context"
	"fmt"
	"time"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/words"
	"roostack.cloud/roostack/units"
)

// Disk space on each server (SPEC.md §6.7): what's used and free on each
// disk, what's promised to other labs storing backups there, and a
// warning when it runs low.

// Low space (SPEC.md §6.7): a warning below lowFreeWarn (or 10% of the disk), a problem
// below lowFreeProblem (or 5%). On a disk under 20 GB the floors shrink to
// a quarter and a tenth of it: a 2 GB drive with 1.9 GB free isn't low.
const (
	lowFreeWarn    = 5 * units.GB
	lowFreeProblem = 2 * units.GB
)

// Disk is one of a server's disks, with how low its space is.
type Disk struct {
	node.DiskSpace
	// ServerID is whose disk it is; Title names it ("Media disk").
	ServerID, Title string
	// Promised is the space promised to other labs storing backups here
	// (their quotas); PromisedLeft the part of it they don't use yet.
	Promised, PromisedLeft int64
	// Level is "", Warning or Problem; Warning says why.
	Level, Warning string
}

// LowSpace says how low free space is: "", Warning or Problem.
func LowSpace(free, total int64) string {
	switch {
	case free < 0 || total <= 0:
		return ""
	case free < min(lowFreeProblem, total/10) || free*20 < total:
		return Problem
	case free < min(lowFreeWarn, total/4) || free*10 < total:
		return Warning
	}
	return ""
}

// diskTitle names a disk on the page.
func diskTitle(d node.DiskSpace) string {
	switch d.Kind {
	case node.DiskSystem:
		return "System disk"
	case node.DiskData:
		return "Data disk"
	case node.DiskMedia:
		return "Media disk"
	}
	if d.Name != "" {
		// As the backup plan names it ("USB drive BACKUP").
		return d.Name
	}
	return "Backup drive"
}

// ServerDisks asks server m for its disks (d, its Disks; nil if it can't
// say), with the space promised to other labs on the disk holding the
// backup store, and a warning for each disk running low.
func ServerDisks(ctx context.Context, mgr Manager, m Server, d node.Disks) []Disk {
	if d == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	disks, err := d.DiskSpaces(ctx)
	if err != nil || len(disks) == 0 {
		return nil
	}
	var out []Disk
	var promised, promisedLeft int64
	// Other labs' quotas count on the server that stores their backups.
	if mgr.GuestServer(ctx) == m.ID {
		if guests, err := mgr.StorageGuests(ctx); err == nil {
			for _, g := range guests {
				if g.Removed.IsZero() {
					promised += g.Quota
					promisedLeft += max(0, g.Quota-max(g.Used, 0))
				}
			}
		}
	}
	name := m.Name
	if m.This {
		name = "This server"
	}
	for _, ds := range disks {
		v := Disk{DiskSpace: ds, ServerID: m.ID, Title: diskTitle(ds)}
		// "This server" with one disk, "This server's media disk" with more.
		subject, disk := name, m.InSentence()+"'s disk"
		if len(disks) > 1 {
			disk = m.InSentence() + "'s " + words.LowerFirstWord(v.Title) // "USB drive BACKUP" stays
			subject = m.Label() + "'s " + words.LowerFirstWord(v.Title)
		}
		if ds.Store {
			v.Promised, v.PromisedLeft = promised, promisedLeft
		}
		switch v.Level = LowSpace(ds.Free, ds.Total); {
		case v.Level != "":
			v.Warning = fmt.Sprintf("%s is running out of disk space: %s free of %s. Services can stop working, and backups kept there fail, when it's full.",
				subject, units.Bytes(ds.Free), units.Bytes(ds.Total))
		case v.PromisedLeft > ds.Free:
			v.Level = Warning
			v.Warning = fmt.Sprintf("Other labs could fill %s: they've been promised %s more space for their backups, but only %s is free.",
				disk, units.Bytes(v.PromisedLeft), units.Bytes(ds.Free))
		case v.PromisedLeft > 0 && v.PromisedLeft > ds.Free-ds.Total*manager.GuestHeadroom/100:
			// Promised space now reaches into what's kept free for the lab's
			// own use: warn before other labs could fill the disk.
			v.Level = Warning
			v.Warning = fmt.Sprintf("Space promised to other labs now takes some of what's kept free on %s for your own services and backups: they can still add %s, and %s is free. Free up space, or give them less.",
				disk, units.Bytes(v.PromisedLeft), units.Bytes(ds.Free))
		}
		out = append(out, v)
	}
	return out
}

// space flags servers running out of space.
func (lab *Lab) space(ctx context.Context, l *List) {
	for _, mn := range lab.servers() {
		if d := ServerDisks(ctx, lab.Manager, mn.view, mn.node.Disks()); d != nil {
			for _, v := range d {
				if v.Level != "" {
					l.add(Item{Key: "space:" + mn.view.ID + ":" + v.Mount + ":" + v.Level, Level: v.Level,
						Text: v.Warning,
						Link: "/servers#server-" + mn.view.ID, LinkText: "See its disks"})
				}
			}
		}
	}
}
