package web

import (
	"cmp"
	"context"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/photoimport"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
	"roostack.cloud/roostack/units"
)

// Migrating photos into Immich (SPEC.md §8.5, docs/mockups/immich-import):
// numbered steps, each done one shrinking to a line with Change, from
// where the photos are now to the summary. Owners and Admins take them on
// Immich's page in the control panel (/services/<name>/photos); everyone
// else the Owner allows on a page of its own (/immich-photos/<name>),
// with the lab's name and none of the control panel's menus. Both post to
// /immich-photos/<name>/..., which checks who may. The service's server
// keeps each person's steps and runs the migration (node.Photos).

// PhotoMigrations is what the pages need from the manager.
type PhotoMigrations interface {
	PhotoServices(ctx context.Context) []manager.PhotoService
	PhotosOf(service string) (node.Photos, error)
	PhotoSettings(ctx context.Context, service string) manager.PhotoSettings
	SetPhotoSettings(ctx context.Context, service string, s manager.PhotoSettings) error
	PhotoLocations(ctx context.Context, service string) []store.MediaLibrary
	AddPhotoLocation(ctx context.Context, service string, req manager.LibraryRequest) error
	RemovePhotoLocation(ctx context.Context, service, name string) error
	PhotoDrives(ctx context.Context, service string, admin bool) ([]manager.PhotoDrive, error)
	CheckPhotoSource(ctx context.Context, service string, src node.PhotoSource, admin bool) error
	// MigrationStarted tells the Owners when someone else starts one.
	MigrationStarted(ctx context.Context, service string, u *store.User)
}

// Steps of a migration, in order.
const (
	photoStepFrom = 1 + iota
	photoStepExport
	photoStepWhere
	photoStepKey
	photoStepChoose
	photoStepCheck
	photoStepMigrate
	photoStepDone
)

// photoPage is a migration's page.
type photoPage struct {
	Name, Title string
	Service     ServiceView
	// Panel: Immich's page in the control panel, for Owners and Admins;
	// else the page of its own. Base is where the page is, Post where its
	// forms go.
	Panel      bool
	Base, Post string
	Admin      bool
	V          node.PhotoView
	// Open is the step shown open, Reached the furthest the person got.
	Open, Reached int
	Live          bool
	// Watching: an Owner or Admin looking at someone else's migration.
	Watching bool
	Error    string
	// Owner names who to ask; IsOwner: the Owner is looking. MostMemory:
	// migrations may already use three quarters of the server's memory.
	Owner      string
	IsOwner    bool
	MostMemory bool
	// ImmichURL is Immich's own address; KeyURL its page for API keys.
	ImmichURL, KeyURL string
	Permissions       []string
	Source            photoSourceForm
	Problems          []string
	Progress          photoProgress
	Account           string
}

// photoSourceForm is step 3's form: what someone may choose, and what
// they chose.
type photoSourceForm struct {
	Where      string
	Drives     []photoDriveChoice
	DriveError string
	// Drive and Sub are the drive chosen and a folder on it; Label and
	// Counts say what's there; PickHref opens the folder picker.
	Drive, DriveName, Sub string
	Label, Counts         string
	PickHref, PlaceError  string
	Folder, FolderLabel   string
	FolderPickHref        string
	ServerName            string
	// Locations are the Owner's additional locations; Location the one
	// chosen and LocationSub a folder in it, with LocationFolders the
	// folders there.
	Locations       []store.MediaLibrary
	Location        string
	LocationSub     string
	LocationFolders []node.PhotoFolder
	LocationCrumbs  []mediaCrumb
	LocationError   string
	// Which choices this person has: drives, a folder on the server,
	// shares, locations.
	CanDrive, CanFolder, CanShare, CanLocation bool
	shareForm
}

// photoDriveChoice is a drive for step 3.
type photoDriveChoice struct {
	manager.PhotoDrive
	Why     string
	Checked bool
}

// photoProgress is a run's progress, for its meter.
type photoProgress struct {
	Percent  int
	Done     int64
	Total    int64
	TimeLeft string
}

// photoService is one of the lab's Immich services.
func (s *Server) photoService(ctx context.Context, name string) (manager.PhotoService, bool) {
	if s.Photos == nil {
		return manager.PhotoService{}, false
	}
	for _, ps := range s.Photos.PhotoServices(ctx) {
		if ps.Name == name {
			return ps, true
		}
	}
	return manager.PhotoService{}, false
}

// isAdmin: an Owner or Admin, who may migrate from anywhere.
func isAdmin(u *store.User) bool { return u != nil && auth.Can(u.Role, auth.ManageServices) }

