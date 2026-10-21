package web

import (
	"context"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/manager"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/photoimport"
	"roostack.cloud/roostack/internal/store"
)

// fakePhotos is the manager's and Immich's server's side of migrating
// photos: the Owner's choices, and each person's migration.
type fakePhotos struct {
	settings  manager.PhotoSettings
	locations []store.MediaLibrary
	people    map[string]*node.PhotoState
	drives    []manager.PhotoDrive
	// checked records each source checked, and whether as an Admin.
	checked []bool
	started []string
}

func newFakePhotos() *fakePhotos {
	return &fakePhotos{people: map[string]*node.PhotoState{}, drives: []manager.PhotoDrive{
		{Drive: agent.Drive{UUID: "1111-2222", Name: "T7 Shield", Kind: "usb", Size: 2e12, Free: 1.4e12, Usable: true}},
		{Drive: agent.Drive{UUID: "3333-4444", Name: "Backups", Kind: "usb", Usable: true, Backup: true}, Why: "backups"},
	}}
}

func (f *fakePhotos) PhotoServices(context.Context) []manager.PhotoService {
	return []manager.PhotoService{{Name: "immich", Title: "Immich", Host: "nuc"}}
}
func (f *fakePhotos) PhotosOf(string) (node.Photos, error) { return f, nil }
func (f *fakePhotos) PhotoSettings(context.Context, string) manager.PhotoSettings {
	return f.settings
}
func (f *fakePhotos) SetPhotoSettings(_ context.Context, _ string, s manager.PhotoSettings) error {
	f.settings = s
	return nil
}
func (f *fakePhotos) PhotoLocations(context.Context, string) []store.MediaLibrary { return f.locations }
func (f *fakePhotos) AddPhotoLocation(_ context.Context, service string, req manager.LibraryRequest) error {
	f.locations = append(f.locations, store.MediaLibrary{Name: manager.LibraryName(req.Title), Title: req.Title, PhotosFor: service})
	return nil
}
func (f *fakePhotos) RemovePhotoLocation(_ context.Context, _, name string) error {
	for i, l := range f.locations {
		if l.Name == name {
			f.locations = append(f.locations[:i], f.locations[i+1:]...)
			return nil
		}
	}
	return manager.ErrNotFound
}
func (f *fakePhotos) PhotoDrives(_ context.Context, _ string, admin bool) ([]manager.PhotoDrive, error) {
	out := append([]manager.PhotoDrive(nil), f.drives...)
	if !admin {
		out = append(out, manager.PhotoDrive{Drive: agent.Drive{UUID: "5555", Name: "Media", Kind: "disk", Usable: true}, Why: "media"})
	}
	return out, nil
}
func (f *fakePhotos) CheckPhotoSource(_ context.Context, _ string, src node.PhotoSource, admin bool) error {
	f.checked = append(f.checked, admin)
	if src.Kind == "folder" && !admin {
		return &node.FormError{Msg: "Only Owners and Admins can choose a folder on the server."}
	}
	return nil
}

