package manager

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"regexp"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
)

// Media libraries (SPEC.md §8.1, "Media libraries"): the controller keeps
// each server's libraries and sends them with the server's assignment;
// the server mounts them through its agent.

// LibraryStore keeps the servers' media libraries.
type LibraryStore interface {
	MediaLibraries(ctx context.Context) ([]store.MediaLibrary, error)
	SetMediaLibrary(ctx context.Context, l store.MediaLibrary) error
	DeleteMediaLibrary(ctx context.Context, serverID, name string) error
}

// librariesFor is a server's media libraries, as its agent mounts them.
func (m *Manager) librariesFor(ctx context.Context, serverID string) []agent.Library {
	if m.Libraries == nil {
		return nil
	}
	libs, err := m.Libraries.MediaLibraries(ctx)
	if err != nil {
		m.Log.Error("reading media libraries", "err", err)
		return nil
	}
	var out []agent.Library
	for _, l := range libs {
		if l.ServerID == serverID {
			out = append(out, agentLibrary(l))
		}
	}
	return out
}

// agentLibrary is a stored library as the agent mounts it.
func agentLibrary(l store.MediaLibrary) agent.Library {
	a := agent.Library{Name: l.Name, Drive: l.Drive, Sub: l.Sub, Folder: l.Folder}
	if l.ShareType != "" {
		a.Share = &agent.Share{Type: l.ShareType, Server: l.ShareServer, Path: l.SharePath, Username: l.ShareUser}
	}
	if l.OwnerUID != 0 {
		a.AsOwner = &agent.Owner{UID: l.OwnerUID, GID: l.OwnerGID, Name: l.OwnerName}
	}
	return a
}

// LibraryView is a library as its page shows it.
type LibraryView struct {
	store.MediaLibrary
	// Where it is, in words: "USB drive MEDIA › Films", "/srv/music".
	Where string
	// Mounted, Connected and Size are what its server says; Unknown when
	// it couldn't be asked.
	Mounted, Connected, Unknown bool
	Size                        int64
	// ReadBy are the services that may read it (their titles).
	ReadBy []string
}

// ServerLibraries are one server's libraries.
type ServerLibraries struct {
	ServerID, ServerName string
	This                 bool
	Libraries            []LibraryView
	// CanHave: the server can have libraries (its agent answers).
	CanHave bool
}

// MediaLibraries lists every server's libraries, this server first, with
// what each server says about them and which services read them.
func (m *Manager) MediaLibraries(ctx context.Context) ([]ServerLibraries, error) {
	servers, err := m.Servers()
	if err != nil {
		return nil, err
	}
	var stored []store.MediaLibrary
	if m.Libraries != nil {
		if stored, err = m.Libraries.MediaLibraries(ctx); err != nil {
			return nil, err
		}
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return nil, err
	}
	var out []ServerLibraries
	for _, srv := range servers {
		sl := ServerLibraries{ServerID: srv.ID, ServerName: srv.Name, This: srv.This}
		status := map[string]agent.LibraryStatus{}
		asked := false
		if n := m.node(srv.ID); n != nil && srv.Reachable {
			if media := n.Media(); media != nil {
				sl.CanHave = true
				if libs, err := media.Libraries(ctx); err == nil {
					asked = true
					for _, l := range libs {
						status[l.Name] = l
					}
				}
			}
		}
		for _, l := range stored {
			if l.ServerID != srv.ID {
				continue
			}
			v := LibraryView{MediaLibrary: l, Where: libraryWhere(l)}
			if st, ok := status[l.Name]; ok {
				v.Mounted, v.Connected, v.Size = st.Mounted, st.Connected, st.Size
			}
			v.Unknown = !asked
			for _, s := range services {
				if s.Instance.Host == srv.ID && slices.Contains(s.Instance.Libraries, l.Name) {
					v.ReadBy = append(v.ReadBy, s.Title())
				}
			}
			sl.Libraries = append(sl.Libraries, v)
		}
		out = append(out, sl)
	}
	return out, nil
}

// libraryWhere says where a library is, in words.
func libraryWhere(l store.MediaLibrary) string {
	switch l.ShareType {
	case "smb":
		return "The SMB share " + l.SharePath + " on " + l.ShareServer
	case "nfs":
		return "The NFS share " + l.ShareServer + ":" + l.SharePath
	}
	if l.Folder != "" {
		return l.Folder
	}
	where := "The drive " + cmp.Or(l.DriveName, l.Drive)
	if l.Sub != "" {
		where += " › " + strings.ReplaceAll(l.Sub, "/", " › ")
	}
	return where
}

