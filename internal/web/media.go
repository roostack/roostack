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
// libraries lists each server's, adds one by stepping through a drive's
// folders (or a server's) with plain links, and manages each; services'
// install pages and settings choose the libraries they read.

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
	SetServiceLibraries(ctx context.Context, service string, libraries []string) error
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

// mediaCrumb is one step of the folders opened so far.
type mediaCrumb struct{ Name, Href string }

// mediaAddView is what the add page shows: where it's looking, and the
// folders there.
type mediaAddView struct {
	Server, ServerName string
	// Drive (UUID), DriveName and Sub; or Folder; neither: choosing.
	Drive, DriveName, Sub, Folder string
	Drives                        []agent.Drive
	Roots                         []string
	List                          *agent.FolderList
	Crumbs                        []mediaCrumb
	// Here is the place chosen so far, in words; Name the library's
	// suggested name.
	Here, Name string
	Readers    []manager.LibraryReader
	Error      string
}

// FolderHref opens a folder below the current place.
func (v mediaAddView) FolderHref(name string) string {
	q := url.Values{"server": {v.Server}}
	if v.Drive != "" {
		q.Set("drive", v.Drive)
		q.Set("name", v.DriveName)
		q.Set("sub", strings.TrimPrefix(v.Sub+"/"+name, "/"))
	} else {
		q.Set("folder", path.Join(v.Folder, name))
	}
	return "/settings/media/add?" + q.Encode()
}

// DriveHref opens a drive's folders.
func (v mediaAddView) DriveHref(d agent.Drive) string {
	return "/settings/media/add?" + url.Values{"server": {v.Server}, "drive": {d.UUID}, "name": {d.Name}}.Encode()
}

// RootHref opens one of the places folders on the server can be.
func (v mediaAddView) RootHref(root string) string {
	return "/settings/media/add?" + url.Values{"server": {v.Server}, "folder": {root}}.Encode()
}

func (s *Server) mediaAddPage(w http.ResponseWriter, r *http.Request) {
	s.renderMediaAdd(w, r, "")
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
	v := mediaAddView{Server: q.Get("server"), Drive: q.Get("drive"), DriveName: q.Get("name"),
		Sub: strings.Trim(path.Clean("/"+q.Get("sub")), "/"), Folder: q.Get("folder"), Error: errMsg}
	if v.Sub == "." {
		v.Sub = ""
	}
	servers, err := s.Libraries.MediaLibraries(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	for _, sv := range servers {
		if sv.ServerID == v.Server || (v.Server == "" && sv.This) {
			v.Server, v.ServerName = sv.ServerID, sv.ServerName
		}
	}
	if v.ServerName == "" {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	switch {
	case v.Drive != "":
		list, err := s.Libraries.LibraryFolders(ctx, v.Server, agent.Library{Drive: v.Drive, Sub: v.Sub})
		if msg, ok := userMessage(err); ok {
			v.Error = msg
			break
		} else if err != nil {
			s.serverError(w, err)
			return
		}
		v.List = &list
		base := url.Values{"server": {v.Server}, "drive": {v.Drive}, "name": {v.DriveName}}
		v.Crumbs = append(v.Crumbs, mediaCrumb{v.DriveName, "/settings/media/add?" + base.Encode()})
		v.Here, v.Name = "the drive "+v.DriveName, v.DriveName
		if v.Sub != "" {
			parts := strings.Split(v.Sub, "/")
			for i, p := range parts {
				base.Set("sub", strings.Join(parts[:i+1], "/"))
				v.Crumbs = append(v.Crumbs, mediaCrumb{p, "/settings/media/add?" + base.Encode()})
			}
			v.Here, v.Name = v.DriveName+" › "+strings.Join(parts, " › "), parts[len(parts)-1]
		}
	case v.Folder != "":
		list, err := s.Libraries.LibraryFolders(ctx, v.Server, agent.Library{Folder: v.Folder})
		if msg, ok := userMessage(err); ok {
			v.Error = msg
			break
		} else if err != nil {
			s.serverError(w, err)
			return
		}
		v.List = &list
		v.Folder = path.Clean(v.Folder)
		dir := ""
		for _, p := range strings.Split(strings.Trim(v.Folder, "/"), "/") {
			dir += "/" + p
			v.Crumbs = append(v.Crumbs, mediaCrumb{p, v.RootHref(dir)})
		}
		v.Here, v.Name = v.Folder, path.Base(v.Folder)
	default:
		drives, err := s.Libraries.LibraryDrives(ctx, v.Server)
		if msg, ok := userMessage(err); ok {
			v.Error = msg
		} else if err != nil {
			s.serverError(w, err)
			return
		}
		v.Drives = drives
		v.Roots = agent.LibraryFolderRoots
	}
	if v.List != nil {
		if v.Readers, err = s.Libraries.LibraryReaders(ctx, v.Server); err != nil {
			s.serverError(w, err)
			return
		}
	}
	if name := r.PostFormValue("title"); name != "" {
		v.Name = name
	}
	data["Add"] = v
	s.render(w, "settings-media-add", withArea(data, "media"))
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
	err := s.Libraries.SetServiceLibraries(r.Context(), name, r.Form["library"])
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