// node.Photos.
func (f *fakePhotos) st(who string) *node.PhotoState {
	if f.people[who] == nil {
		f.people[who] = &node.PhotoState{Who: who, Choices: photoimport.DefaultChoices()}
	}
	return f.people[who]
}
func (f *fakePhotos) PhotoMigration(_ context.Context, _, who string) (node.PhotoView, error) {
	return node.PhotoView{PhotoState: *f.st(who), Ready: true}, nil
}
func (f *fakePhotos) PhotoList(context.Context, string) ([]node.PhotoSummary, error) {
	var out []node.PhotoSummary
	for who, st := range f.people {
		out = append(out, node.PhotoSummary{Service: "immich", Who: who, Kind: st.Kind, Run: st.Run})
	}
	return out, nil
}
func (f *fakePhotos) SetPhotoKind(_ context.Context, _, who, kind string) error {
	f.st(who).Kind = kind
	return nil
}
func (f *fakePhotos) SetPhotoGuided(_ context.Context, _, who string) error {
	f.st(who).Guided = true
	return nil
}
func (f *fakePhotos) SetPhotoSource(_ context.Context, _, who string, src node.PhotoSource) error {
	st := f.st(who)
	st.Source = &src
	st.Scan = &photoimport.Scan{Kind: st.Kind, Parts: []photoimport.Part{{Name: "takeout-20261019T091234Z-001.zip", Number: 1}},
		Photos: 61204, Videos: 3977, Bytes: 395e9, Albums: 214, Untitled: 3, Trashed: 1203, Partner: 41,
		First: time.Date(2009, 3, 1, 0, 0, 0, 0, time.UTC), Last: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	return nil
}
func (f *fakePhotos) SetPhotoKey(_ context.Context, _, who, key string) error {
	st := f.st(who)
	st.HasKey = true
	st.Account = &node.PhotoAccount{ID: "a1", Name: "Anna Smith", Email: "anna@example.com", Photos: 8912}
	if key == "too-narrow" {
		st.Account.Missing = []string{"album.create", "stack.create"}
	}
	return nil
}
func (f *fakePhotos) SetPhotoChoices(_ context.Context, _, who string, c photoimport.Choices) error {
	f.st(who).Choices, f.st(who).ChoicesSet = c, true
	return nil
}
func (f *fakePhotos) CheckPhotos(_ context.Context, _, who string) error {
	st := f.st(who)
	st.Check = &node.PhotoCheck{At: time.Now(), Needs: 435e9, Free: 1.2e12, Disk: "/srv", Account: *st.Account}
	return nil
}
func (f *fakePhotos) StartPhotos(_ context.Context, _, who string) error {
	f.started = append(f.started, who)
	st := f.st(who)
	st.Run = &node.PhotoRun{Started: time.Now().Add(-time.Hour), Running: true, Before: *st.Account,
		Tally: photoimport.Tally{Found: 52292, Sent: 12000, Had: 34, SentBytes: 98e9}}
	return nil
}
func (f *fakePhotos) StopPhotos(_ context.Context, _, who string) error {
	if r := f.st(who).Run; r != nil {
		r.Running, r.Stopped, r.Finished = false, true, time.Now()
	}
	return nil
}
func (f *fakePhotos) ForgetPhotos(_ context.Context, _, who string) error {
	delete(f.people, who)
	return nil
}
func (f *fakePhotos) PhotoFolders(context.Context, string, string) ([]node.PhotoFolder, error) {
	return []node.PhotoFolder{{Name: "Ella", Zips: 3}}, nil
}

func immichServices(context.Context) ([]ServiceView, error) {
	return []ServiceView{{Name: "immich", Title: "Immich", Status: "running", URLs: []string{"https://photos.lab.example.com"}}}, nil
}

// An Owner migrates Google Photos into Immich from its page, step by step:
// each step continues to the next, done ones shrink to a line with Change,
// and the summary comes last.
func TestPhotoSteps(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	h.server.Log = slog.New(slog.NewTextHandler(testWriter{t}, nil))
	f := newFakePhotos()
	h.server.Photos, h.server.Services = f, immichServices
	h.server.Libraries = newFakeLibraries()
	base := "/services/immich/photos"
	post := func(step string, form url.Values) (int, string) {
		t.Helper()
		form.Set("panel", "1")
		return h.post("/immich-photos/immich/"+step, form, h.srv.URL)
	}

	// 1: where from.
	code, body := h.get(base)
	if code != http.StatusOK || !strings.Contains(body, `id="step-1"`) || !strings.Contains(body, "Where are your photos now?") {
		t.Fatalf("the page: %d\n%s", code, body)
	}
	post("from", url.Values{"from": {"google"}})
	if f.st("u").Kind != photoimport.KindGoogle {
		t.Fatalf("kind %q", f.st("u").Kind)
	}

	// 2: Google's Takeout, with its warning that it's slow.
	_, body = h.get(base)
	if !strings.Contains(body, "This can be very slow, so it may take you several days.") || !strings.Contains(body, "takeout.google.com") {
		t.Error("step 2 doesn't guide the Takeout, or warn it's slow")
	}
	post("export", url.Values{})

	// 3: where the zips are: drives, the backups' drive greyed.
	_, body = h.get(base)
	if !strings.Contains(body, `id="step-3"`) || !strings.Contains(body, "T7 Shield") || !strings.Contains(body, "Roostack&#39;s backups") ||
		!strings.Contains(body, "Folder on") {
		t.Errorf("step 3:\n%s", body)
	}
	post("where", url.Values{"where": {"drive"}, "drive": {"1111-2222"}, "drive_name": {"T7 Shield"}, "sub": {"Takeout"}})
	if src := f.st("u").Source; src == nil || src.Title != "T7 Shield › Takeout" || len(f.checked) == 0 || !f.checked[len(f.checked)-1] {
		t.Fatalf("source %+v, checked as an Admin %v", src, f.checked)
	}

	// 4: the key: a link to Immich's page for keys, and missing
	// permissions named.
	_, body = h.get(base)
	if !strings.Contains(body, "https://photos.lab.example.com/user-settings?isOpen=api-keys") || !strings.Contains(body, "albumAsset.create") {
		t.Errorf("step 4 doesn't link to Immich's keys or list the permissions")
	}
	post("key", url.Values{"key": {"too-narrow"}})
	_, body = h.get(base)
	if !strings.Contains(body, "2 permissions are missing:") || !strings.Contains(body, "<code>stack.create</code>") {
		t.Errorf("missing permissions aren't named:\n%s", body)
	}
	post("key", url.Values{"key": {"fine"}})

	// 5: what to migrate, with the quick look's counts.
	_, body = h.get(base)
	if !strings.Contains(body, `id="step-5"`) || !strings.Contains(body, "1,203. They'd be archived in Immich.") {
		t.Errorf("step 5:\n%s", body)
	}
	post("choices", url.Values{"people": {"on"}, "stacks": {"on"}})
	if !f.st("u").ChoicesSet || f.st("u").Check == nil {
		t.Fatal("choices weren't kept, or the check didn't start")
	}

	// 6: the check.
	_, body = h.get(base)
	for _, want := range []string{"61,204", "Room for them:", "Left out, as you chose:</strong> 1,203 in Google&#39;s trash, 41 a partner shared with you, 3 albums without a name",
		"Your next backup will be about 395.0 GB larger.", ">Migrate them</button>"} {
		if !strings.Contains(body, want) {
			t.Errorf("the check lacks %q", want)
		}
	}

	// 7: migrating, with its progress and Stop.
	post("start", url.Values{})
	if len(f.started) != 1 || f.started[0] != "u" {
		t.Fatalf("started %v", f.started)
	}
	_, body = h.get(base)
	if !strings.Contains(body, "Migrating photos…") || !strings.Contains(body, "12,034 of 52,292") && !strings.Contains(body, "of 52,292") ||
		!strings.Contains(body, ">Stop</button>") || strings.Contains(body, ">Change<") {
		t.Errorf("step 7:\n%s", body)
	}
	_, live := h.get(base + "?part=live")
	if !strings.Contains(live, "Migrating photos…") {
		t.Error("the live part doesn't show the run")
	}
	post("stop", url.Values{})
	_, body = h.get(base)
	if !strings.Contains(body, ">Carry on</button>") || !strings.Contains(body, "nothing comes twice") {
		t.Errorf("stopped:\n%s", body)
	}

	// Done: the summary.
	r := f.st("u").Run
	r.Stopped, r.Finished = false, time.Now()
	r.After = node.PhotoAccount{ID: "a1", Photos: 8912 + 12034}
	r.Tally.Sent, r.Tally.Failed = 12034, 2
	r.Tally.Errors = []photoimport.Problem{{File: "a.zip:IMG_1.psd", Why: "unsupported"}}
	f.st("u").HasKey = false
	_, body = h.get(base)
	for _, want := range []string{"Migrated</h2>", "12,034 photos migrated into Anna Smith's account.", "Immich counts 12,034 more",
		"Already there, skipped:</strong> 34", "2 couldn't be migrated.", "IMG_1.psd", "Roostack has forgotten your key.", "Migrate more photos…"} {
		if !strings.Contains(body, want) {
			t.Errorf("the summary lacks %q", want)
		}
	}
}

// Immich's page: the call to action at the top, and the card with the
// Owner's choices.
func TestPhotoCard(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleOwner)
	f := newFakePhotos()
	h.server.Photos, h.server.Services = f, immichServices
	_, body := h.get("/services/immich")
	for _, want := range []string{"Moving from Google Photos or iCloud?", `href="/services/immich/photos">Migrate photos…`, `id="photos"`,
		"Who can migrate photos", "You haven't defined any additional locations that people can migrate photos from.",
		`href="/settings/media/add?server=nuc&amp;photos=immich">Add a location`, "/immich-photos"} {
		if !strings.Contains(body, want) {
			t.Errorf("Immich's page lacks %q", want)
		}
	}
	h.post("/services/immich/photos/settings", url.Values{"who": {"everyone"}, "drives": {"on"}}, h.srv.URL)
	if f.settings.OwnersOnly || f.settings.NoDrives || !f.settings.NoShares {
		t.Errorf("settings %+v", f.settings)
	}
	f.locations = []store.MediaLibrary{{Name: "takeouts", Title: "Family NAS › Takeouts", ShareType: "smb", ShareServer: "nas.home", PhotosFor: "immich"}}
	_, body = h.get("/services/immich")
	if !strings.Contains(body, "Family NAS › Takeouts") || strings.Contains(body, "You haven't defined") {
		t.Error("a location isn't listed")
	}
	h.post("/services/immich/photos/locations/takeouts/remove", url.Values{}, h.srv.URL)
	if len(f.locations) != 0 {
		t.Error("the location wasn't removed")
	}
	// A migration running shows at the top, instead of the call to action.
	f.st("anna").Kind = photoimport.KindGoogle
	f.st("anna").Run = &node.PhotoRun{Started: time.Now(), Running: true, Tally: photoimport.Tally{Found: 100, Sent: 25}}
	_, body = h.get("/services/immich")
	if !strings.Contains(body, "anna is migrating photos from Google") || strings.Contains(body, "Moving from Google Photos or iCloud?") {
		t.Error("a running migration isn't at the top")
	}
	h.post("/services/immich/photos/hide", url.Values{}, h.srv.URL)
	f.people = map[string]*node.PhotoState{}
	_, body = h.get("/services/immich")
	if strings.Contains(body, "Moving from Google Photos or iCloud?") {
		t.Error("Hide didn't hide the call to action")
	}
}