// libraryMedia is a server's media part, or a FormError saying why it has
// none.
func (m *Manager) libraryMedia(serverID string) (node.Media, error) {
	n := m.node(serverID)
	if n == nil {
		return nil, ErrNotFound
	}
	media := n.Media()
	if media == nil {
		return nil, formErrorf("That server can't have media libraries: it doesn't answer, or its Roostack is too old.")
	}
	return media, nil
}

// LibraryDrives lists the drives a server's libraries could be on: its USB
// drives and second disks with a filesystem, backup drives marked.
func (m *Manager) LibraryDrives(ctx context.Context, serverID string) ([]agent.Drive, error) {
	media, err := m.libraryMedia(serverID)
	if err != nil {
		return nil, err
	}
	drives, err := media.Drives(ctx)
	if err != nil {
		return nil, err
	}
	var out []agent.Drive
	for _, d := range drives {
		if (d.Kind == "usb" || d.Kind == "disk") && d.Usable {
			out = append(out, d)
		}
	}
	return out, nil
}

// LibraryFolders lists the folders where a library could be on a server,
// and what a sample of the files there says.
func (m *Manager) LibraryFolders(ctx context.Context, serverID string, l agent.Library) (agent.FolderList, error) {
	media, err := m.libraryMedia(serverID)
	if err != nil {
		return agent.FolderList{}, err
	}
	list, err := media.LibraryFolders(ctx, l)
	if err != nil {
		var fe *node.FormError
		if errors.As(err, &fe) {
			return agent.FolderList{}, formErrorf("%s", fe.Msg)
		}
		return agent.FolderList{}, formErrorf("%s", err)
	}
	return list, nil
}

// LibraryRequest adds a library.
type LibraryRequest struct {
	Server string
	Title  string
	// Drive (a filesystem UUID) and Sub, or Folder.
	Drive, DriveName, Sub, Folder string
	// AsOwner reads the files of the owner the folder's check named.
	AsOwner bool
	// Services may read it (names of services on its server).
	Services []string
	// Share, if set, makes it a network share (its password only goes to
	// the server, to check it and keep its login).
	Share *agent.Share
}

// libraryNameRe and LibraryName: a library's name comes from its title.
var libraryNameRe = regexp.MustCompile(`[^a-z0-9]+`)

