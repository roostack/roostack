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
// below lowFreeProblem (or 5%).
const (
	lowFreeWarn    = 5 * units.GB
	lowFreeProblem = 2 * units.GB
)

// spaceView is a server's disk space for a page.
type spaceView struct {
	node.DiskSpace
	// Promised is the space promised to other labs storing backups here
	// (their quotas); PromisedLeft the part of it they don't use yet.
	Promised, PromisedLeft int64
	// Level is "", levelWarning or levelProblem; Warning says why.
	Level, Warning string
}

// UsedPercent and PromisedPercent size the bar on Settings → Servers.
func (v spaceView) UsedPercent() int     { return percentOf(v.Used, v.Total) }
func (v spaceView) PromisedPercent() int { return percentOf(min(v.PromisedLeft, v.Free), v.Total) }

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
	case free < lowFreeProblem || free*20 < total:
		return levelProblem
	case free < lowFreeWarn || free*10 < total:
		return levelWarning
	}
	return ""
}

// serverSpace asks a server for its disk space (nil if it can't say).
func (s *Server) serverSpace(ctx context.Context, m serverRef, n any) *spaceView {
	d, ok := n.(node.Disks)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ds, err := d.DiskSpace(ctx)
	if err != nil {
		return nil
	}
	v := &spaceView{DiskSpace: ds}
	// Other labs' quotas count on the server that stores their backups.
	if s.Manager != nil && s.Manager.GuestServer(ctx) == m.ID {
		if guests, err := s.Manager.StorageGuests(ctx); err == nil {
			for _, g := range guests {
				if !g.Removed.IsZero() {
					continue
				}
				v.Promised += g.Quota
				v.PromisedLeft += max(0, g.Quota-max(g.Used, 0))
			}
		}
	}
	name := m.Name
	if m.This {
		name = "This server"
	}
	switch v.Level = lowSpace(ds.Free, ds.Total); {
	case v.Level != "":
		v.Warning = fmt.Sprintf("%s is running out of disk space: %s free of %s. Services can stop working, and backups kept there fail, when it's full.",
			name, sizeText(ds.Free), sizeText(ds.Total))
	case v.PromisedLeft > ds.Free:
		v.Level = levelWarning
		v.Warning = fmt.Sprintf("Other labs could fill %s's disk: they've been promised %s more space for their backups, but only %s is free.",
			m.Label(), sizeText(v.PromisedLeft), sizeText(ds.Free))
	case v.PromisedLeft > 0 && v.PromisedLeft > ds.Free-ds.Total*manager.GuestHeadroom/100:
		// Promised space now reaches into what's kept free for the lab's
		// own use: warn before other labs could fill the disk.
		v.Level = levelWarning
		v.Warning = fmt.Sprintf("Space promised to other labs now takes some of what %s keeps free for your own services and backups: they can still add %s, and %s is free. Free up space, or give them less.",
			m.Label(), sizeText(v.PromisedLeft), sizeText(ds.Free))
	}
	return v
}

// spaceAttention flags servers running out of space (home page).
func (s *Server) spaceAttention(r *http.Request, add func(level, text, link, linkText string)) {
	for _, mn := range s.serverNodes(r) {
		if v := s.serverSpace(r.Context(), mn.view, mn.node); v != nil && v.Level != "" {
			add(v.Level, v.Warning, "/settings/servers#server-"+mn.view.ID, "See its disk")
		}
	}
}