// Everyone else the Owner allows migrates on a page of its own, from only
// the places the Owner allows; the Owner can close it to them.
func TestPhotosForMembers(t *testing.T) {
	h, _ := loggedIn(t, auth.RoleMember)
	f := newFakePhotos()
	h.server.Photos, h.server.Services = f, immichServices
	h.server.Libraries = newFakeLibraries()

	// Their dashboard links to it, and /immich-photos goes to the one
	// Immich.
	_, body := h.get("/")
	if !strings.Contains(body, `href="/immich-photos/immich">Migrate your photos into it`) {
		t.Error("the dashboard doesn't link members to migrating their photos")
	}
	resp := h.poll("/immich-photos")
	if resp.Request.URL.Path != "/immich-photos/immich" {
		t.Errorf("/immich-photos went to %s", resp.Request.URL.Path)
	}

	// A page of its own: no control panel.
	code, body := h.get("/immich-photos/immich")
	if code != http.StatusOK || !strings.Contains(body, "Migrate your photos into Immich") || strings.Contains(body, `class="sidebar"`) ||
		!strings.Contains(body, "own-header") {
		t.Fatalf("the page of its own: %d\n%s", code, body)
	}
	h.post("/immich-photos/immich/from", url.Values{"from": {"google"}}, h.srv.URL)
	h.post("/immich-photos/immich/export", url.Values{}, h.srv.URL)
	_, body = h.get("/immich-photos/immich")
	if !strings.Contains(body, "Your drive") || strings.Contains(body, "Folder on") || !strings.Contains(body, "Holds the lab&#39;s media") {
		t.Errorf("step 3 for a member:\n%s", body)
	}
	code, body = h.post("/immich-photos/immich/where", url.Values{"where": {"folder"}, "folder": {"/srv/x"}}, h.srv.URL)
	if code != http.StatusBadRequest || !strings.Contains(html.UnescapeString(body), "Only Owners and Admins can choose a folder") || f.checked[len(f.checked)-1] {
		t.Errorf("a member's folder on the server: %d", code)
	}
	// The Owner's settings page isn't theirs.
	if code, _ := h.post("/services/immich/photos/settings", url.Values{"who": {"admins"}}, h.srv.URL); code == http.StatusOK && f.settings.OwnersOnly {
		t.Error("a member changed the Owner's choices")
	}

	// Closed to them: who to ask.
	f.settings.OwnersOnly = true
	code, body = h.get("/immich-photos/immich")
	if code != http.StatusForbidden || !strings.Contains(body, "Ask ") {
		t.Errorf("closed: %d", code)
	}
	if code, _ := h.post("/immich-photos/immich/start", url.Values{}, h.srv.URL); code != http.StatusNotFound {
		t.Errorf("starting when closed: %d", code)
	}
}