// LibraryName is the folder name a title gives a library (in services too).
func LibraryName(title string) string {
	n := strings.Trim(libraryNameRe.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(n) > 32 {
		n = strings.TrimRight(n[:32], "-")
	}
	return n
}

// AddLibrary adds a media library to a server, checking with the server
// that it's there and can be one, for the services chosen, then applies.
func (m *Manager) AddLibrary(ctx context.Context, req LibraryRequest) error {
	if m.Libraries == nil {
		return formErrorf("Media libraries aren't available here.")
	}
	title := strings.TrimSpace(req.Title)
	name := LibraryName(title)
	if title == "" || name == "" || !agent.ValidLibraryName(name) {
		return formErrorf("Give the library a name with some letters or digits, e.g. Films.")
	}
	stored, err := m.Libraries.MediaLibraries(ctx)
	if err != nil {
		return err
	}
	for _, l := range stored {
		if l.ServerID == req.Server && l.Name == name {
			return formErrorf("%s already has a library called %s: choose another name.", m.nameOf(req.Server), l.Title)
		}
	}
	if req.Share != nil {
		return m.addShare(ctx, req, name, title)
	}
	l := agent.Library{Name: name, Drive: req.Drive, Sub: req.Sub, Folder: req.Folder}
	if (l.Drive == "") == (l.Folder == "") {
		return formErrorf("Choose where the library is: a drive, or a folder on the server.")
	}
	// The server checks it can be one, and says who owns what can't be read.
	list, err := m.LibraryFolders(ctx, req.Server, l)
	if err != nil {
		return err
	}
	lib := store.MediaLibrary{ServerID: req.Server, Name: name, Title: title, Drive: req.Drive, DriveName: req.DriveName,
		Sub: list.Path, Folder: req.Folder}
	if req.AsOwner {
		o := list.Check.Owner
		if o == nil || req.Drive == "" {
			return formErrorf("There's no owner to read these files as: Roostack can read them already, or they belong to the system.")
		}
		lib.OwnerUID, lib.OwnerGID, lib.OwnerName = o.UID, o.GID, o.Name
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.Libraries.SetMediaLibrary(ctx, lib); err != nil {
		return err
	}
	if err := m.setReadersLocked(req.Server, name, req.Services); err != nil {
		return err
	}
	m.applyReason = "Adding the media library " + title
	m.startApplyLocked()
	return nil
}

// addShare checks a share with its server, keeps its login there, and
// adds it as a library.
func (m *Manager) addShare(ctx context.Context, req LibraryRequest, name, title string) error {
	media, err := m.libraryMedia(req.Server)
	if err != nil {
		return err
	}
	sh := *req.Share
	sh.Server, sh.Path, sh.Username = strings.TrimSpace(sh.Server), strings.TrimSpace(sh.Path), strings.TrimSpace(sh.Username)
	if sh.Type == "nfs" && !strings.HasPrefix(sh.Path, "/") {
		sh.Path = "/" + sh.Path
	}
	if _, err := media.CheckShare(ctx, sh); err != nil {
		return formErrorf("%s", userText(err))
	}
	if err := media.SaveShareLogin(ctx, name, sh); err != nil {
		return formErrorf("%s", userText(err))
	}
	lib := store.MediaLibrary{ServerID: req.Server, Name: name, Title: title, ShareType: sh.Type, ShareServer: sh.Server,
		SharePath: sh.Path, ShareUser: sh.Username}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.Libraries.SetMediaLibrary(ctx, lib); err != nil {
		return err
	}
	if err := m.setReadersLocked(req.Server, name, req.Services); err != nil {
		return err
	}
	m.applyReason = "Adding the media library " + title
	m.startApplyLocked()
	return nil
}

// userText is an error's words for a form: a FormError's own.
func userText(err error) string {
	var fe *node.FormError
	if errors.As(err, &fe) {
		return fe.Msg
	}
	return err.Error()
}

// RemoveLibrary removes a library from its server and from the services
// reading it, then applies (the server unmounts it; nothing on the drive
// changes).
func (m *Manager) RemoveLibrary(ctx context.Context, serverID, name string) error {
	if m.Libraries == nil {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.Libraries.DeleteMediaLibrary(ctx, serverID, name); errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if err := m.setReadersLocked(serverID, name, nil); err != nil {
		return err
	}
	m.applyReason = "Removing the media library " + name
	m.startApplyLocked()
	return nil
}

// SetLibraryReaders sets which services on its server may read a library.
func (m *Manager) SetLibraryReaders(ctx context.Context, serverID, name string, services []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.setReadersLocked(serverID, name, services); err != nil {
		return err
	}
	m.applyReason = "Changing who reads the media library " + name
	m.startApplyLocked()
	return nil
}

// setReadersLocked adds the library to the instances of the services named
// (on its server, reading libraries) and takes it out of the others'.
func (m *Manager) setReadersLocked(serverID, name string, readers []string) error {
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	for _, s := range services {
		in := s.Instance
		if in.Host != serverID || !s.Package.ReadsLibraries() {
			continue
		}
		had := slices.Contains(in.Libraries, name)
		want := slices.Contains(readers, s.Name())
		if had == want {
			continue
		}
		if want {
			in.Libraries = append(in.Libraries, name)
			slices.Sort(in.Libraries)
		} else {
			in.Libraries = slices.DeleteFunc(in.Libraries, func(l string) bool { return l == name })
		}
		if err := in.Save(state.InstanceFile(m.Lab, s.Name())); err != nil {
			return err
		}
	}
	return nil
}

// SetServiceLibraries sets which of its server's libraries a service may
// read (its settings), and where any of them is inside it, if not in its
// libraries folder (SPEC.md §8.4: library → a container path; nil keeps
// the paths it has, for the libraries it still reads).
func (m *Manager) SetServiceLibraries(ctx context.Context, service string, libraries []string, paths map[string]string) error {
	p, in, err := m.installedService(service)
	if err != nil {
		return err
	}
	if !p.ReadsLibraries() {
		return formErrorf("%s reads no media libraries.", p.Title)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	in.Libraries = m.chosenLibraries(ctx, in.Host, libraries, true)
	if paths == nil {
		paths = in.LibraryPaths
	}
	in.LibraryPaths = nil
	for _, l := range in.Libraries {
		if at := strings.TrimSpace(paths[l]); at != "" {
			if in.LibraryPaths == nil {
				in.LibraryPaths = map[string]string{}
			}
			in.LibraryPaths[l] = strings.TrimSuffix(at, "/")
		}
	}
	for _, l := range slices.Sorted(maps.Keys(in.LibraryPaths)) {
		if err := instance.CheckLibraryPath(p, in.LibraryPaths, l); err != nil {
			return formErrorf("%s can't be at %s inside %s: %v.", m.libraryTitle(ctx, in.Host, l), in.LibraryPaths[l], p.Title, err)
		}
	}
	if err := in.Save(state.InstanceFile(m.Lab, service)); err != nil {
		return err
	}
	m.applyReason = "Changing the media libraries " + p.Title + " reads"
	m.startApplyLocked()
	return nil
}

// ServerLibraryChoices are a server's libraries, for checkboxes.
func (m *Manager) ServerLibraryChoices(ctx context.Context, serverID string) []store.MediaLibrary {
	if m.Libraries == nil {
		return nil
	}
	libs, err := m.Libraries.MediaLibraries(ctx)
	if err != nil {
		m.Log.Error("reading media libraries", "err", err)
		return nil
	}
	var out []store.MediaLibrary
	for _, l := range libs {
		if l.ServerID == cmp.Or(serverID, m.Lab.NodeID) {
			out = append(out, l)
		}
	}
	return out
}

// chosenLibraries are the libraries of a server chosen (those it has), or
// all of them when nothing was chosen.
func (m *Manager) chosenLibraries(ctx context.Context, serverID string, chosen []string, given bool) []string {
	var out []string
	for _, l := range m.ServerLibraryChoices(ctx, serverID) {
		if !given || slices.Contains(chosen, l.Name) {
			out = append(out, l.Name)
		}
	}
	return out
}

// LibraryReader is a service on a server that reads media libraries.
type LibraryReader struct {
	Name, Title string
	Libraries   []string
}

// LibraryReaders are the services on a server whose packages read media
// libraries, by title.
func (m *Manager) LibraryReaders(ctx context.Context, serverID string) ([]LibraryReader, error) {
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return nil, err
	}
	var out []LibraryReader
	for _, s := range services {
		if s.Instance.Host == cmp.Or(serverID, m.Lab.NodeID) && s.Package.ReadsLibraries() {
			out = append(out, LibraryReader{Name: s.Name(), Title: s.Title(), Libraries: s.Instance.Libraries})
		}
	}
	slices.SortFunc(out, func(a, b LibraryReader) int { return strings.Compare(a.Title, b.Title) })
	return out, nil
}

// ServiceLibraryChoice is what a service's settings show about media
// libraries: whether it reads any, those of its server, and its choice;
// where each is inside it (Inside, the package's folder for them, unless
// Paths gives one a place of its own, SPEC.md §8.4).
type ServiceLibraryChoice struct {
	Reads      bool
	ServerName string
	Choices    []store.MediaLibrary
	Chosen     []string
	Inside     string
	Paths      map[string]string
	// Binds are the libraries bound one by one at their paths, which it
	// sees again after their drive was unplugged only once it restarts
	// (instance.PlaceLibraries); Gone are those of them whose drive isn't
	// connected now (their titles), as far as its server says.
	Binds []string
	Gone  []string
}

// ServiceLibraries says which libraries a service may read, of its
// server's.
func (m *Manager) ServiceLibraries(ctx context.Context, service string) (ServiceLibraryChoice, error) {
	p, in, err := m.installedService(service)
	if err != nil {
		return ServiceLibraryChoice{}, err
	}
	c := ServiceLibraryChoice{Reads: p.ReadsLibraries(), Chosen: in.Libraries, ServerName: m.nameOf(cmp.Or(in.Host, m.Lab.NodeID)),
		Paths: in.LibraryPaths, Inside: librariesPath(p)}
	if c.Reads {
		c.Choices = m.ServerLibraryChoices(ctx, in.Host)
	}
	c.Binds = slices.Sorted(maps.Keys(in.PlaceLibraries(p).Binds))
	if len(c.Binds) > 0 {
		c.Gone = m.goneLibraries(ctx, cmp.Or(in.Host, m.Lab.NodeID), c.Binds)
	}
	return c, nil
}

// librariesPath is where a package's containers have the media libraries
// ("/media"), or "".
func librariesPath(p *pkgspec.Package) string {
	for _, c := range p.Containers {
		for _, mt := range c.Mounts {
			if mt.Libraries {
				return mt.Path
			}
		}
	}
	return ""
}

// libraryTitle is a server's library's title, or its name.
func (m *Manager) libraryTitle(ctx context.Context, serverID, name string) string {
	for _, l := range m.ServerLibraryChoices(ctx, serverID) {
		if l.Name == name {
			return l.Title
		}
	}
	return name
}

// goneLibraries are those of a server's libraries whose drive isn't
// connected, by title (none when the server can't be asked).
func (m *Manager) goneLibraries(ctx context.Context, serverID string, names []string) []string {
	n := m.node(serverID)
	if n == nil {
		return nil
	}
	if r, ok := n.(RemoteServer); ok && r.Settings() == nil {
		return nil // not reachable
	}
	media := n.Media()
	if media == nil {
		return nil
	}
	libs, err := media.Libraries(ctx)
	if err != nil {
		return nil
	}
	var gone []string
	for _, l := range libs {
		if slices.Contains(names, l.Name) && !l.Connected {
			gone = append(gone, m.libraryTitle(ctx, serverID, l.Name))
		}
	}
	return gone
}
