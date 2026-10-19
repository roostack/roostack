package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
)

// fakeLibraries is the manager's media library side: one server with a
// drive MEDIA (Films, Music) and a library Films read by Plex.
type fakeLibraries struct {
	added    []manager.LibraryRequest
	removed  []string
	readers  map[string][]string
	svcLibs  map[string][]string
	folderOf []agent.Library
}

func newFakeLibraries() *fakeLibraries {
	return &fakeLibraries{readers: map[string][]string{}, svcLibs: map[string][]string{}}
}

func (f *fakeLibraries) MediaLibraries(context.Context) ([]manager.ServerLibraries, error) {
	return []manager.ServerLibraries{{ServerID: "n-1", ServerName: "nuc", This: true, CanHave: true, Libraries: []manager.LibraryView{{
		MediaLibrary: store.MediaLibrary{ServerID: "n-1", Name: "films", Title: "Films", OwnerName: "sam"},
		Where:        "The drive MEDIA › Films", Mounted: true, Connected: true, Size: 2e12, ReadBy: []string{"Plex"}}}}}, nil
}
func (f *fakeLibraries) LibraryDrives(context.Context, string) ([]agent.Drive, error) {
	return []agent.Drive{{ID: "uuid:9999", Kind: "usb", Name: "ROOSTACK", UUID: "9999", Backup: true},
		{ID: "uuid:1111-2222", Kind: "usb", Name: "MEDIA", UUID: "1111-2222", FSType: "ext4", Size: 2e12, Free: 1e12, Usable: true}}, nil
}
func (f *fakeLibraries) LibraryFolders(_ context.Context, _ string, l agent.Library) (agent.FolderList, error) {
	f.folderOf = append(f.folderOf, l)
	if l.Sub == "Backups" {
		return agent.FolderList{}, &node.FormError{Msg: "that's a backup drive: keep media on another drive"}
	}
	list := agent.FolderList{Path: l.Sub, Folders: []agent.FolderEntry{{Name: "Films", Folders: 412}, {Name: "Music", Folders: 3, Files: 1}}, Files: 1}
	if l.Sub == "Films" {
		list.Folders = nil
		list.Check = agent.Readability{Sampled: 10, Unreadable: 8, Owner: &agent.Owner{UID: 1000, GID: 1000, Name: "sam"}, OwnerFiles: 8}
	}
	return list, nil
}
func (f *fakeLibraries) LibraryReaders(context.Context, string) ([]manager.LibraryReader, error) {
	return []manager.LibraryReader{{Name: "plex", Title: "Plex", Libraries: []string{"films"}}, {Name: "navidrome", Title: "Navidrome"}}, nil
}
func (f *fakeLibraries) AddLibrary(_ context.Context, req manager.LibraryRequest) error {
	if strings.TrimSpace(req.Title) == "" {
		return &node.FormError{Msg: "Give the library a name with some letters or digits, e.g. Films."}
	}
	f.added = append(f.added, req)
	return nil
}
func (f *fakeLibraries) RemoveLibrary(_ context.Context, server, name string) error {
	f.removed = append(f.removed, server+"/"+name)
	return nil
}
func (f *fakeLibraries) SetLibraryReaders(_ context.Context, server, name string, services []string) error {
	f.readers[server+"/"+name] = services
	return nil
}
func (f *fakeLibraries) SetServiceLibraries(_ context.Context, service string, libs []string) error {
	f.svcLibs[service] = libs
	return nil
}
func (f *fakeLibraries) ServerLibraryChoices(context.Context, string) []store.MediaLibrary {
	return []store.MediaLibrary{{ServerID: "n-1", Name: "films", Title: "Films"}}
}
func (f *fakeLibraries) ServiceLibraries(context.Context, string) (manager.ServiceLibraryChoice, error) {
	return manager.ServiceLibraryChoice{}, nil
}

