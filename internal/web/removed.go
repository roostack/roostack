package web

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/store"
)

// Data left by removed services (SPEC.md §8.1, "Data left by removed
// services"; docs/mockups/lab-notes): removing a service keeps its data
// so that installing it again carries on, and Services → "Removed, data
// kept" lists what's kept on each server, with Install again and Delete
// (a modal dialog, the maintainer's choice). The store names each one
// (what package, removed when and by whom); the servers list the folders
// (node.LeftServices), so a folder from before the store kept a record
// is listed too, by its name.

// removedRoutes is deleting one's data (Owners only, like kept copies).
func (s *Server) removedRoutes(mux *http.ServeMux) {
	mux.Handle("POST /services/removed/{server}/{name}/delete", s.requireOwner(s.deleteRemovedSubmit))
}

// removedRow is one removed service's data on one server.
type removedRow struct {
	ServerID, Server string
	node.LeftService
	// Known: the store noted its removal, with what follows.
	Known                   bool
	Package, Title, Version string
	Logo                    string
	At                      time.Time
	By                      string
	// CanInstall: its package is in the catalog, to install again.
	CanInstall bool
	// Backups have its data, the oldest made at Oldest (only for the
	// Delete dialog that's open).
	Backups int
	Oldest  time.Time
}

// Label is what to call it: its title, or its folder's name.
func (r removedRow) Label() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

// DialogID is its Delete dialog's ID.
func (r removedRow) DialogID() string { return "delete-" + r.ServerID + "-" + r.Name }

// AppVersion is its package's version without Roostack's revision.
func (r removedRow) AppVersion() string {
	v, _, _ := strings.Cut(r.Version, "-roostack.")
	return v
}

// leftWait is how long the list waits for each server.
const leftWait = 5 * time.Second

// removedRows lists every server's removed services' data, newest first,
// then those without a record by name.
func (s *Server) removedRows(r *http.Request) []removedRow {
	ctx, cancel := context.WithTimeout(r.Context(), leftWait)
	defer cancel()
	records, _ := s.Store.RemovedServices(ctx)
	known := map[string]store.RemovedService{}
	for _, rec := range records {
		known[rec.Server+"/"+rec.Name] = rec
	}
	catalog := map[string]bool{}
	if items, err := s.Manager.Catalog(); err == nil {
		for _, it := range items {
			catalog[it.Name] = true
		}
	}
	nodes := s.serverNodes()
	found := make([][]node.LeftService, len(nodes))
	var wg sync.WaitGroup
	for i, mn := range nodes {
		if ld := mn.node.LeftData(); ld != nil {
			wg.Go(func() { found[i], _ = ld.LeftServices(ctx) })
		}
	}
	wg.Wait()
	var out []removedRow
	for i, mn := range nodes {
		for _, ls := range found[i] {
			row := removedRow{ServerID: mn.view.ID, Server: mn.view.Name, LeftService: ls}
			if rec, ok := known[mn.view.ID+"/"+ls.Name]; ok {
				row.Known, row.Package, row.Title, row.Version, row.At, row.By = true, rec.Package, rec.Title, rec.Version, rec.At, rec.By
				row.CanInstall = catalog[rec.Package]
				if row.CanInstall {
					row.Logo = "/install/" + rec.Package + "/icon.png"
				}
			}
			out = append(out, row)
		}
	}
	slices.SortStableFunc(out, func(a, b removedRow) int {
		if a.Known != b.Known {
			return map[bool]int{true: -1, false: 1}[a.Known]
		}
		return b.At.Compare(a.At)
	})
	return out
}

// addRemoved adds the removed services' data to Services, for those who
// manage services: how many (for its "Removed, data kept" view), and
// with ?removed=1 the list. ?delete=<dialog> opens one's Delete dialog,
// with when its backups were made (asked of restic, so only for that
// one: the dialog loads it as it opens).
func (s *Server) addRemoved(r *http.Request, data map[string]any) {
	if data["CanManage"] != true {
		return
	}
	rows := s.removedRows(r)
	data["RemovedCount"] = len(rows)
	q := r.URL.Query()
	if q.Get("removed") == "" {
		return
	}
	data["ShowRemoved"], data["Removed"], data["Deleted"] = true, rows, q.Get("deleted")
	open := q.Get("delete")
	for i := range rows {
		if rows[i].DialogID() == open {
			data["OpenDelete"] = open
			rows[i].Backups, rows[i].Oldest = s.removedBackups(r.Context(), rows[i])
		}
	}
}

