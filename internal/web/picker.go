package web

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"

	"roostack.cloud/roostack/internal/agent"
)

// mediaCrumb is one step of the folders opened so far ("" Href: the one
// open now).
type mediaCrumb struct{ Name, Href string }

// folderPicker is the folder picker (components.html "folder-picker"):
// the folders in a place, and choosing it.
type folderPicker struct {
	Title   string
	Crumbs  []mediaCrumb
	Folders []pickerFolder
	// Label and Counts say what the place is and holds; UseHref chooses
	// it ("" where nothing can be chosen: the list of places on a server).
	Label, Counts     string
	UseHref, UseText  string
	CancelHref, Error string
}

// pickerFolder is a folder in the picker, opening it.
type pickerFolder struct {
	Name, Href     string
	Folders, Files int
	NoCounts       bool
}

// crumbsFor are the breadcrumbs of a folder rel ("a/b", "" for the top)
// below top: top, then each folder down to rel, which isn't a link (it's
// open now). href is a folder's address, given its path from the top.
func crumbsFor(top, rel string, href func(rel string) string) []mediaCrumb {
	crumbs := []mediaCrumb{{top, href("")}}
	if rel != "" {
		parts := strings.Split(rel, "/")
		for i, part := range parts {
			crumbs = append(crumbs, mediaCrumb{part, href(strings.Join(parts[:i+1], "/"))})
		}
	}
	crumbs[len(crumbs)-1].Href = ""
	return crumbs
}

// pickerPlace is where a folder picker is open: a folder on a drive
// (Drive, named DriveName, at Sub), or one of the server's (Folder; ""
// for the places folders can be).
type pickerPlace struct {
	Drive, DriveName, Sub string
	Folder                string
}

// pickerPlaceOf is where a picker's query says.
func pickerPlaceOf(q url.Values) pickerPlace {
	if drive := q.Get("drive"); drive != "" {
		return pickerPlace{Drive: drive, DriveName: q.Get("name"), Sub: cleanSub(q.Get("sub"))}
	}
	return pickerPlace{Folder: q.Get("folder")}
}

// pickerLinks are what a folder picker's page decides: where the picker
// is at a place (at, given its query), the form it goes back to (back:
// with the place chosen when use, else only how), and what the server's
// folders are called in its title and first crumb.
type pickerLinks struct {
	at          func(url.Values) string
	back        func(pl pickerPlace, use bool) string
	serverTitle string
	serverTop   string
}

// pickFolders fills the folder-picker part for pl: its title, crumbs and
// links, and the folders there, which server lists. A problem people can
// fix shows in the picker (with nothing to use); any other is returned.
func (s *Server) pickFolders(ctx context.Context, server string, pl pickerPlace, links pickerLinks) (folderPicker, error) {
	var p folderPicker
	var l agent.Library
	var folderHref func(string) string
	at := links.at
	p.CancelHref = links.back(pl, false)
	if pl.Drive != "" {
		drive, dn, sub := pl.Drive, pl.DriveName, pl.Sub
		l = agent.Library{Drive: drive, Sub: sub}
		p.Title, p.Label, p.UseText = "Choose a folder on "+dn, dn, "Use "+dn
		p.Crumbs = crumbsFor(dn, sub, func(rel string) string {
			q := url.Values{"drive": {drive}, "name": {dn}}
			if rel != "" {
				q.Set("sub", rel)
			}
			return at(q)
		})
		if sub != "" {
			parts := strings.Split(sub, "/")
			p.Label, p.UseText = dn+" › "+strings.Join(parts, " › "), "Use "+parts[len(parts)-1]
		}
		folderHref = func(f string) string {
			return at(url.Values{"drive": {drive}, "name": {dn}, "sub": {strings.TrimPrefix(sub+"/"+f, "/")}})
		}
	} else {
		p.Title = links.serverTitle
		folderAt := func(rel string) string {
			if rel == "" {
				return at(url.Values{})
			}
			return at(url.Values{"folder": {"/" + rel}})
		}
		if pl.Folder == "" {
			// The places folders can be, to start with.
			for _, root := range agent.LibraryFolderRoots {
				p.Folders = append(p.Folders, pickerFolder{Name: root, Href: at(url.Values{"folder": {root}}), NoCounts: true})
			}
			p.Crumbs = crumbsFor(links.serverTop, "", folderAt)
			return p, nil
		}
		folder := path.Clean("/" + pl.Folder)
		l = agent.Library{Folder: folder}
		p.Crumbs = crumbsFor(links.serverTop, strings.Trim(folder, "/"), folderAt)
		p.Label, p.UseText = folder, "Use "+path.Base(folder)
		pl.Folder = folder
		folderHref = func(f string) string { return at(url.Values{"folder": {path.Join(folder, f)}}) }
	}
	p.UseHref = links.back(pl, true)
	list, err := s.Libraries.LibraryFolders(ctx, server, l)
	if msg, ok := userMessage(err); ok {
		p.Error, p.UseHref = msg, ""
	} else if err != nil {
		return p, err
	}
	for _, f := range list.Folders {
		p.Folders = append(p.Folders, pickerFolder{Name: f.Name, Href: folderHref(f.Name), Folders: f.Folders, Files: f.Files})
	}
	p.Counts = folderCounts(len(list.Folders), list.Files)
	return p, nil
}

// stepBack is the back link of a step's picker, as photos' and imports'
// pages make it: to base's step (anchor), with the place chosen.
func stepBack(base, anchor string) func(pickerPlace, bool) string {
	return func(pl pickerPlace, use bool) string {
		q := url.Values{"where": {"folder"}}
		if pl.Drive != "" {
			q.Set("where", "drive")
		}
		if use && pl.Drive != "" {
			q.Set("drive", pl.Drive)
			q.Set("sub", pl.Sub)
		} else if use {
			q.Set("folder", pl.Folder)
		}
		return base + "?" + q.Encode() + anchor
	}
}

// shareForm is a network share's fields on a form (share-fields), kept
// when it comes back: never its password.
type shareForm struct {
	ShareType, ShareServer, SharePath, ShareUser string
}

// fillShare is a saved share's fields.
func (f *shareForm) fillShare(sh *agent.Share) {
	f.ShareType, f.ShareServer, f.SharePath, f.ShareUser = sh.Type, sh.Server, sh.Path, sh.Username
}

// fillShareFrom is the fields as a form sent them (value is its values').
func (f *shareForm) fillShareFrom(value func(string) string) {
	f.ShareType, f.ShareServer, f.SharePath, f.ShareUser = value("share_type"), value("share_server"), value("share_path"), value("share_user")
}

// shareKind makes the share's kind SMB unless it's NFS.
func (f *shareForm) shareKind() {
	if f.ShareType != "nfs" {
		f.ShareType = "smb"
	}
}

// shareFields is the share-fields part's data: the form's fields, its
// server's name, the share's example, and what its password's hint says
// after "readable only by its system". Media is the media library form,
// which says what each kind is and needs the fields while a share is
// chosen.
type shareFields struct {
	shareForm
	ServerName, Example, Kept string
	Media                     bool
}

// sharePosted is the share a step's form sent, with no login for NFS.
func sharePosted(r *http.Request) *agent.Share {
	sh := &agent.Share{Type: r.FormValue("share_type"), Server: strings.TrimSpace(r.FormValue("share_server")),
		Path: strings.TrimSpace(r.FormValue("share_path")), Username: r.FormValue("share_user"), Password: r.FormValue("share_password")}
	if sh.Type == "nfs" {
		sh.Username, sh.Password = "", ""
	}
	return sh
}
