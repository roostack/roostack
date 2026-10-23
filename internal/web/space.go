package web

import (
	"context"
	"net/http"

	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/node"
)

// Disk space on each server (SPEC.md §6.7): what's used and free on the
// disk holding its Roostack data, what's promised to other labs storing
// backups there, and a warning when it runs low. Shown on Settings →
// Servers, on the backup plan, and as attention items on the home page.

// spaceView is a server's disk space for a page.
type spaceView struct{ attention.Disk }

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
	if v.Level == attention.Problem {
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

// serverSpaces asks a server for its disks (nil if it can't say), each
// with how low its space is (attention.ServerDisks).
func (s *Server) serverSpaces(ctx context.Context, m serverRef, n ServiceNode) *serverDisks {
	disks := attention.ServerDisks(ctx, s.Guests, attention.Server{ID: m.ID, Name: m.Name, This: m.This}, n.Disks())
	if disks == nil {
		return nil
	}
	out := &serverDisks{ServerID: m.ID}
	for _, d := range disks {
		out.Counting = out.Counting || d.Counting
		out.Disks = append(out.Disks, &spaceView{Disk: d})
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