// Settings → Media libraries lists each library with where it is, its
// state and its readers; only an Owner sees it.
func TestMediaPage(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.server.Libraries = newFakeLibraries()
	_, body := h.get("/settings/media")
	for _, want := range []string{"Media libraries", "Films", "The drive MEDIA › Films", "Connected", "read as its owner, sam", "Plex",
		`href="/settings/media/n-1/films"`, `href="/settings/media/add?server=n-1"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
	admin, _ := loggedIn(t, auth.RoleAdmin)
	admin.server.Libraries = newFakeLibraries()
	if code, _ := admin.get("/settings/media"); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Errorf("an Admin got %d", code)
	}
}

// Adding a library is one form: where it is (a segmented control), the
// folder chosen with its check, the read-as-owner tickbox and the
// services; the form passes on only the chosen place.
func TestMediaAdd(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	libs := newFakeLibraries()
	h.server.Libraries = libs
	_, body := h.get("/settings/media/add")
	for _, want := range []string{"Where is it?", `name="where" value="drive" checked`, `value="folder"`, "Folder on nuc", `value="share"`,
		"ROOSTACK", "Your backups' drive", `name="drive" value="1111-2222" checked`, "USB drive", "MEDIA (the whole drive)", "2 folders, 1 file",
		`href="/settings/media/folders?drive=1111-2222&amp;name=MEDIA&amp;server=n-1&amp;sub="`, `data-picker="folder-picker-dialog"`,
		`href="/settings/media/folders?folder=&amp;server=n-1"`, "No folder chosen yet", `name="title" value="MEDIA"`,
		`name="service" value="plex" checked`, `name="share_type" value="smb" checked`, `id="place-drive"`, `id="place-folder"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the form lacks %q", want)
		}
	}
	if strings.Contains(body, `name="drive" value="9999"`+" checked") || !strings.Contains(body, `value="9999" disabled`) {
		t.Error("the backup drive can be chosen")
	}
	// A folder chosen in the picker comes back as the place, with its check.
	_, body = h.get("/settings/media/add?server=n-1&where=drive&drive=1111-2222&name=MEDIA&sub=Films")
	for _, want := range []string{"MEDIA › Films", "can only be read by their owner, sam", `name="as_owner" checked`,
		`name="title" value="Films"`, `name="sub" value="Films"`, "empty"} {
		if !strings.Contains(body, want) {
			t.Errorf("the folder chosen lacks %q", want)
		}
	}
	_, body = h.get("/settings/media/add?server=n-1&where=drive&drive=1111-2222&name=MEDIA&sub=Backups")
	if !strings.Contains(body, "that&#39;s a backup drive") {
		t.Errorf("a refusal is shown:\n%s", body)
	}
	_, body = h.get("/settings/media/add?server=n-1&where=folder&folder=/srv/media")
	for _, want := range []string{`name="where" value="folder" checked`, "<strong>/srv/media", `name="folder" value="/srv/media"`,
		`name="title" value="media"`} {
		if !strings.Contains(body, want) {
			t.Errorf("a folder on the server lacks %q", want)
		}
	}
	form := url.Values{"server": {"n-1"}, "where": {"drive"}, "drive": {"1111-2222"}, "name": {"MEDIA"}, "sub": {"Films"}, "folder": {""},
		"title": {"Films"}, "as_owner": {"on"}, "service": {"plex", "navidrome"}}
	if code, _ := h.post("/settings/media/add", form, h.srv.URL); code != http.StatusOK || len(libs.added) != 1 {
		t.Fatalf("adding: %d %+v", code, libs.added)
	}
	if a := libs.added[0]; a.Server != "n-1" || a.Drive != "1111-2222" || a.Sub != "Films" || !a.AsOwner || a.Folder != "" ||
		!slices.Equal(a.Services, []string{"plex", "navidrome"}) || a.DriveName != "MEDIA" {
		t.Errorf("added %+v", a)
	}
	// Only the chosen place counts: the drive's fields are on the form too.
	folder := url.Values{"server": {"n-1"}, "where": {"folder"}, "drive": {"1111-2222"}, "name": {"MEDIA"}, "sub": {"Films"},
		"folder": {"/srv/media"}, "as_owner": {"on"}, "title": {"Media"}}
	h.post("/settings/media/add", folder, h.srv.URL)
	if a := libs.added[len(libs.added)-1]; a.Folder != "/srv/media" || a.Drive != "" || a.Sub != "" || a.AsOwner || a.Share != nil {
		t.Errorf("a folder on the server: %+v", a)
	}
	share := url.Values{"server": {"n-1"}, "where": {"share"}, "drive": {"1111-2222"}, "share_type": {"smb"}, "share_server": {"nas.local"},
		"share_path": {"media"}, "share_user": {"sam"}, "share_password": {"s3cret pass"}, "title": {"Media"}, "service": {"plex"}}
	h.post("/settings/media/add", share, h.srv.URL)
	if a := libs.added[len(libs.added)-1]; a.Share == nil || a.Share.Server != "nas.local" || a.Share.Path != "media" ||
		a.Share.Username != "sam" || a.Share.Password != "s3cret pass" || a.Title != "Media" || a.Drive != "" {
		t.Errorf("a share: %+v %+v", a, a.Share)
	}
	share.Set("title", " ")
	share.Set("share_type", "nfs")
	_, body = h.post("/settings/media/add", share, h.srv.URL)
	for _, want := range []string{`value="nas.local"`, "Give the library a name", `name="where" value="share" checked`,
		`name="share_type" value="nfs" checked`, `name="service" value="plex" checked`, `name="service" value="navidrome">`} {
		if !strings.Contains(body, want) {
			t.Errorf("a share's form comes back without %q", want)
		}
	}
	if strings.Contains(body, "s3cret pass") {
		t.Error("a share's form came back with its password")
	}
	form.Set("title", " ")
	if _, body := h.post("/settings/media/add", form, h.srv.URL); !strings.Contains(body, "Give the library a name") ||
		!strings.Contains(body, "MEDIA › Films") {
		t.Errorf("a form error comes back on the page, with the folder chosen:\n%s", body)
	}
}

