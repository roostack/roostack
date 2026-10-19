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
	return []agent.Drive{{ID: "uuid:1111-2222", Kind: "usb", Name: "MEDIA", UUID: "1111-2222", FSType: "ext4", Size: 2e12, Free: 1e12, Usable: true}}, nil
}
func (f *fakeLibraries) LibraryFolders(_ context.Context, _ string, l agent.Library) (agent.FolderList, error) {
	f.folderOf = append(f.folderOf, l)
	if l.Sub == "Backups" {
		return agent.FolderList{}, &node.FormError{Msg: "that's a backup drive: keep media on another drive"}
	}
	list := agent.FolderList{Path: l.Sub, Folders: []agent.FolderEntry{{Name: "Films", Folders: 412}, {Name: "Music", Folders: 3, Files: 1}}}
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

// Adding a library steps through a drive's folders with links; the folder
// chosen shows its check, the read-as-owner tickbox and the services; the
// form passes all of it on.
func TestMediaAdd(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	libs := newFakeLibraries()
	h.server.Libraries = libs
	_, body := h.get("/settings/media/add")
	for _, want := range []string{"Where is it?", "MEDIA", "USB drive", "/settings/media/add?drive=1111-2222", "/mnt"} {
		if !strings.Contains(body, want) {
			t.Errorf("the first step lacks %q", want)
		}
	}
	_, body = h.get("/settings/media/add?server=n-1&drive=1111-2222&name=MEDIA")
	if !strings.Contains(body, "sub=Films") || !strings.Contains(body, "412 folders") || !strings.Contains(body, "Use the drive MEDIA") {
		t.Errorf("the drive's folders:\n%s", body)
	}
	_, body = h.get("/settings/media/add?server=n-1&drive=1111-2222&name=MEDIA&sub=Films")
	for _, want := range []string{"Use MEDIA › Films", "can only be read by their owner, sam", `name="as_owner" checked`,
		`name="title" value="Films"`, `name="service" value="plex" checked`, `name="sub" value="Films"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the folder chosen lacks %q", want)
		}
	}
	_, body = h.get("/settings/media/add?server=n-1&drive=1111-2222&name=MEDIA&sub=Backups")
	if !strings.Contains(body, "that&#39;s a backup drive") {
		t.Errorf("a refusal is shown:\n%s", body)
	}
	form := url.Values{"server": {"n-1"}, "drive": {"1111-2222"}, "name": {"MEDIA"}, "sub": {"Films"}, "title": {"Films"},
		"as_owner": {"on"}, "service": {"plex", "navidrome"}}
	if code, _ := h.post("/settings/media/add", form, h.srv.URL); code != http.StatusOK || len(libs.added) != 1 {
		t.Fatalf("adding: %d %+v", code, libs.added)
	}
	if a := libs.added[0]; a.Server != "n-1" || a.Drive != "1111-2222" || a.Sub != "Films" || !a.AsOwner ||
		!slices.Equal(a.Services, []string{"plex", "navidrome"}) || a.DriveName != "MEDIA" {
		t.Errorf("added %+v", a)
	}
	share := url.Values{"server": {"n-1"}, "where": {"share"}, "share_type": {"smb"}, "share_server": {"nas.local"}, "share_path": {"media"},
		"share_user": {"sam"}, "share_password": {"s3cret pass"}, "title": {"Media"}, "service": {"plex"}}
	h.post("/settings/media/add", share, h.srv.URL)
	if a := libs.added[len(libs.added)-1]; a.Share == nil || a.Share.Server != "nas.local" || a.Share.Path != "media" ||
		a.Share.Username != "sam" || a.Share.Password != "s3cret pass" || a.Title != "Media" {
		t.Errorf("a share: %+v %+v", a, a.Share)
	}
	share.Set("title", " ")
	if _, body := h.post("/settings/media/add", share, h.srv.URL); !strings.Contains(body, `value="nas.local"`) ||
		strings.Contains(body, "s3cret pass") || !strings.Contains(body, "Give the library a name") {
		t.Errorf("a share's form comes back with its values but never the password:\n%s", body)
	}
	form.Set("title", " ")
	if _, body := h.post("/settings/media/add", form, h.srv.URL); !strings.Contains(body, "Give the library a name") {
		t.Errorf("a form error comes back on the page:\n%s", body)
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
