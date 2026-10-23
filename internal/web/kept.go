package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"roostack.cloud/roostack/internal/kept"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
	"roostack.cloud/roostack/units"
)

// Kept copies (SPEC.md §8.4): the data a server set aside rather than
// deleting it, one list on a service's restore page (on every server) and
// on the Servers page (each server's, of every service), drawn by the
// "kept-copies" template part with the same labels and buttons (the
// Owner's mock-ups of 2026-10-23, docs/mockups/kept-copies).

// keptList is one "Kept copies" list.
type keptList struct {
	Copies []keptView
	// Action is where its forms post (…/put-back and …/remove).
	Action string
	// Total says how many copies there are and how much space they take.
	Total string
}

// keptView is one kept copy.
type keptView struct {
	manager.KeptCopy
	// Label says why it's kept; When is its time, and Path names it in a
	// form (its first folder).
	Label, When, Path string
	ServerName        string
	// ServiceTitle is its service's title, on the Servers page (the
	// restore page is the service's own). About says what it is, around its
	// server (on the restore page): AboutStart, the server, AboutEnd.
	ServiceTitle                   string
	AboutStart, OnServer, AboutEnd string
	// Note says why it can't be put back, or where its service runs.
	Note string
	// PutBack asks before putting it back; Restore is its service's name
	// when putting it back will run in the background, its progress on
	// the restore page.
	PutBack, Restore string
	Remove           string
}

// keptView describes a copy for people. title is its service's title
// (its name if it isn't installed), names the servers' names by ID, and
// ofService says the list is the service's own (on its restore page).
func newKeptView(c manager.KeptCopy, title string, names map[string]string, ofService bool) keptView {
	server, host := cmp.Or(names[c.Server], c.Server), cmp.Or(names[c.Host], c.Host)
	v := keptView{KeptCopy: c, Label: c.Why.Label(), When: words.Capitalise(words.When(c.Time)), Path: c.Paths[0],
		ServerName: server, OnServer: " on ",
		Remove: fmt.Sprintf("Delete this copy of %s's data for good? It can't be undone.", title)}
	// it is the service in a sentence: its title on its own page, "it"
	// after its title on the Servers page.
	it := title
	if !ofService {
		v.ServiceTitle, it = title, "it"
	}
	switch c.Why {
	case kept.BeforeRestore:
		v.AboutStart = "What " + it + " had before you restored it"
	case kept.FailedRestore:
		v.AboutStart, v.AboutEnd = "What a restore had copied", " before it stopped"
		v.Note = "It's only part of a backup, so it can't be put back."
	case kept.MovedAway:
		v.AboutStart, v.AboutEnd = title+"'s old copy", ", from before it moved"
		if to := strings.TrimPrefix(c.Note, "to-"); to != "" {
			v.AboutEnd += " to " + to
		}
		if !ofService {
			v.AboutStart = "Its old copy"
		}
	case kept.BeforeMove:
		v.AboutStart = "What was there before " + it + " moved in"
	case kept.FailedMove:
		v.AboutStart, v.OnServer, v.AboutEnd = "What was copied", " to ", " when "+it+" couldn't move there"
		if !ofService {
			v.AboutStart = "What was copied here"
		}
		if c.Host != "" && c.Host != c.Server {
			v.AboutEnd = " when " + it + " couldn't move from " + host
			v.Note = fmt.Sprintf("%s runs on %s, as before.", title, host)
		}
	case kept.BeforeImport:
		v.AboutStart = "What " + it + " had before you brought your old data in"
	case kept.FailedImport:
		v.AboutStart, v.AboutEnd = "What was brought in", ", set aside when it didn't work"
		v.Note = "Bringing it in didn't work, so it can't be put back. Bring the data in again instead."
	case kept.BeforePutBack:
		v.AboutStart = "What " + it + " had before you put back a kept copy"
	default:
		v.AboutStart = "Data of " + title
	}
	switch {
	case c.CanPutBack:
		v.PutBack = fmt.Sprintf("Put back %s's data from this copy, as it was at %s? %s will stop for a moment and start again on that data. "+
			"What it has now will be kept here instead, so you can change your mind again.", title, c.Time.Local().Format("Mon 2 Jan 15:04"), title)
		if c.Why == kept.BeforeRestore {
			v.PutBack = fmt.Sprintf("Put back %s's data from before the restore, as it was at %s? %s will stop for a moment and start again on that data. "+
				"What it has now will be kept here instead, so you can change your mind again.", title, c.Time.Local().Format("Mon 2 Jan 15:04"), title)
		}
		if !c.Whole {
			v.Restore = c.Service
		}
	case v.Note != "":
	case c.Host == "":
		v.Note = fmt.Sprintf("%s isn't installed any more, so this copy can't be put back.", title)
	case c.Why == kept.MovedAway && c.Host == c.Server:
		v.Note = "It's from before " + title + " moved away and back, so it can't be put back. Restore a backup instead."
	case c.Why == kept.MovedAway:
		v.Note = fmt.Sprintf("%s runs on %s now, so this copy can't be put back on %s. Restore a backup instead.", title, host, server)
	default:
		v.Note = fmt.Sprintf("%s runs on %s now, so this copy can't be put back on %s.", title, host, server)
	}
	return v
}