// canMigrate says whether someone may migrate photos into a service: Owners
// and Admins always; others when the Owner allows everyone who uses Immich
// and they do (single sign-on's "Who can sign in" when it's on).
func (s *Server) canMigrate(ctx context.Context, u *store.User, service string) bool {
	if u == nil || s.Photos == nil {
		return false
	}
	if isAdmin(u) {
		return true
	}
	if s.Photos.PhotoSettings(ctx, service).OwnersOnly {
		return false
	}
	if s.SSO != nil {
		for _, c := range s.SSO.SSOClients() {
			if c.ID == service {
				ok, err := s.Store.CanUse(ctx, service, u.ID)
				return err == nil && ok
			}
		}
	}
	return true
}

// migratable are the Immich services someone may migrate photos into.
func (s *Server) migratable(ctx context.Context, u *store.User) []manager.PhotoService {
	if s.Photos == nil {
		return nil
	}
	var out []manager.PhotoService
	for _, ps := range s.Photos.PhotoServices(ctx) {
		if s.canMigrate(ctx, u, ps.Name) {
			out = append(out, ps)
		}
	}
	return out
}

// photosHome is /immich-photos: the one Immich someone may migrate into,
// or a choice of them, or who to ask.
func (s *Server) photosHome(w http.ResponseWriter, r *http.Request) {
	data := s.pageData(r)
	u := currentUser(r)
	all := []manager.PhotoService(nil)
	if s.Photos != nil {
		all = s.Photos.PhotoServices(r.Context())
	}
	if len(all) == 0 {
		http.NotFound(w, r)
		return
	}
	mine := s.migratable(r.Context(), u)
	if len(mine) == 1 {
		http.Redirect(w, r, "/immich-photos/"+url.PathEscape(mine[0].Name), http.StatusSeeOther)
		return
	}
	data["Own"], data["Choices"], data["Owner"] = true, mine, s.ownerName(r.Context())
	data["PhotoHosts"] = s.photoHosts(r.Context(), all)
	s.render(w, "photos-home", data)
}

// photoHosts are each Immich's address, by name, for the choice.
func (s *Server) photoHosts(ctx context.Context, list []manager.PhotoService) map[string]string {
	out := map[string]string{}
	views, err := s.Services(ctx)
	if err != nil {
		return out
	}
	for _, v := range views {
		for _, ps := range list {
			if v.Name == ps.Name && len(v.URLs) > 0 {
				out[ps.Name] = strings.TrimPrefix(strings.TrimPrefix(v.URLs[0], "https://"), "http://")
			}
		}
	}
	return out
}

// photosOwnPage is /immich-photos/<name>: the steps in a page of their own.
func (s *Server) photosOwnPage(w http.ResponseWriter, r *http.Request) {
	s.renderPhotos(w, r, false, "", http.StatusOK)
}

// photosPanelPage is /services/<name>/photos: the steps on Immich's page.
func (s *Server) photosPanelPage(w http.ResponseWriter, r *http.Request) {
	s.renderPhotos(w, r, true, "", http.StatusOK)
}