// The folder picker steps through a drive's folders (or the server's),
// each with what it holds; Use goes back to the form with it chosen. It's
// a page of its own (without scripts) and what the dialog shows.
func TestMediaFolders(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	libs := newFakeLibraries()
	h.server.Libraries = libs
	_, body := h.get("/settings/media/folders?server=n-1&drive=1111-2222&name=MEDIA")
	for _, want := range []string{`id="folder-picker"`, "Choose a folder on MEDIA", "412 folders", "3 folders, 1 file",
		`href="/settings/media/folders?drive=1111-2222&amp;name=MEDIA&amp;server=n-1&amp;sub=Films"`,
		`href="/settings/media/add?drive=1111-2222&amp;name=MEDIA&amp;server=n-1&amp;sub=&amp;where=drive" data-use>Use MEDIA`, "MEDIA holds 2 folders, 1 file"} {
		if !strings.Contains(body, want) {
			t.Errorf("a drive's folders lack %q", want)
		}
	}
	_, body = h.get("/settings/media/folders?server=n-1&drive=1111-2222&name=MEDIA&sub=Films")
	for _, want := range []string{`<a href="/settings/media/folders?drive=1111-2222&amp;name=MEDIA&amp;server=n-1">MEDIA</a>`,
		`<strong aria-current="true">Films</strong>`, "No folders in here.", "sub=Films&amp;where=drive\" data-use>Use Films"} {
		if !strings.Contains(body, want) {
			t.Errorf("a folder on the drive lacks %q", want)
		}
	}
	_, body = h.get("/settings/media/folders?server=n-1&drive=1111-2222&name=MEDIA&sub=Backups")
	if !strings.Contains(body, "that&#39;s a backup drive") || strings.Contains(body, "data-use") {
		t.Errorf("a refusal can't be used:\n%s", body)
	}
	_, body = h.get("/settings/media/folders?server=n-1")
	for _, want := range []string{"Choose a folder on nuc", `href="/settings/media/folders?folder=%2Fmnt&amp;server=n-1"`, "/srv",
		"Open a folder to choose it"} {
		if !strings.Contains(body, want) {
			t.Errorf("the server's places lack %q", want)
		}
	}
	if strings.Contains(body, "data-use") {
		t.Error("the list of places can be used")
	}
	_, body = h.get("/settings/media/folders?server=n-1&folder=/srv/../srv/media")
	if l := libs.folderOf[len(libs.folderOf)-1]; l.Folder != "/srv/media" || l.Drive != "" {
		t.Errorf("looked at %+v", l)
	}
	for _, want := range []string{"folder=%2Fsrv%2Fmedia%2FFilms", "Use media", "folder=%2Fsrv%2Fmedia&amp;server=n-1&amp;where=folder"} {
		if !strings.Contains(body, want) {
			t.Errorf("a folder on the server lacks %q", want)
		}
	}
}

// A library's page sets its readers and removes it.
func TestMediaLibraryPage(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	libs := newFakeLibraries()
	h.server.Libraries = libs
	_, body := h.get("/settings/media/n-1/films")
	for _, want := range []string{"Films", "The drive MEDIA › Films", "its owner, sam", `value="plex" checked`, `value="navidrome">`, "Remove library"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if code, _ := h.get("/settings/media/n-1/nope"); code != http.StatusNotFound {
		t.Errorf("an unknown library: %d", code)
	}
	h.post("/settings/media/n-1/films/readers", url.Values{"service": {"navidrome"}}, h.srv.URL)
	if got := libs.readers["n-1/films"]; !slices.Equal(got, []string{"navidrome"}) {
		t.Errorf("readers %q", got)
	}
	h.post("/settings/media/n-1/films/remove", url.Values{}, h.srv.URL)
	if !slices.Equal(libs.removed, []string{"n-1/films"}) {
		t.Errorf("removed %q", libs.removed)
	}
	h.post("/services/plex/libraries", url.Values{"library": {"films"}}, h.srv.URL)
	if got := libs.svcLibs["plex"]; !slices.Equal(got, []string{"films"}) {
		t.Errorf("a service's libraries: %q", got)
	}
}