// removedBackups is how many backups have a removed service's data, and
// the oldest's time (0 and zero if none, or the server can't say).
func (s *Server) removedBackups(ctx context.Context, row removedRow) (int, time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, mn := range s.serverNodes() {
		if mn.view.ID != row.ServerID || mn.node.Restores() == nil {
			continue
		}
		snaps, err := mn.node.Restores().Snapshots(ctx, row.Name)
		if err != nil || len(snaps) == 0 {
			return 0, time.Time{}
		}
		oldest := snaps[0].Time
		for _, sn := range snaps {
			if sn.Time.Before(oldest) {
				oldest = sn.Time
			}
		}
		return len(snaps), oldest
	}
	return 0, time.Time{}
}

// removedRecord is what a service being removed was: its package, title,
// version and server. Nil if that can't be worked out (it's listed by
// name then).
func (s *Server) removedRecord(r *http.Request, name string, p *pkgspec.Package) *store.RemovedService {
	n := s.Manager.NodeFor(name)
	if n == nil {
		return nil
	}
	rec := &store.RemovedService{Server: n.Identity().ID, Name: name, Package: p.Name, Title: p.Title, Version: p.Version,
		At: time.Now(), By: currentUser(r).Username}
	if v, ok, err := s.serviceByName(r.Context(), name); err == nil && ok {
		rec.Title = v.Title
	}
	return rec
}

// deleteRemovedSubmit deletes a removed service's data for good.
func (s *Server) deleteRemovedSubmit(w http.ResponseWriter, r *http.Request) {
	serverID, name := r.PathValue("server"), r.PathValue("name")
	if r.FormValue("sure") != "on" {
		redirect(w, r, "/services?removed=1&delete="+serverID+"-"+name+"#removed")
		return
	}
	err := s.deleteLeftData(r.Context(), serverID, name)
	if errors.Is(err, node.ErrNotFound) {
		redirect(w, r, "/services?removed=1#removed")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "remove", "deleted "+name+"'s data on "+s.serverName(serverID))
	redirect(w, r, "/services?removed=1&deleted="+name+"#removed")
}

// deleteLeftData asks the server to delete a removed service's data, and
// forgets its removal.
func (s *Server) deleteLeftData(ctx context.Context, serverID, name string) error {
	var ld node.LeftData
	for _, mn := range s.serverNodes() {
		if mn.view.ID == serverID {
			ld = mn.node.LeftData()
		}
	}
	if ld == nil {
		return node.ErrNotFound
	}
	if err := ld.DeleteLeftData(ctx, name); err != nil {
		return err
	}
	return s.Store.ForgetRemoved(ctx, serverID, name)
}

// leftDeleteWait is how long deleting a service's data on removal waits
// for the change that stops it to reach its server, trying again every
// leftDeleteEvery.
const (
	leftDeleteWait  = 10 * time.Minute
	leftDeleteEvery = 5 * time.Second
)

// deleteLeftDataLater deletes a service's data once the change removing
// it has stopped it on its server (the Remove page's tick): the server
// refuses while the service is still its own.
func (s *Server) deleteLeftDataLater(serverID, name string, u *store.User) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), leftDeleteWait)
		defer cancel()
		t := time.NewTicker(leftDeleteEvery)
		defer t.Stop()
		var err error
		for {
			err = s.deleteLeftData(ctx, serverID, name)
			if err == nil {
				s.Store.Audit(context.Background(), u, "remove", "deleted "+name+"'s data on "+s.serverName(serverID))
				return
			}
			// Gone from its server, with nothing in its folders.
			if errors.Is(err, node.ErrNotFound) {
				s.Store.ForgetRemoved(context.Background(), serverID, name)
				return
			}
			select {
			case <-ctx.Done():
				s.Log.Warn("deleting a removed service's data", "service", name, "err", err)
				return
			case <-t.C:
			}
		}
	}()
}

// serverName is a server's name, or its ID if it isn't known.
func (s *Server) serverName(id string) string {
	for _, mn := range s.serverNodes() {
		if mn.view.ID == id {
			return mn.view.Name
		}
	}
	return id
}