func (s *Server) renderPhotos(w http.ResponseWriter, r *http.Request, panel bool, errMsg string, status int) {
	name := r.PathValue("name")
	u := currentUser(r)
	ps, ok := s.photoService(r.Context(), name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := s.pageData(r)
	pg := &photoPage{Name: name, Title: ps.Title, Panel: panel, Admin: isAdmin(u), Error: errMsg,
		Owner: s.ownerName(r.Context()), IsOwner: u.Role == auth.RoleOwner, Permissions: photoimport.Permissions,
		MostMemory: s.Photos.PhotoSettings(r.Context(), name).Memory == photoimport.MemoryMost,
		Post:       "/immich-photos/" + url.PathEscape(name), Service: s.serviceRow(r.Context(), name, isAdmin(u))}
	pg.Base = pg.Post
	if panel {
		pg.Base = "/services/" + url.PathEscape(name) + "/photos"
	}
	if !s.canMigrate(r.Context(), u, name) {
		data["Own"], data["Photos"], data["Denied"] = !panel, pg, true
		s.renderPhotosPage(w, data, panel, http.StatusForbidden)
		return
	}
	x, err := s.Photos.PhotosOf(name)
	if msg, ok := usererr.Message(err); ok {
		pg.Error = msg
		data["Own"], data["Photos"], data["Unavailable"] = !panel, pg, true
		s.renderPhotosPage(w, data, panel, http.StatusOK)
		return
	} else if err != nil {
		s.serverError(w, err)
		return
	}
	who := u.Username
	// Owners and Admins can watch someone else's migration (Watch, on
	// Immich's page): it's theirs to stop or carry on, not to change.
	if other := r.URL.Query().Get("who"); other != "" && panel && pg.Admin && other != who {
		who, pg.Watching = other, true
	}
	v, err := x.PhotoMigration(r.Context(), name, who)
	if err != nil {
		s.serverError(w, err)
		return
	}
	pg.V = v
	pg.Reached = photoReached(v)
	pg.Open = pg.Reached
	if step, err := strconv.Atoi(r.URL.Query().Get("step")); err == nil && step >= photoStepFrom && step < pg.Reached &&
		pg.Reached < photoStepMigrate {
		pg.Open = step
	}
	if r.URL.Query().Has("where") && pg.Reached >= photoStepWhere && pg.Reached < photoStepMigrate {
		pg.Open = photoStepWhere // the picker, or another drive chosen
	}
	pg.Live = v.Scanning || v.Checking || (v.Run != nil && v.Run.Running)
	pg.ImmichURL = s.serviceURL(r.Context(), name)
	if pg.ImmichURL != "" {
		pg.KeyURL = strings.TrimSuffix(pg.ImmichURL, "/") + "/user-settings?isOpen=api-keys"
	}
	if v.Account != nil {
		pg.Account = cmp.Or(v.Account.Name, v.Account.Email)
	}
	pg.Problems = v.Problems()
	pg.Progress = photoProgressOf(v.Run)
	if pg.Open == photoStepWhere {
		if err := s.photoSource(r, pg, ps); err != nil {
			s.serverError(w, err)
			return
		}
	}
	data["Own"], data["Photos"] = !panel, pg
	if r.URL.Query().Get("part") == "live" {
		if !pg.Live {
			// Finished: the whole page, so the refreshing stops.
			w.Header().Set("HX-Refresh", "true")
			return
		}
		s.renderLive(w, r, "photos", "photos-steps", pg)
		return
	}
	s.renderPhotosPage(w, data, panel, status)
}

// renderPhotosPage renders the page, in the panel or a page of its own.
func (s *Server) renderPhotosPage(w http.ResponseWriter, data map[string]any, panel bool, status int) {
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	if pg, ok := data["Photos"].(*photoPage); ok && panel {
		data["Service"] = pg.Service // the sidebar marks Immich
	}
	s.render(w, "photos", data)
}

// serviceURL is a service's own address ("" if it has none).
func (s *Server) serviceURL(ctx context.Context, name string) string {
	if v, ok, _ := s.serviceByName(ctx, name); ok && len(v.URLs) > 0 {
		return v.URLs[0]
	}
	return ""
}

// photoReached is the furthest step a person's migration has reached.
func photoReached(v node.PhotoView) int {
	switch {
	case v.Run != nil && (v.Run.Running || !v.Run.Finished.IsZero() && (v.Run.Stopped || v.Run.Interrupted || v.Run.Err != "")):
		return photoStepMigrate
	case v.Run != nil && !v.Run.Finished.IsZero():
		return photoStepDone
	case v.Kind == "":
		return photoStepFrom
	case v.Kind == photoimport.KindGoogle && !v.Guided:
		return photoStepExport
	case v.Source == nil || v.Scanning || v.ScanErr != "" || v.Scan == nil:
		return photoStepWhere
	case !v.HasKey || v.Account == nil || len(v.Account.Missing) > 0:
		return photoStepKey
	case !v.ChoicesSet:
		return photoStepChoose
	}
	// The check holds Migrate them: it stays open until the run starts.
	return photoStepCheck
}

// photoProgressOf is a run's progress: how many of those found have
// reached an end, and a rough time left from the pace so far.
func photoProgressOf(run *node.PhotoRun) photoProgress {
	if run == nil {
		return photoProgress{}
	}
	t := run.Tally
	p := photoProgress{Done: t.Done(), Total: t.Found}
	if p.Total > 0 {
		p.Percent = int(min(100, p.Done*100/p.Total))
	}
	if run.Running && p.Done > 50 && p.Total > p.Done {
		spent := time.Since(run.Started)
		left := time.Duration(float64(spent) * float64(p.Total-p.Done) / float64(p.Done))
		p.TimeLeft = words.RoughDuration(left)
	}
	return p
}

// photoSource fills step 3: the places this person may choose.
func (s *Server) photoSource(r *http.Request, pg *photoPage, ps manager.PhotoService) error {
	ctx := r.Context()
	f := &pg.Source
	q := r.URL.Query()
	set := s.Photos.PhotoSettings(ctx, ps.Name)
	f.CanDrive, f.CanShare, f.CanFolder = pg.Admin || !set.NoDrives, pg.Admin || !set.NoShares, pg.Admin
	f.Locations = s.Photos.PhotoLocations(ctx, ps.Name)
	f.CanLocation = len(f.Locations) > 0
	src := pg.V.Source
	f.Where = q.Get("where")
	if !slices.Contains([]string{"drive", "folder", "share", "location"}, f.Where) {
		f.Where = ""
		if src != nil {
			f.Where = src.Kind
		}
	}
	allowed := map[string]bool{"drive": f.CanDrive, "folder": f.CanFolder, "share": f.CanShare, "location": f.CanLocation}
	if !allowed[f.Where] {
		f.Where = ""
		for _, w := range []string{"drive", "location", "share", "folder"} {
			if allowed[w] {
				f.Where = w
				break
			}
		}
	}
	f.Drive, f.Sub, f.Folder = q.Get("drive"), cleanSub(q.Get("sub")), q.Get("folder")
	f.Location, f.LocationSub = q.Get("location"), cleanSub(q.Get("lsub"))
	if q.Get("where") == "" && src != nil {
		switch src.Kind {
		case "drive":
			f.Drive, f.Sub = src.Library.Drive, src.Library.Sub
		case "folder":
			f.Folder = src.Library.Folder
		case "location":
			f.Location, f.LocationSub = src.Location, src.Sub
		}
		if sh := src.Library.Share; sh != nil {
			f.fillShare(sh)
		}
	}
	f.shareKind()
	if n := s.Manager.NodeFor(ps.Name); n != nil {
		f.ServerName = n.Identity().Name
	}
	// Drives, for those who may use one.
	if f.CanDrive {
		drives, err := s.Photos.PhotoDrives(ctx, ps.Name, pg.Admin)
		if msg, ok := usererr.Message(err); ok {
			f.DriveError = msg
		} else if err != nil {
			return err
		}
		for _, d := range drives {
			c := photoDriveChoice{PhotoDrive: d, Why: s.driveWhy(ctx, d.Why)}
			if d.UUID == f.Drive && d.Why == "" {
				c.Checked, f.DriveName = true, d.Name
			}
			f.Drives = append(f.Drives, c)
		}
		if f.DriveName == "" {
			f.Drive, f.Sub = "", ""
			for i, d := range f.Drives {
				if d.Why == "" {
					f.Drives[i].Checked, f.Drive, f.DriveName = true, d.UUID, d.Name
					break
				}
			}
		}
		if f.Drive != "" && f.Where == "drive" {
			got, err := s.mediaPlaceAt(ctx, ps.Host, mediaPlace{Drive: f.Drive, DriveName: f.DriveName, Sub: f.Sub})
			if err != nil {
				return err
			}
			f.Label, f.Counts, f.PlaceError = got.Label, got.Counts, got.Error
			f.PickHref = pg.Base + "/folders?" + url.Values{"drive": {f.Drive}, "name": {f.DriveName}, "sub": {f.Sub}}.Encode()
		}
	}
	if f.CanFolder && f.Where == "folder" {
		folder := ""
		if f.Folder != "" {
			folder = path.Clean("/" + f.Folder)
		}
		got, err := s.mediaPlaceAt(ctx, ps.Host, mediaPlace{Folder: folder})
		if err != nil {
			return err
		}
		f.Folder, f.FolderLabel = folder, got.Label
		f.FolderPickHref = pg.Base + "/folders?" + url.Values{"folder": {folder}}.Encode()
	}
	if f.CanLocation {
		if f.Location == "" || !slices.ContainsFunc(f.Locations, func(l store.MediaLibrary) bool { return l.Name == f.Location }) {
			f.Location, f.LocationSub = f.Locations[0].Name, ""
		}
		if f.Where == "location" {
			x, err := s.Photos.PhotosOf(ps.Name)
			if err != nil {
				return err
			}
			folders, err := x.PhotoFolders(ctx, f.Location, f.LocationSub)
			if msg, ok := usererr.Message(err); ok {
				f.LocationError = msg
			} else if err != nil {
				return err
			}
			f.LocationFolders = folders
			title := f.Location
			for _, l := range f.Locations {
				if l.Name == f.Location {
					title = l.Title
				}
			}
			at := func(sub string) string {
				return pg.Base + "?" + url.Values{"where": {"location"}, "location": {f.Location}, "lsub": {sub}}.Encode() + "#step-3"
			}
			f.LocationCrumbs = crumbsFor(title, f.LocationSub, at)
		}
	}
	return nil
}

// ShareFields are the share's fields, for share-fields.
func (f photoSourceForm) ShareFields() shareFields {
	return shareFields{shareForm: f.shareForm, ServerName: cmp.Or(f.ServerName, "the server"), Example: "photos or /volume1/photos",
		Kept: ", until the migration is done."}
}

// LocationHref opens a folder in the location chosen.
func (f photoSourceForm) LocationHref(base, folder string) string {
	return base + "?" + url.Values{"where": {"location"}, "location": {f.Location},
		"lsub": {strings.TrimPrefix(f.LocationSub+"/"+folder, "/")}}.Encode() + "#step-3"
}

// LocationTitle is the chosen location's title, and the folder in it.
func (f photoSourceForm) LocationTitle() string {
	var parts []string
	for _, c := range f.LocationCrumbs {
		parts = append(parts, c.Name)
	}
	return strings.Join(parts, " › ")
}

// driveWhy says why a drive can't be used, for people.
func (s *Server) driveWhy(ctx context.Context, why string) string {
	switch why {
	case "backups":
		return "Roostack's backups"
	case "unreadable":
		return "No filesystem Roostack can read"
	case "media":
		return "Holds the lab's media: " + s.ownerName(ctx) + " can set up a location on it for you"
	}
	return ""
}

// photoFoldersPage is the folder picker for step 3: on a drive or, for
// Owners and Admins, on the server.
func (s *Server) photoFoldersPage(w http.ResponseWriter, r *http.Request, panel bool) {
	name := r.PathValue("name")
	u := currentUser(r)
	ps, ok := s.photoService(r.Context(), name)
	if !ok || !s.canMigrate(r.Context(), u, name) {
		http.NotFound(w, r)
		return
	}
	data := s.pageData(r)
	base := "/immich-photos/" + url.PathEscape(name)
	if panel {
		base = "/services/" + url.PathEscape(name) + "/photos"
	}
	pl := pickerPlaceOf(r.URL.Query())
	if pl.Drive != "" {
		src := node.PhotoSource{Kind: "drive", Library: agent.Library{Drive: pl.Drive}}
		if err := s.Photos.CheckPhotoSource(r.Context(), name, src, isAdmin(u)); err != nil {
			http.NotFound(w, r)
			return
		}
	} else if !isAdmin(u) {
		http.NotFound(w, r)
		return
	}
	if pl.Folder == "/" {
		pl.Folder = "" // the places folders can be
	}
	p, err := s.pickFolders(r.Context(), ps.Host, pl, pickerLinks{at: func(set url.Values) string { return base + "/folders?" + set.Encode() },
		back: stepBack(base, "#step-3"), serverTitle: "Choose a folder", serverTop: "Folders"})
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Picker"], data["Own"], data["PickerBack"] = p, !panel, base
	s.render(w, "photos-folders", data)
}

// photoPost is a step's form: the service, the person and their
// migration, if they may.
func (s *Server) photoPost(w http.ResponseWriter, r *http.Request) (string, *store.User, node.Photos, bool) {
	name := r.PathValue("name")
	u := currentUser(r)
	if _, ok := s.photoService(r.Context(), name); !ok || !s.canMigrate(r.Context(), u, name) {
		http.NotFound(w, r)
		return "", nil, nil, false
	}
	x, err := s.Photos.PhotosOf(name)
	if msg, ok := usererr.Message(err); ok {
		s.renderPhotos(w, r, r.FormValue("panel") == "1", msg, http.StatusBadRequest)
		return "", nil, nil, false
	} else if err != nil {
		s.serverError(w, err)
		return "", nil, nil, false
	}
	return name, u, x, true
}

// photoDone ends a step's form: the page again with the error, or back to
// it at the next step.
func (s *Server) photoDone(w http.ResponseWriter, r *http.Request, err error, what, anchor string) {
	panel := r.FormValue("panel") == "1"
	if msg, ok := usererr.Message(err); ok {
		s.renderPhotos(w, r, panel, msg, http.StatusBadRequest)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	name := r.PathValue("name")
	if what != "" {
		s.Store.Audit(r.Context(), currentUser(r), "photos", name+": "+what)
	}
	base := "/immich-photos/" + url.PathEscape(name)
	if panel {
		base = "/services/" + url.PathEscape(name) + "/photos"
	}
	http.Redirect(w, r, base+anchor, http.StatusSeeOther)
}

func (s *Server) photoKindSubmit(w http.ResponseWriter, r *http.Request) {
	name, u, x, ok := s.photoPost(w, r)
	if !ok {
		return
	}
	err := x.SetPhotoKind(r.Context(), name, u.Username, r.FormValue("from"))
	s.photoDone(w, r, err, "", "#step-2")
}

func (s *Server) photoGuidedSubmit(w http.ResponseWriter, r *http.Request) {
	name, u, x, ok := s.photoPost(w, r)
	if !ok {
		return
	}
	err := x.SetPhotoGuided(r.Context(), name, u.Username)
	s.photoDone(w, r, err, "", "#step-3")
}

func (s *Server) photoSourceSubmit(w http.ResponseWriter, r *http.Request) {
	name, u, x, ok := s.photoPost(w, r)
	if !ok {
		return
	}
	r.ParseForm()
	src := node.PhotoSource{Kind: r.FormValue("where")}
	switch src.Kind {
	case "drive":
		src.Library = agent.Library{Drive: r.FormValue("drive"), Sub: cleanSub(r.FormValue("sub"))}
		src.Title = cmp.Or(r.FormValue("drive_name"), "The drive")
		if src.Library.Sub != "" {
			src.Title += " › " + strings.ReplaceAll(src.Library.Sub, "/", " › ")
		}
		if src.Library.Drive == "" {
			s.renderPhotos(w, r, r.FormValue("panel") == "1", "Choose the drive your export is on.", http.StatusBadRequest)
			return
		}
	case "folder":
		folder := r.FormValue("folder")
		if folder == "" {
			s.renderPhotos(w, r, r.FormValue("panel") == "1", "Choose the folder your export is in.", http.StatusBadRequest)
			return
		}
		src.Library = agent.Library{Folder: path.Clean("/" + folder)}
		src.Title = src.Library.Folder
	case "share":
		sh := sharePosted(r)
		src.Library = agent.Library{Share: sh}
		src.Title = sh.Server + ", share " + sh.Path
	case "location":
		src.Location, src.Sub = r.FormValue("location"), cleanSub(r.FormValue("lsub"))
		src.Title = r.FormValue("location_title")
	}
	if err := s.Photos.CheckPhotoSource(r.Context(), name, src, isAdmin(u)); err != nil {
		s.photoDone(w, r, err, "", "")
		return
	}
	if src.Kind == "location" {
		for _, l := range s.Photos.PhotoLocations(r.Context(), name) {
			if l.Name == src.Location {
				src.Title = l.Title
				if src.Sub != "" {
					src.Title += " › " + strings.ReplaceAll(src.Sub, "/", " › ")
				}
			}
		}
	}
	err := x.SetPhotoSource(r.Context(), name, u.Username, src)
	s.photoDone(w, r, err, "export at "+src.Title, "#step-3")
}

func (s *Server) photoKeySubmit(w http.ResponseWriter, r *http.Request) {
	name, u, x, ok := s.photoPost(w, r)
	if !ok {
		return
	}
	err := x.SetPhotoKey(r.Context(), name, u.Username, r.FormValue("key"))
	s.photoDone(w, r, err, "gave an Immich key", "#step-4")
}

func (s *Server) photoChoicesSubmit(w http.ResponseWriter, r *http.Request) {
	name, u, x, ok := s.photoPost(w, r)
	if !ok {
		return
	}
	on := func(k string) bool { return r.FormValue(k) == "on" }
	c := photoimport.Choices{Trash: on("trash"), Partner: on("partner"), Untitled: on("untitled"), People: on("people"),
		Stacks: on("stacks"), FolderAlbums: on("folder_albums")}
	err := x.SetPhotoChoices(r.Context(), name, u.Username, c)
	if err == nil {
		err = x.CheckPhotos(r.Context(), name, u.Username)
	}
	s.photoDone(w, r, err, "", "#step-6")
}

func (s *Server) photoCheckSubmit(w http.ResponseWriter, r *http.Request) {
	name, u, x, ok := s.photoPost(w, r)
	if !ok {
		return
	}
	err := x.CheckPhotos(r.Context(), name, u.Username)
	s.photoDone(w, r, err, "", "#step-6")
}

func (s *Server) photoStartSubmit(w http.ResponseWriter, r *http.Request) {
	name, u, x, ok := s.photoPost(w, r)
	if !ok {
		return
	}
	who := u.Username
	// Owners and Admins can carry on anyone's, from Immich's page.
	if other := r.FormValue("who"); other != "" && isAdmin(u) {
		who = other
	}
	err := x.StartPhotos(r.Context(), name, who)
	if err == nil && who == u.Username {
		s.Photos.MigrationStarted(r.Context(), name, u)
	}
	s.photoDone(w, r, err, "started migrating photos for "+who, "#step-7")
}

func (s *Server) photoStopSubmit(w http.ResponseWriter, r *http.Request) {
	name, u, x, ok := s.photoPost(w, r)
	if !ok {
		return
	}
	who := u.Username
	// Owners and Admins can stop anyone's, from Immich's page.
	if other := r.FormValue("who"); other != "" && isAdmin(u) {
		who = other
	}
	err := x.StopPhotos(r.Context(), name, who)
	s.photoDone(w, r, err, "stopped migrating photos for "+who, "#step-7")
}

func (s *Server) photoForgetSubmit(w http.ResponseWriter, r *http.Request) {
	name, u, x, ok := s.photoPost(w, r)
	if !ok {
		return
	}
	err := x.ForgetPhotos(r.Context(), name, u.Username)
	s.photoDone(w, r, err, "", "")
}

// photoSettingsSubmit keeps the Owner's choices, from Immich's card.
func (s *Server) photoSettingsSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.photoService(r.Context(), name); !ok {
		http.NotFound(w, r)
		return
	}
	set := manager.PhotoSettings{OwnersOnly: r.FormValue("who") == "admins", NoDrives: r.FormValue("drives") != "on",
		NoShares: r.FormValue("shares") != "on", Memory: r.FormValue("memory")}
	if _, ok := photoimport.MemoryShares[set.Memory]; !ok {
		set.Memory = photoimport.MemoryHalf
	}
	if err := s.Photos.SetPhotoSettings(r.Context(), name, set); err != nil {
		if msg, ok := usererr.Message(err); ok {
			http.Error(w, msg, http.StatusBadRequest)
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "photos", fmt.Sprintf("%s: who can migrate photos: %s", name,
		map[bool]string{true: "Owners and Admins", false: "everyone who uses it"}[set.OwnersOnly]))
	http.Redirect(w, r, photosSavedHref(name, "saved"), http.StatusSeeOther)
}

// photoLocationRemoveSubmit removes one of the Owner's locations.
func (s *Server) photoLocationRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.photoService(r.Context(), name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.Photos.RemovePhotoLocation(r.Context(), name, r.PathValue("location")); err != nil {
		if err == manager.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "photos", name+": removed the location "+r.PathValue("location"))
	http.Redirect(w, r, photosSavedHref(name, "removed"), http.StatusSeeOther)
}

// photoCard is the Migrate photos card and call to action on Immich's
// page.
type photoCard struct {
	Name, Title string
	Settings    manager.PhotoSettings
	Locations   []store.MediaLibrary
	List        []node.PhotoSummary
	// Running and Stopped are the migration to show at the top of the
	// page; Hidden: the call to action isn't shown (someone has migrated
	// photos, or the Owner hid it).
	Running, Stopped *node.PhotoSummary
	Hidden           bool
	Address          string
	Error            string
	CanSettings      bool
	// Saved says what the Owner just changed (?photos=, photoSaved).
	Saved string
	// Memory is what a migration may use with each choice, on Immich's
	// server.
	Memory photoMemory
	// Host is the server Immich runs on, where locations go; Me is who's
	// looking.
	Host, Me string
}

// photoMemory is the card's choice of memory for migrations.
type photoMemory struct {
	Half, Most int64
	// Photos is roughly how large a library half lets them migrate.
	Photos int64
}

// photoSaved is what the card says after the Owner changes it: its
// ?photos= after saving. A query, not only #photos, so that a form in a
// dialog on the same page (Add a location) reloads it.
var photoSaved = map[string]string{"saved": "Saved.", "added": "Location added.", "removed": "Location removed."}

// photosSavedHref is a service's page after the Owner changed its card.
func photosSavedHref(name, what string) string {
	return "/services/" + url.PathEscape(name) + "?photos=" + what + "#photos"
}

// photoCardFor is Immich's card, for its page ("" when the service isn't
// one photos can be migrated into).
func (s *Server) photoCardFor(ctx context.Context, u *store.User, name string) *photoCard {
	ps, ok := s.photoService(ctx, name)
	if !ok || !isAdmin(u) {
		return nil
	}
	c := &photoCard{Name: name, Title: ps.Title, Settings: s.Photos.PhotoSettings(ctx, name),
		Locations: s.Photos.PhotoLocations(ctx, name), CanSettings: u.Role == auth.RoleOwner,
		Address: "https://" + s.UIHost + "/immich-photos", Host: ps.Host, Me: u.Username}
	x, err := s.Photos.PhotosOf(name)
	if msg, ok := usererr.Message(err); ok {
		c.Error = msg
		return c
	} else if err != nil {
		s.Log.Error("reading photo migrations", "service", name, "err", err)
		return c
	}
	_, total := x.PhotoMemory(ctx, name)
	half := photoimport.Memory(total, photoimport.MemoryHalf)
	c.Memory = photoMemory{Half: half, Most: photoimport.Memory(total, photoimport.MemoryMost),
		Photos: half * photoimport.PhotosPerGB / 1e9 / 10_000 * 10_000}
	list, err := x.PhotoList(ctx, name)
	if err != nil {
		s.Log.Error("reading photo migrations", "service", name, "err", err)
	}
	c.List = list
	for i := range list {
		m := &list[i]
		switch {
		case m.Run != nil && m.Run.Running:
			c.Running = m
		case m.Run != nil && (m.Run.Stopped || m.Run.Interrupted) && c.Stopped == nil:
			c.Stopped = m
		}
		if m.Run != nil && !m.Run.Finished.IsZero() && !m.Run.Stopped && !m.Run.Interrupted && m.Run.Err == "" {
			c.Hidden = true
		}
	}
	if v, err := s.Store.Setting(ctx, "photo-cta-hidden."+name); err == nil && v == "yes" {
		c.Hidden = true
	}
	return c
}

// photoHideSubmit hides the call to action on Immich's page.
func (s *Server) photoHideSubmit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.photoService(r.Context(), name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.SetSetting(r.Context(), "photo-cta-hidden."+name, "yes"); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/services/"+url.PathEscape(name)+"#photos", http.StatusSeeOther)
}

// photoTotals words a scan's counts: "61,204 photos and 3,977 videos".
func photoTotals(photos, videos int64) string {
	switch {
	case videos == 0:
		return words.Count(photos, "photo", "photos")
	case photos == 0:
		return words.Count(videos, "video", "videos")
	}
	return words.Count(photos, "photo", "photos") + " and " + words.Count(videos, "video", "videos")
}

// Kinds are the places this person may choose, in order.
func (f photoSourceForm) Kinds() []string {
	var out []string
	for _, k := range []string{"drive", "location", "share", "folder"} {
		if map[string]bool{"drive": f.CanDrive, "location": f.CanLocation, "share": f.CanShare, "folder": f.CanFolder}[k] {
			out = append(out, k)
		}
	}
	return out
}

// Seg is a kind's panel number under the segmented control (0: not
// offered).
func (f photoSourceForm) Seg(kind string) int {
	return slices.Index(f.Kinds(), kind) + 1
}

// Control is the segmented control choosing where, when there's more than
// one place to choose from.
func (f photoSourceForm) Control(admin bool) (template.HTML, error) {
	kinds := f.Kinds()
	if len(kinds) < 2 {
		return "", nil
	}
	var args []any
	for _, k := range kinds {
		var c segmentChoice
		var err error
		switch k {
		case "drive":
			c, err = segChoice("drive", map[bool]string{true: "Drive", false: "Your drive"}[admin], "disk")
		case "location":
			c, err = segChoice("location", "Other locations", "folder")
		case "share":
			c, err = segChoice("share", "Network share", "network")
		case "folder":
			c, err = segChoice("folder", "Folder on "+cmp.Or(f.ServerName, "the server"), "server")
		}
		if err != nil {
			return "", err
		}
		args = append(args, c)
	}
	return segmented("where", "Where the export is", f.Where, args...)
}

// serviceRow is a service's row as the dashboard has it (its title,
// status and icon, with its logo for those who may see it: withIcons), or
// just its name.
func (s *Server) serviceRow(ctx context.Context, name string, logos bool) ServiceView {
	if svc, ok, _ := s.serviceByName(ctx, name); ok {
		one := []ServiceView{svc}
		s.withIcons(one, logos)
		return one[0]
	}
	return ServiceView{Name: name, Title: name, DrawnIcon: categoryIcon(nil)}
}

// migrationResult is a migration's result, for the card's list.
func migrationResult(m node.PhotoSummary) string {
	r := m.Run
	switch {
	case r == nil:
		return "Getting ready"
	case r.Running:
		return "Running"
	case r.Err != "":
		return "Stopped with an error"
	case r.Stopped:
		return "Stopped"
	case r.Interrupted:
		return "Stopped when the server restarted"
	}
	return photoTotals(r.Migrated(), 0) + " migrated"
}

// libraryWhereText says where a location is, in words.
func libraryWhereText(l store.MediaLibrary) string {
	switch {
	case l.ShareType != "":
		return "A share on " + l.ShareServer + ", with your login"
	case l.Drive != "":
		return strings.TrimSuffix(cmp.Or(l.DriveName, "A drive")+" › "+strings.ReplaceAll(l.Sub, "/", " › "), " › ")
	}
	return l.Folder
}

// choicesText says what someone chose to migrate, for step 5's line.
func choicesText(kind string, c photoimport.Choices) string {
	if kind == photoimport.KindFolder {
		var parts []string
		if c.FolderAlbums {
			parts = append(parts, "each folder as an album")
		}
		if c.Stacks {
			parts = append(parts, "stacks")
		}
		if len(parts) == 0 {
			return "The photos alone"
		}
		return words.Capitalise(strings.Join(parts, ", "))
	}
	d := photoimport.DefaultChoices()
	d.FolderAlbums = c.FolderAlbums // a folder's only
	if c == d {
		return "Roostack's suggestions"
	}
	var with, without []string
	for _, x := range []struct {
		on   bool
		what string
	}{{c.Trash, "the trash"}, {c.Partner, "a partner's photos"}, {c.Untitled, "albums without a name"}, {c.People, "people as tags"}, {c.Stacks, "stacks"}} {
		if x.on {
			with = append(with, x.what)
		} else {
			without = append(without, x.what)
		}
	}
	switch {
	case len(with) == 0:
		return "None of the extras"
	case len(without) == 0:
		return "Everything"
	}
	return words.Capitalise(strings.Join(with, ", ")) + "; not " + strings.Join(without, ", ")
}

// leftOutText says what a Takeout's choices leave out, with counts: "1,203
// in Google's trash, 41 a partner shared with you".
func leftOutText(s *photoimport.Scan, c photoimport.Choices) string {
	var parts []string
	if !c.Trash && s.Trashed > 0 {
		parts = append(parts, units.Count(s.Trashed)+" in Google's trash")
	}
	if !c.Partner && s.Partner > 0 {
		parts = append(parts, units.Count(s.Partner)+" a partner shared with you")
	}
	if !c.Untitled && s.Untitled > 0 {
		parts = append(parts, words.Count(int64(s.Untitled), "album without a name", "albums without a name"))
	}
	return strings.Join(parts, ", ")
}
