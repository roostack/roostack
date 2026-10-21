package web

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/store"
)

// Media libraries (SPEC.md §8.1, "Media libraries"): Settings → Media
// libraries lists each server's, adds one on a single form (where it is,
// a folder chosen in the folder picker, its name and readers), and
// manages each; services' install pages and settings choose the libraries
// they read.

func init() {
	// join lists strings with a separator, e.g. services reading a library.
	templateFuncs["join"] = strings.Join
}

// LibraryManager is the manager's media library operations.
type LibraryManager interface {
	MediaLibraries(ctx context.Context) ([]manager.ServerLibraries, error)
	LibraryDrives(ctx context.Context, serverID string) ([]agent.Drive, error)
	LibraryFolders(ctx context.Context, serverID string, l agent.Library) (agent.FolderList, error)
	LibraryReaders(ctx context.Context, serverID string) ([]manager.LibraryReader, error)
	AddLibrary(ctx context.Context, req manager.LibraryRequest) error
	RemoveLibrary(ctx context.Context, serverID, name string) error
	SetLibraryReaders(ctx context.Context, serverID, name string, services []string) error
	SetServiceLibraries(ctx context.Context, service string, libraries []string, paths map[string]string) error
	ServerLibraryChoices(ctx context.Context, serverID string) []store.MediaLibrary
	ServiceLibraries(ctx context.Context, service string) (manager.ServiceLibraryChoice, error)
}

