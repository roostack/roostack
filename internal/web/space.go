package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/units"
)

// Disk space on each server (SPEC.md §6.7): what's used and free on the
// disk holding its Roostack data, what's promised to other labs storing
// backups there, and a warning when it runs low. Shown on Settings →
// Servers, on the backup plan, and as attention items on the home page.

// Low space: a warning below lowFreeWarn (or 10% of the disk), a problem
// below lowFreeProblem (or 5%). On a disk under 20 GB the floors shrink to
// a quarter and a tenth of it: a 2 GB drive with 1.9 GB free isn't low.
const (
	lowFreeWarn    = 5 * units.GB
	lowFreeProblem = 2 * units.GB
)

// spaceView is a server's disk space for a page.
type spaceView struct {
	node.DiskSpace
	// ServerID is whose disk it is; Title names it ("Media disk").
	ServerID, Title string
	// Promised is the space promised to other labs storing backups here
	// (their quotas); PromisedLeft the part of it they don't use yet.
	Promised, PromisedLeft int64
	// Level is "", levelWarning or levelProblem; Warning says why.
	Level, Warning string
}

// UsedPercent and PromisedPercent size the bar on Settings → Servers.
func (v spaceView) UsedPercent() int     { return percentOf(v.Used, v.Total) }
func (v spaceView) PromisedPercent() int { return percentOf(min(v.PromisedLeft, v.Free), v.Total) }
func (v spaceView) HeldBackPercent() int { return percentOf(v.HeldBack(), v.Total) }

// Parts is what uses the disk, for the bar's coloured segments and their
// key (SPEC.md §6.7); nil until the parts are counted (the bar then shows
// one "in use" segment).
func (v spaceView) Parts() []spacePart {
	if v.Other() < 0 {
		return nil
	}
	part := func(kind, label string, n int64) spacePart {
		return spacePart{Kind: kind, Label: label, Bytes: n, Percent: percentOf(n, v.Total)}
	}
	other := "Everything else"
	if v.Kind == node.DiskSystem {
		other = "The system and everything else"
	}
	return []spacePart{part("services", "Services' data", v.Services), part("backups", "Backups of your lab's servers", v.Backups),
		part("guests", "Backups of other labs", v.Guests), part("other", other, v.Other())}
}

// spacePart is one part of a disk's use: Kind names its colour
// (space-<kind>).
type spacePart struct {
	Kind, Label string
	Bytes       int64
	Percent     int
}

// Other is what the system and everything else takes: in use, less
// Roostack's counted parts (-1 until they're counted).
func (v spaceView) Other() int64 {
	if v.Services < 0 || v.Backups < 0 || v.Guests < 0 {
		return -1
	}
	return max(0, v.Used-v.Services-v.Backups-v.Guests)
}

// HeldBack is the space the filesystem keeps for the system (ext4 keeps
// 5% by default): neither used nor free to Roostack.
func (v spaceView) HeldBack() int64 { return max(0, v.Total-v.Used-v.Free) }

// NoticeKind is the notice for Warning.
func (v spaceView) NoticeKind() string {
	if v.Level == levelProblem {
		return "error"
	}
	return "warn"
}

func percentOf(n, total int64) int {
	if total <= 0 || n <= 0 {
		return 0
	}
	return int(n * 100 / total)
}

// lowSpace says how low free space is: "", levelWarning or levelProblem.
func lowSpace(free, total int64) string {
	switch {
	case free < 0 || total <= 0:
		return ""
	case free < min(lowFreeProblem, total/10) || free*20 < total:
		return levelProblem
	case free < min(lowFreeWarn, total/4) || free*10 < total:
		return levelWarning
	}
	return ""
}