func TestPhotoReached(t *testing.T) {
	st := node.PhotoState{}
	v := func() int { return photoReached(node.PhotoView{PhotoState: st}) }
	steps := []struct {
		change func()
		want   int
	}{
		{func() {}, photoStepFrom},
		{func() { st.Kind = photoimport.KindGoogle }, photoStepExport},
		{func() { st.Guided = true }, photoStepWhere},
		{func() { st.Source = &node.PhotoSource{}; st.Scanning = true }, photoStepWhere},
		{func() {
			st.Scanning = false
			st.Scan = &photoimport.Scan{Kind: "google", Parts: []photoimport.Part{{Number: 1}}, Photos: 1}
		}, photoStepKey},
		{func() { st.HasKey = true; st.Account = &node.PhotoAccount{Missing: []string{"x"}} }, photoStepKey},
		{func() { st.Account.Missing = nil }, photoStepChoose},
		{func() { st.ChoicesSet = true }, photoStepCheck},
		{func() { st.Check = &node.PhotoCheck{Needs: 10, Free: 5} }, photoStepCheck}, // no room
		{func() { st.Check.Free = 100 }, photoStepCheck},                             // Migrate them is there
		{func() { st.Run = &node.PhotoRun{Running: true} }, photoStepMigrate},
		{func() { st.Run.Running, st.Run.Finished = false, time.Now() }, photoStepDone},
		{func() { st.Run.Interrupted = true }, photoStepMigrate},
	}
	for i, s := range steps {
		s.change()
		if got := v(); got != s.want {
			t.Errorf("%d: reached %d, want %d", i, got, s.want)
		}
	}
}