// keptTotal says how many copies there are and how much space they take.
func keptTotal(copies []keptView) string {
	var size int64
	for _, c := range copies {
		size += max(c.Size, 0)
	}
	n := words.Count(int64(len(copies)), "copy", "copies")
	if size == 0 {
		return n + "."
	}
	return n + ", " + units.Bytes(size) + " in all."
}

// serviceTitles are the installed services' titles, by name.
func (s *Server) serviceTitles(ctx context.Context) map[string]string {
	titles := map[string]string{}
	if services, err := s.Services(ctx); err == nil {
		for _, svc := range services {
			titles[svc.Name] = svc.Title
		}
	}
	return titles
}

// serverNames are the lab's servers' names, by ID.
func serverNames(servers []manager.Server) map[string]string {
	names := map[string]string{}
	for _, mc := range servers {
		names[mc.ID] = mc.Name
	}
	return names
}

// putBackKept puts a kept copy back from a form (server, path) and
// records it; restore is its service's name when putting it back runs in
// the background.
func (s *Server) putBackKept(r *http.Request, server string) (restore string, err error) {
	path := r.FormValue("path")
	if err := s.Manager.PutBackKept(r.Context(), server, path); err != nil {
		return "", err
	}
	s.Store.Audit(r.Context(), currentUser(r), "restore", "put back kept copy "+path+" on "+server)
	return r.FormValue("restore"), nil
}

// removeKept deletes a kept copy from a form, and records it.
func (s *Server) removeKept(r *http.Request, server string) error {
	path := r.FormValue("path")
	if err := s.Manager.RemoveKept(r.Context(), server, path); err != nil {
		return err
	}
	s.Store.Audit(r.Context(), currentUser(r), "restore", "deleted kept copy "+path+" on "+server)
	return nil
}

// putBackKeptServerSubmit puts a kept copy back from the Servers page. A
// copy a restore kept goes back in the background: its progress is on
// its service's restore page.
func (s *Server) putBackKeptServerSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("server")
	restore, err := s.putBackKept(r, id)
	if msg, ok := usererr.Message(err); ok {
		w.WriteHeader(http.StatusBadRequest)
		s.renderServers(w, r, map[string]any{"Error": msg, "ErrorServer": id})
		return
	}
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if _, ok, _ := s.serviceByName(r.Context(), restore); ok {
		redirect(w, r, "/backups/restore/"+restore)
		return
	}
	redirect(w, r, "/servers#server-"+id)
}

// removeKeptServerSubmit deletes a kept copy from the Servers page.
func (s *Server) removeKeptServerSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("server")
	err := s.removeKept(r, id)
	if errors.Is(err, manager.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	redirect(w, r, "/servers#server-"+id)
}
