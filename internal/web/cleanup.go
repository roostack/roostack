package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/words"
	"roostack.cloud/roostack/units"
)

// Cleaning up a server (SPEC.md §6.7, "Cleaning up"; docs/mockups/
// lab-notes): what takes space there that no service needs, and deleting
// it. Old versions of services (images nothing uses) and Roostack's own
// leftovers are deleted here; data from removed services and kept copies
// have their own lists, which this links to. Owners only, like the rest
// of a server's pages.

func (s *Server) cleanUpRoutes(mux *http.ServeMux) {
	mux.Handle("GET /settings/servers/{server}/clean-up", s.requireOwner(s.cleanUpPage))
	mux.Handle("POST /settings/servers/{server}/clean-up/remove", s.requireOwner(s.cleanUpRemoveSubmit))
	mux.Handle("POST /settings/servers/{server}/clean-up/auto", s.requireOwner(s.cleanUpAutoSubmit))
}

// cleanUpWait is how long the page waits for the server's report.
const cleanUpWait = 30 * time.Second

// cleanUpImage is an image on the page: what it was, in words.
type cleanUpImage struct {
	node.ImageInfo
	// Title is the service it was used by, as people call it ("" if not
	// known); Installed: that service is still here.
	Title     string
	Installed bool
}

// What is what it was ("Immich 3.3.3"), or its repository.
func (i cleanUpImage) What() string {
	switch {
	case i.Title != "" && i.AppVersion() != "":
		return i.Title + " " + i.AppVersion()
	case i.Title != "":
		return i.Title
	case i.Repository() != "":
		return i.Repository()
	}
	return "An unnamed image"
}

// cleanUpGroup is a kind of image, with its total size and IDs.
type cleanUpGroup struct {
	Images []cleanUpImage
	Total  int64
}

// IDs are its images' IDs.
func (g cleanUpGroup) IDs() []string {
	var out []string
	for _, i := range g.Images {
		out = append(out, i.ID)
	}
	return out
}

func (s *Server) cleanUpPage(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil || n.CleanUp() == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	data := s.pageData(r)
	data["Ref"] = m
	ctx, cancel := context.WithTimeout(r.Context(), cleanUpWait)
	defer cancel()
	rep, err := n.CleanUp().CleanUpReport(ctx)
	if err != nil {
		s.Log.Warn("asking a server what could be cleaned up", "server", m.Name, "err", err)
		data["Error"] = "Roostack couldn't ask " + m.Name + " what's taking space just now. Try again in a minute."
	}
	titles := s.serviceTitles(r.Context())
	var services, own cleanUpGroup
	for _, img := range rep.Old {
		v := cleanUpImage{ImageInfo: img}
		v.Title, v.Installed = titles[img.Service], titles[img.Service] != ""
		if v.Title == "" && img.Service != "" {
			v.Title = img.Service
		}
		if img.Kind == node.ImageOwn {
			own.Images, own.Total = append(own.Images, v), own.Total+img.Size
		} else {
			services.Images, services.Total = append(services.Images, v), services.Total+img.Size
		}
	}
	var kept []cleanUpImage
	for _, img := range rep.Kept {
		kept = append(kept, cleanUpImage{ImageInfo: img, Title: titles[img.Service], Installed: titles[img.Service] != ""})
	}
	// (Not "Own" or "Services": the layout has its own.)
	data["OldVersions"], data["OwnImages"], data["Kept"], data["AutoClean"] = services, own, kept, rep.AutoClean
	// The other two kinds, which have their own lists.
	var removed cleanUpGroup
	removedCount := 0
	for _, row := range s.removedRows(r) {
		if row.ServerID == m.ID {
			removedCount++
			removed.Total += max(row.Size, 0)
		}
	}
	data["RemovedCount"], data["RemovedTotal"] = removedCount, removed.Total
	if copies, err := s.Manager.KeptCopies(r.Context(), m.ID); err == nil {
		var views []keptView
		for _, c := range copies {
			views = append(views, newKeptView(c, c.Service, nil, false))
		}
		data["KeptCopies"] = len(copies)
		data["KeptTotal"] = keptTotal(views)
	}
	if sd := s.serverSpaces(r.Context(), m, n); sd != nil {
		data["Disk"] = sd.Summary()
	}
	data["Freeable"] = services.Total + own.Total + removed.Total
	q := r.URL.Query()
	if done, err := strconv.Atoi(q.Get("removed")); err == nil {
		freed, _ := strconv.ParseInt(q.Get("freed"), 10, 64)
		text := "Deleted " + words.Count(int64(done), "image", "images") + ", freeing up to " + units.Bytes(freed) + "."
		if failed, _ := strconv.Atoi(q.Get("failed")); failed > 0 {
			text += " Podman wouldn't delete " + map[bool]string{true: "another: a container is using it now.", false: strconv.Itoa(failed) + " others: containers are using them now."}[failed == 1]
		}
		data["DoneText"] = text
	}
	data["AutoSaved"] = q.Get("auto") == "saved"
	s.render(w, "settings-cleanup", withArea(data, "servers"))
}

// cleanUpRemoveSubmit deletes the chosen images.
func (s *Server) cleanUpRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil || n.CleanUp() == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	r.ParseForm()
	ids := r.PostForm["id"]
	base := "/settings/servers/" + m.ID + "/clean-up"
	if len(ids) == 0 {
		redirect(w, r, base)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	res, err := n.CleanUp().RemoveImages(ctx, ids)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "clean up", "deleted "+strconv.Itoa(res.Removed)+" images on "+m.Name+", "+units.Bytes(res.Freed))
	target := base + "?removed=" + strconv.Itoa(res.Removed) + "&freed=" + strconv.FormatInt(res.Freed, 10)
	if len(res.Failed) > 0 {
		var why []string
		for _, w := range res.Failed {
			why = append(why, w)
		}
		target += "&failed=" + strconv.Itoa(len(res.Failed))
		s.Log.Warn("images that couldn't be deleted", "server", m.Name, "why", strings.Join(why, "; "))
	}
	redirect(w, r, target)
}

// cleanUpAutoSubmit turns the nightly clean-up on or off.
func (s *Server) cleanUpAutoSubmit(w http.ResponseWriter, r *http.Request) {
	n := s.serverOf(r)
	if n == nil || n.CleanUp() == nil {
		http.NotFound(w, r)
		return
	}
	m := s.serverRefOf(r)
	on := r.FormValue("on") == "on"
	if err := n.CleanUp().SetAutoClean(r.Context(), on); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "settings", "nightly clean-up on "+m.Name+": "+map[bool]string{true: "on", false: "off"}[on])
	redirect(w, r, "/settings/servers/"+m.ID+"/clean-up?auto=saved#auto")
}