func (s *Server) mediaPage(w http.ResponseWriter, r *http.Request) {
	if s.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	servers, err := s.Libraries.MediaLibraries(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Servers"] = servers
	data["Several"] = len(servers) > 1
	s.render(w, "settings-media", withArea(data, "media"))
}

// mediaCrumb is one step of the folders opened so far ("" Href: the one
// open now).
type mediaCrumb struct{ Name, Href string }

// mediaPlace is where a new library would be, so far: a drive and a
// folder on it, or a folder on the server; with what's there and what the
// check of it found.
type mediaPlace struct {
	Drive, DriveName, Sub string
	Folder                string
	// Label is the place in words ("MEDIA › Films", "/srv/media"; "" for
	// no folder chosen yet), Counts what it holds, Name the library's
	// suggested name.
	Label, Counts, Name string
	Check               *agent.Readability
	// PickHref opens the folder picker there.
	PickHref string
	Error    string
}

// mediaReader is a service that could read the new library.
type mediaReader struct {
	Name, Title string
	Checked     bool
}

// mediaAddView is the add page: one form, where the library is chosen
// with a segmented control (a drive, a folder on the server or a network
// share), then its name and the services that read it.
type mediaAddView struct {
	Server, ServerName string
	// Where is drive, folder or share.
	Where             string
	Drives            []agent.Drive
	DrivesError       string
	DrivePlace, Place mediaPlace
	HasDrive          bool
	Roots             []string
	Readers           []mediaReader
	Title             string
	TitleEdited       bool
	Error             string
	// The share form's values, kept when it comes back (never the
	// password).
	ShareType, ShareServer, SharePath, ShareUser string
	// PhotosFor makes it an additional location for migrating photos into
	// that Immich (SPEC.md §8.5): the same form, with no services, back to
	// its page; PhotosTitle is the Immich's title.
	PhotosFor, PhotosTitle string
}

// mediaAddHref is the add page with a place chosen.
func mediaAddHref(q url.Values) string { return "/settings/media/add?" + q.Encode() }

// DriveHref chooses a drive (its whole drive), for its radio's refresh.
func (v mediaAddView) DriveHref(d agent.Drive) string {
	q := url.Values{"server": {v.Server}, "where": {"drive"}, "drive": {d.UUID}, "name": {d.Name}}
	if v.PhotosFor != "" {
		q.Set("photos", v.PhotosFor)
	}
	return mediaAddHref(q)
}

func (s *Server) mediaAddPage(w http.ResponseWriter, r *http.Request) {
	s.renderMediaAdd(w, r, "")
}

// mediaServer finds the server a media page is about (this one if none is
// named).
func (s *Server) mediaServer(r *http.Request, id string) (manager.ServerLibraries, bool, error) {
	servers, err := s.Libraries.MediaLibraries(r.Context())
	if err != nil {
		return manager.ServerLibraries{}, false, err
	}
	for _, sv := range servers {
		if sv.ServerID == id || (id == "" && sv.This) {
			return sv, true, nil
		}
	}
	return manager.ServerLibraries{}, false, nil
}

// cleanSub is a folder on a drive as the agent takes it ("" for its top).
func cleanSub(sub string) string {
	sub = strings.Trim(path.Clean("/"+sub), "/")
	if sub == "." {
		return ""
	}
	return sub
}

// mediaPlaceAt looks at a place: its folders' counts and the check. A
// refusal people can act on (a backup drive) is the place's Error.
func (s *Server) mediaPlaceAt(ctx context.Context, server string, p mediaPlace) (mediaPlace, error) {
	q := url.Values{"server": {server}}
	l := agent.Library{Drive: p.Drive, Sub: p.Sub, Folder: p.Folder}
	if p.Drive != "" {
		q.Set("drive", p.Drive)
		q.Set("name", p.DriveName)
		q.Set("sub", p.Sub)
		p.Label, p.Name = p.DriveName, p.DriveName
		if p.Sub != "" {
			parts := strings.Split(p.Sub, "/")
			p.Label, p.Name = p.DriveName+" › "+strings.Join(parts, " › "), parts[len(parts)-1]
		}
	} else {
		q.Set("folder", p.Folder)
	}
	p.PickHref = "/settings/media/folders?" + q.Encode()
	if p.Drive == "" {
		if p.Folder == "" {
			return p, nil
		}
		p.Label, p.Name = p.Folder, path.Base(p.Folder)
	}
	list, err := s.Libraries.LibraryFolders(ctx, server, l)
	if msg, ok := userMessage(err); ok {
		p.Error = msg
		return p, nil
	} else if err != nil {
		return p, err
	}
	p.Counts = folderCounts(len(list.Folders), list.Files)
	if list.Check.Unreadable > 0 {
		p.Check = &list.Check
	}
	return p, nil
}

func (s *Server) renderMediaAdd(w http.ResponseWriter, r *http.Request, errMsg string) {
	if s.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	q := r.Form
	if q == nil {
		q = r.URL.Query()
	}
	sv, ok, err := s.mediaServer(r, q.Get("server"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	posted := r.Method == http.MethodPost
	v := mediaAddView{Server: sv.ServerID, ServerName: sv.ServerName, Where: q.Get("where"), Error: errMsg,
		Roots: agent.LibraryFolderRoots}
	if pf := q.Get("photos"); pf != "" {
		ps, ok := s.photoService(r.Context(), pf)
		if !ok || ps.Host != sv.ServerID {
			http.NotFound(w, r)
			return
		}
		v.PhotosFor, v.PhotosTitle = ps.Name, ps.Title
	}
	if !slices.Contains([]string{"drive", "folder", "share"}, v.Where) {
		v.Where = "drive"
		if q.Get("folder") != "" {
			v.Where = "folder"
		}
	}
	ctx := r.Context()
	drives, err := s.Libraries.LibraryDrives(ctx, v.Server)
	if msg, ok := userMessage(err); ok {
		v.DrivesError = msg
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	v.Drives = drives
	// The drive chosen: the one asked for, or the first that can be one.
	for _, d := range drives {
		if !d.Backup && (d.UUID == q.Get("drive") || (!v.HasDrive && q.Get("drive") == "")) {
			v.HasDrive = true
			v.DrivePlace = mediaPlace{Drive: d.UUID, DriveName: d.Name}
			if d.UUID == q.Get("drive") {
				v.DrivePlace.Sub = cleanSub(q.Get("sub"))
			}
		}
	}
	if v.HasDrive {
		if v.DrivePlace, err = s.mediaPlaceAt(ctx, v.Server, v.DrivePlace); err != nil {
			s.serverError(w, err)
			return
		}
	}
	folder := q.Get("folder")
	if folder != "" {
		folder = path.Clean("/" + folder)
	}
	if v.Place, err = s.mediaPlaceAt(ctx, v.Server, mediaPlace{Folder: folder}); err != nil {
		s.serverError(w, err)
		return
	}
	readers, err := s.Libraries.LibraryReaders(ctx, v.Server)
	if err != nil {
		s.serverError(w, err)
		return
	}
	if v.PhotosFor != "" {
		readers = nil // no service reads a location for migrations
	}
	for _, rd := range readers {
		v.Readers = append(v.Readers, mediaReader{Name: rd.Name, Title: rd.Title, Checked: !posted || slices.Contains(q["service"], rd.Name)})
	}
	v.ShareType = r.PostFormValue("share_type")
	if v.ShareType != "nfs" {
		v.ShareType = "smb"
	}
	v.ShareServer, v.SharePath, v.ShareUser = r.PostFormValue("share_server"), r.PostFormValue("share_path"), r.PostFormValue("share_user")
	switch {
	case posted:
		v.Title, v.TitleEdited = r.PostFormValue("title"), true
	case v.Where == "drive":
		v.Title = v.DrivePlace.Name
	case v.Where == "folder":
		v.Title = v.Place.Name
	}
	data["Add"] = v
	if v.PhotosFor != "" {
		data["NavActive"] = "services"
		s.render(w, "settings-media-add", data)
		return
	}
	s.render(w, "settings-media-add", withArea(data, "media"))
}

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

// mediaFoldersPage is the folder picker: in the add page's dialog
// (app.js shows its #folder-picker), or a page of its own without scripts.
// Each folder opens a folder below; Use goes back to the add page with it
// chosen.
func (s *Server) mediaFoldersPage(w http.ResponseWriter, r *http.Request) {
	if s.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	q := r.URL.Query()
	sv, ok, err := s.mediaServer(r, q.Get("server"))
	if err != nil {
		s.serverError(w, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	server := sv.ServerID
	back := url.Values{"server": {server}}
	photos := q.Get("photos")
	if photos != "" {
		back.Set("photos", photos)
	}
	at := func(set url.Values) string {
		u := url.Values{"server": {server}}
		if photos != "" {
			u.Set("photos", photos)
		}
		for k, vs := range set {
			u[k] = vs
		}
		return "/settings/media/folders?" + u.Encode()
	}
	var p folderPicker
	var l agent.Library
	var folderHref func(name string) string
	if drive := q.Get("drive"); drive != "" {
		name, sub := q.Get("name"), cleanSub(q.Get("sub"))
		l = agent.Library{Drive: drive, Sub: sub}
		base := url.Values{"drive": {drive}, "name": {name}}
		p.Title, p.Label, p.UseText = "Choose a folder on "+name, name, "Use "+name
		p.Crumbs = []mediaCrumb{{name, at(base)}}
		if sub != "" {
			parts := strings.Split(sub, "/")
			for i, part := range parts {
				p.Crumbs = append(p.Crumbs, mediaCrumb{part, at(url.Values{"drive": {drive}, "name": {name}, "sub": {strings.Join(parts[:i+1], "/")}})})
			}
			p.Label, p.UseText = name+" › "+strings.Join(parts, " › "), "Use "+parts[len(parts)-1]
		}
		back.Set("where", "drive")
		back.Set("drive", drive)
		back.Set("name", name)
		p.CancelHref = mediaAddHref(back)
		back.Set("sub", sub)
		p.UseHref = mediaAddHref(back)
		folderHref = func(f string) string {
			return at(url.Values{"drive": {drive}, "name": {name}, "sub": {strings.TrimPrefix(sub+"/"+f, "/")}})
		}
	} else {
		folder := q.Get("folder")
		back.Set("where", "folder")
		p.CancelHref = mediaAddHref(back)
		p.Title = "Choose a folder on " + sv.ServerName
		p.Crumbs = []mediaCrumb{{sv.ServerName, at(nil)}}
		if folder == "" {
			// The places folders can be, to start with.
			for _, root := range agent.LibraryFolderRoots {
				p.Folders = append(p.Folders, pickerFolder{Name: root, Href: at(url.Values{"folder": {root}}), NoCounts: true})
			}
			p.Crumbs[0].Href = ""
			data["Picker"] = p
			s.render(w, "settings-media-folders", withArea(data, "media"))
			return
		}
		folder = path.Clean("/" + folder)
		l = agent.Library{Folder: folder}
		dir := ""
		for _, part := range strings.Split(strings.Trim(folder, "/"), "/") {
			dir += "/" + part
			p.Crumbs = append(p.Crumbs, mediaCrumb{part, at(url.Values{"folder": {dir}})})
		}
		p.Label, p.UseText = folder, "Use "+path.Base(folder)
		back.Set("folder", folder)
		p.UseHref = mediaAddHref(back)
		folderHref = func(f string) string { return at(url.Values{"folder": {path.Join(folder, f)}}) }
	}
	p.Crumbs[len(p.Crumbs)-1].Href = ""
	list, err := s.Libraries.LibraryFolders(r.Context(), server, l)
	if msg, ok := userMessage(err); ok {
		p.Error, p.UseHref = msg, ""
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	for _, f := range list.Folders {
		p.Folders = append(p.Folders, pickerFolder{Name: f.Name, Href: folderHref(f.Name), Folders: f.Folders, Files: f.Files})
	}
	p.Counts = folderCounts(len(list.Folders), list.Files)
	data["Picker"] = p
	s.render(w, "settings-media-folders", withArea(data, "media"))
}

func (s *Server) mediaAddSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	req := manager.LibraryRequest{Server: r.FormValue("server"), Title: r.FormValue("title"), Drive: r.FormValue("drive"),
		DriveName: r.FormValue("name"), Sub: r.FormValue("sub"), Folder: r.FormValue("folder"),
		AsOwner: r.FormValue("as_owner") == "on", Services: r.Form["service"]}
	// The form holds every choice's fields: only the chosen one's count.
	switch r.FormValue("where") {
	case "drive":
		req.Folder = ""
	case "folder":
		req.Drive, req.DriveName, req.Sub, req.AsOwner = "", "", "", false
	case "share":
		req.Drive, req.DriveName, req.Sub, req.Folder, req.AsOwner = "", "", "", "", false
	}
	if r.FormValue("where") == "share" {
		req.Share = &agent.Share{Type: r.FormValue("share_type"), Server: r.FormValue("share_server"),
			Path: r.FormValue("share_path"), Username: r.FormValue("share_user"), Password: r.FormValue("share_password")}
		if req.Share.Type == "nfs" {
			req.Share.Username, req.Share.Password = "", ""
		}
	}
	if pf := r.FormValue("photos"); pf != "" && s.Photos != nil {
		err := s.Photos.AddPhotoLocation(r.Context(), pf, req)
		if msg, ok := userMessage(err); ok {
			s.renderMediaAdd(w, r, msg)
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
		s.Store.Audit(r.Context(), currentUser(r), "photos", pf+": added the location "+strings.TrimSpace(req.Title))
		http.Redirect(w, r, "/services/"+url.PathEscape(pf)+"#photos", http.StatusSeeOther)
		return
	}
	err := s.Libraries.AddLibrary(r.Context(), req)
	if msg, ok := userMessage(err); ok {
		s.renderMediaAdd(w, r, msg)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "media", "added the media library "+strings.TrimSpace(req.Title))
	http.Redirect(w, r, "/settings/media", http.StatusSeeOther)
}

// mediaLibrary finds a library by its server and name.
func (s *Server) mediaLibrary(r *http.Request) (manager.ServerLibraries, manager.LibraryView, bool) {
	servers, err := s.Libraries.MediaLibraries(r.Context())
	if err != nil {
		return manager.ServerLibraries{}, manager.LibraryView{}, false
	}
	for _, sv := range servers {
		if sv.ServerID != r.PathValue("server") {
			continue
		}
		for _, l := range sv.Libraries {
			if l.Name == r.PathValue("name") {
				return sv, l, true
			}
		}
	}
	return manager.ServerLibraries{}, manager.LibraryView{}, false
}

func (s *Server) mediaLibraryPage(w http.ResponseWriter, r *http.Request) {
	if s.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	sv, lib, ok := s.mediaLibrary(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	readers, err := s.Libraries.LibraryReaders(r.Context(), sv.ServerID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Server"], data["Library"], data["Readers"] = sv, lib, readers
	s.render(w, "settings-media-library", withArea(data, "media"))
}

func (s *Server) mediaReadersSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	server, name := r.PathValue("server"), r.PathValue("name")
	if _, _, ok := s.mediaLibrary(r); !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.Libraries.SetLibraryReaders(r.Context(), server, name, r.Form["service"]); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/settings/media/"+url.PathEscape(server)+"/"+url.PathEscape(name), http.StatusSeeOther)
}

func (s *Server) mediaRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	_, lib, ok := s.mediaLibrary(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.Libraries.RemoveLibrary(r.Context(), lib.ServerID, lib.Name); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "media", "removed the media library "+lib.Title)
	http.Redirect(w, r, "/settings/media", http.StatusSeeOther)
}

// serviceLibrariesSubmit sets which libraries a service reads (its
// settings page).
func (s *Server) serviceLibrariesSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Libraries == nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	name := r.PathValue("name")
	err := s.Libraries.SetServiceLibraries(r.Context(), name, r.Form["library"], nil)
	if msg, ok := userMessage(err); ok {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/services/"+url.PathEscape(name)+"/settings", http.StatusSeeOther)
}

// libraryGroup is a server's libraries on an install page: ticked unless
// the form came back without them.
type libraryGroup struct {
	ServerID, ServerName string
	Choices              []libraryChoice
}

type libraryChoice struct {
	Name, Title string
	Checked     bool
}

// libraryGroups are each server's libraries, for a package that reads
// them; a group shows while its server is the one chosen.
func (s *Server) libraryGroups(r *http.Request, p *pkgspec.Package, submitted map[string][]string) []libraryGroup {
	if s.Libraries == nil || !p.ReadsLibraries() {
		return nil
	}
	servers, err := s.Libraries.MediaLibraries(r.Context())
	if err != nil {
		return nil
	}
	var out []libraryGroup
	for _, sv := range servers {
		g := libraryGroup{ServerID: sv.ServerID, ServerName: sv.ServerName}
		for _, l := range sv.Libraries {
			checked := submitted == nil || slices.Contains(submitted["library."+sv.ServerID], l.Name)
			g.Choices = append(g.Choices, libraryChoice{Name: l.Name, Title: l.Title, Checked: checked})
		}
		out = append(out, g)
	}
	return out
}

// thisServerID is the server serving the page.
func (s *Server) thisServerID() string {
	if s.Node == nil {
		return ""
	}
	return s.Node.Identity().ID
}