// serverDisks is a server's disks, for its part of a page.
type serverDisks struct {
	ServerID string
	Disks    []*spaceView
	// Counting: the server is counting what uses its disks (the part
	// refreshes itself until it's done).
	Counting bool
	// StoreOnly: only the disk holding the backup store (Storage for
	// others).
	StoreOnly bool
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

// serverSpaces asks a server for its disks (nil if it can't say), with
// the space promised to other labs on the disk holding the backup store,
// and a warning for each disk running low.
func (s *Server) serverSpaces(ctx context.Context, m serverRef, n ServiceNode) *serverDisks {
	d := n.Disks()
	if d == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	disks, err := d.DiskSpaces(ctx)
	if err != nil || len(disks) == 0 {
		return nil
	}
	out := &serverDisks{ServerID: m.ID}
	var promised, promisedLeft int64
	// Other labs' quotas count on the server that stores their backups.
	if s.Manager.GuestServer(ctx) == m.ID {
		if guests, err := s.Manager.StorageGuests(ctx); err == nil {
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
		v := &spaceView{DiskSpace: ds, ServerID: m.ID, Title: diskTitle(ds)}
		out.Counting = out.Counting || ds.Counting
		// "This server" with one disk, "This server's media disk" with more.
		subject, disk := name, m.InSentence()+"'s disk"
		if len(disks) > 1 {
			disk = m.InSentence() + "'s " + lowerFirstWord(v.Title) // "USB drive BACKUP" stays
			subject = m.Label() + "'s " + lowerFirstWord(v.Title)
		}
		if ds.Store {
			v.Promised, v.PromisedLeft = promised, promisedLeft
		}
		switch v.Level = lowSpace(ds.Free, ds.Total); {
		case v.Level != "":
			v.Warning = fmt.Sprintf("%s is running out of disk space: %s free of %s. Services can stop working, and backups kept there fail, when it's full.",
				subject, units.Bytes(ds.Free), units.Bytes(ds.Total))
		case v.PromisedLeft > ds.Free:
			v.Level = levelWarning
			v.Warning = fmt.Sprintf("Other labs could fill %s: they've been promised %s more space for their backups, but only %s is free.",
				disk, units.Bytes(v.PromisedLeft), units.Bytes(ds.Free))
		case v.PromisedLeft > 0 && v.PromisedLeft > ds.Free-ds.Total*manager.GuestHeadroom/100:
			// Promised space now reaches into what's kept free for the lab's
			// own use: warn before other labs could fill the disk.
			v.Level = levelWarning
			v.Warning = fmt.Sprintf("Space promised to other labs now takes some of what's kept free on %s for your own services and backups: they can still add %s, and %s is free. Free up space, or give them less.",
				disk, units.Bytes(v.PromisedLeft), units.Bytes(ds.Free))
		}
		out.Disks = append(out.Disks, v)
	}
	return out
}

// store keeps only the disk holding the backup store.
func (d *serverDisks) store() *serverDisks {
	for _, v := range d.Disks {
		if v.Store {
			return &serverDisks{ServerID: d.ServerID, Disks: []*spaceView{v}, Counting: d.Counting, StoreOnly: true}
		}
	}
	return nil
}

// diskPart serves a server's disks while their parts are being counted
// (disks-live): 204 while unchanged, and without polling once done.
// ?store=1: only the disk holding the backup store.
func (s *Server) diskPart(w http.ResponseWriter, r *http.Request) {
	for _, mn := range s.serverNodes() {
		if mn.view.ID != r.PathValue("server") {
			continue
		}
		v := s.serverSpaces(r.Context(), mn.view, mn.node)
		if v != nil && r.URL.Query().Get("store") != "" {
			v = v.store()
		}
		if v != nil {
			s.renderLive(w, r, "settings-servers", "disks-live", v)
			return
		}
	}
	http.NotFound(w, r)
}

// spaceAttention flags servers running out of space (home page).
func (s *Server) spaceAttention(ctx context.Context, l *attentionList) {
	for _, mn := range s.serverNodes() {
		if d := s.serverSpaces(ctx, mn.view, mn.node); d != nil {
			for _, v := range d.Disks {
				if v.Level != "" {
					l.add(attentionItem{Key: "space:" + mn.view.ID + ":" + v.Mount + ":" + v.Level, Level: v.Level,
						Text: v.Warning,
						Link: "/servers#server-" + mn.view.ID, LinkText: "See its disks"})
				}
			}
		}
	}
}
