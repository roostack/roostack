package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/activity"
	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/photoimport"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/units"
)

// Migrating photos into Immich from other platforms (SPEC.md §8.5): the
// server Immich runs on keeps each person's steps (where their export is,
// their key, their choices) in its runtime settings until the migration is
// done or forgotten, so someone waiting days for Google comes back to
// where they were. It looks in the export itself (photoimport.ScanFS),
// checks the key with Immich from inside Immich's own container, as hooks
// do, and runs immich-go in a container of its own on Immich's network, in
// a transient unit of the roostack user's that outlives Roostack
// restarting; it reads immich-go's log as it goes. One migration runs on a
// server at a time.

// Photos is migrating photos into a server's Immich. who is the Roostack
// account doing it: each person has their own steps.
type Photos interface {
	// PhotoMigration is a person's migration into a service, as its page
	// shows it.
	PhotoMigration(ctx context.Context, service, who string) (PhotoView, error)
	// PhotoList is every person's migration into a service, for its card.
	PhotoList(ctx context.Context, service string) ([]PhotoSummary, error)
	SetPhotoKind(ctx context.Context, service, who, kind string) error
	// SetPhotoGuided: the person has their export (step 2).
	SetPhotoGuided(ctx context.Context, service, who string) error
	// SetPhotoSource mounts where the export is, read-only, and has a
	// quick look in it, in the background.
	SetPhotoSource(ctx context.Context, service, who string, src PhotoSource) error
	// SetPhotoKey checks a key with Immich and keeps it for the
	// migration.
	SetPhotoKey(ctx context.Context, service, who, key string) error
	SetPhotoChoices(ctx context.Context, service, who string, c photoimport.Choices) error
	// CheckPhotos looks in the export again and at Immich's account and
	// disk, in the background.
	CheckPhotos(ctx context.Context, service, who string) error
	// StartPhotos starts (or carries on) the migration; StopPhotos stops
	// it, keeping what's in.
	StartPhotos(ctx context.Context, service, who string) error
	StopPhotos(ctx context.Context, service, who string) error
	// ForgetPhotos forgets a person's steps (and their key), unmounting
	// their export.
	ForgetPhotos(ctx context.Context, service, who string) error
	// PhotoFolders lists the folders in a folder of one of the Owner's
	// locations (sub "" for its top), with how many zip files each holds.
	PhotoFolders(ctx context.Context, location, sub string) ([]PhotoFolder, error)
}

// PhotoFolder is a folder in a location.
type PhotoFolder struct {
	Name string `json:"name"`
	Zips int    `json:"zips"`
}

// PhotoAgent is what roostack-agent does for a migration: mounting a
// person's export read-only (agent/photos.go).
type PhotoAgent interface {
	MountPhotos(ctx context.Context, service, who string, l agent.Library) (string, error)
	UnmountPhotos(ctx context.Context, service, who string) error
}

// PhotoUnits runs immich-go detached, as the roostack user, in a unit that
// outlives Roostack (systemd.Manager).
type PhotoUnits interface {
	RunDetached(ctx context.Context, unit string, name string, args ...string) error
	Exited(ctx context.Context, unit string) (running bool, code int, found bool, err error)
	Stop(ctx context.Context, units ...string) error
	Forget(ctx context.Context, unit string) error
}

// PhotoSource is where a person's export is.
type PhotoSource struct {
	// Kind is drive, folder or share (mounted for the migration), or
	// location: one of the Owner's additional locations, a media library
	// kept for migrations (Location), and a folder in it (Sub).
	Kind     string        `json:"kind"`
	Library  agent.Library `json:"library,omitzero"`
	Location string        `json:"location,omitempty"`
	Sub      string        `json:"sub,omitempty"`
	// Title says where, for people: "T7 Shield › Takeout".
	Title string `json:"title"`
}

// PhotoAccount is the Immich account a key belongs to.
type PhotoAccount struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	// Missing are permissions the key lacks; All: it may do everything.
	Missing []string `json:"missing,omitempty"`
	All     bool     `json:"all,omitempty"`
	// Photos and Videos are in the account now.
	Photos int64 `json:"photos"`
	Videos int64 `json:"videos"`
}

// PhotoCheck is the check before migrating (step 6).
type PhotoCheck struct {
	At time.Time `json:"at"`
	// Needs is the room it needs on the library's disk (the photos plus a
	// tenth for Immich's thumbnails), Free what's free there now.
	Needs int64 `json:"needs"`
	Free  int64 `json:"free"`
	// Disk names the library's disk: its mount point.
	Disk    string       `json:"disk"`
	Account PhotoAccount `json:"account"`
}

// PhotoRun is a migration that ran, or runs.
type PhotoRun struct {
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	Running  bool      `json:"running,omitempty"`
	// Stopped: the person stopped it; Interrupted: it stopped by itself
	// (Roostack or the server restarted); Err says what went wrong.
	Stopped     bool   `json:"stopped,omitempty"`
	Interrupted bool   `json:"interrupted,omitempty"`
	Err         string `json:"err,omitempty"`
	// Building: the migration tool's image is being built first.
	Building bool              `json:"building,omitempty"`
	Tally    photoimport.Tally `json:"tally"`
	// Before and After are the account's counts; LibraryBefore and
	// LibraryAfter the library folder's size, so the summary says what
	// Immich itself counts.
	Before        PhotoAccount `json:"before"`
	After         PhotoAccount `json:"after,omitzero"`
	LibraryBefore int64        `json:"library_before"`
	LibraryAfter  int64        `json:"library_after,omitempty"`
	FreeAfter     int64        `json:"free_after,omitempty"`
	// Passes counts runs: a carried-on migration runs again, its Tally
	// afresh. Earlier is what the passes before sent, and FirstStarted
	// when the first began, so the summary adds them up (what Immich had
	// before the migration is Tally.Had less Earlier).
	Passes       int       `json:"passes"`
	Earlier      int64     `json:"earlier,omitempty"`
	FirstStarted time.Time `json:"first_started,omitzero"`
}

// Migrated is how many photos and videos the migration sent, every pass.
func (r *PhotoRun) Migrated() int64 { return r.Earlier + r.Tally.Sent + r.Tally.Upgraded }

// HadBefore is how many Immich had already when the migration began.
func (r *PhotoRun) HadBefore() int64 { return max(0, r.Tally.Had-r.Earlier) }

// Began is when the migration began, its first pass.
func (r *PhotoRun) Began() time.Time {
	if !r.FirstStarted.IsZero() {
		return r.FirstStarted
	}
	return r.Started
}

// PhotoState is a person's migration into a service, kept until it's done
// and dismissed, or forgotten.
type PhotoState struct {
	Who  string `json:"who"`
	Kind string `json:"kind,omitempty"`
	// Guided: they have their export (step 2).
	Guided bool              `json:"guided,omitempty"`
	Source *PhotoSource      `json:"source,omitempty"`
	Scan   *photoimport.Scan `json:"scan,omitempty"`
	// Scanning while the quick look runs; ScanErr if it couldn't.
	Scanning bool          `json:"scanning,omitempty"`
	ScanErr  string        `json:"scan_err,omitempty"`
	Account  *PhotoAccount `json:"account,omitempty"`
	// HasKey: a key is kept for them (a Podman secret).
	HasKey     bool                `json:"has_key,omitempty"`
	Choices    photoimport.Choices `json:"choices"`
	ChoicesSet bool                `json:"choices_set,omitempty"`
	Check      *PhotoCheck         `json:"check,omitempty"`
	Checking   bool                `json:"checking,omitempty"`
	CheckErr   string              `json:"check_err,omitempty"`
	Run        *PhotoRun           `json:"run,omitempty"`
	Updated    time.Time           `json:"updated"`
}

// Problems are what stops the migration, from the check: each a sentence
// saying what to do.
func (st PhotoState) Problems() []string {
	var out []string
	s := st.Scan
	if s == nil {
		return nil
	}
	if st.Kind == photoimport.KindGoogle {
		if len(s.Parts) == 0 {
			if len(s.Tgz) > 0 {
				out = append(out, "These are .tgz files, which can't be read: export again from Google choosing .zip, or unpack them first.")
			} else {
				out = append(out, "There are no Takeout zip files in this folder: choose the folder you copied them into.")
			}
		}
		for _, n := range s.Missing {
			out = append(out, fmt.Sprintf("Part %d isn't here: download it from Google's email (each part can be downloaded 5 times), copy it into the folder, and check again.", n))
		}
		for _, n := range s.Unreadable {
			out = append(out, fmt.Sprintf("%s can't be opened: it may not have finished downloading. Download it again, then check again.", n))
		}
	}
	if s.Media() == 0 && len(out) == 0 {
		out = append(out, "There are no photos or videos here: choose the folder they're in.")
	}
	if c := st.Check; c != nil && c.Free >= 0 && c.Needs > c.Free {
		out = append(out, fmt.Sprintf("There isn't room: about %s is needed on %s, which has %s free. Free some space first.",
			units.Bytes(c.Needs), c.Disk, units.Bytes(c.Free)))
	}
	return out
}

// PhotoView is a person's migration as its page shows it.
type PhotoView struct {
	PhotoState
	// Busy is someone else's migration running on this server (whose,
	// into which service), when there is one.
	Busy *PhotoSummary `json:"busy,omitempty"`
	// Ready: an image of the migration tool is there already.
	Ready bool `json:"ready"`
	// Log is the end of the running or last run's log, for Details.
	Log string `json:"log,omitempty"`
}

// PhotoSummary is one person's migration, for a list.
type PhotoSummary struct {
	Service string            `json:"service"`
	Who     string            `json:"who"`
	Kind    string            `json:"kind"`
	Account string            `json:"account,omitempty"`
	Source  string            `json:"source,omitempty"`
	Run     *PhotoRun         `json:"run,omitempty"`
	Scan    *photoimport.Scan `json:"scan,omitempty"`
	Updated time.Time         `json:"updated"`
}

// photoKey is a service's migrations in the runtime settings.
func photoKey(service string) string { return "photos." + service }

// photoSet is what's kept for a service: each person's migration.
type photoSet struct {
	People map[string]*PhotoState `json:"people"`
}

// photos is this server's migrations.
type photos struct {
	mu sync.Mutex
	// watching: a run's log is being followed (service → who).
	watching map[string]string
	resumed  bool
}

// Places and values, variables for tests.
var (
	photoImportRoot = config.ImportRoot
	photoLibRoot    = config.LibraryRoot
	photoPoll       = 5 * time.Second
)

// Photos is migrating photos into this server's Immich; nil without an
// agent to mount exports or a way to run immich-go.
func (n *Local) Photos() Photos {
	if n.PhotoAgent == nil || n.PhotoUnits == nil || n.State == nil || n.Exec == nil || n.Secrets == nil || n.Launch == nil {
		return nil
	}
	return n
}

// photoService is a service of this server's that photos can be migrated
// into: Immich (photoimport.Supports).
func (n *Local) photoService(service string) (state.Service, error) {
	s, ok := n.service(service)
	if !ok || !photoimport.Supports(s.Package) {
		return s, ErrNotFound
	}
	return s, nil
}

func (n *Local) loadPhotos(ctx context.Context, service string) photoSet {
	var set photoSet
	n.loadSetting(ctx, photoKey(service), &set)
	if set.People == nil {
		set.People = map[string]*PhotoState{}
	}
	return set
}

// updatePhotos changes a person's migration and saves it.
func (n *Local) updatePhotos(ctx context.Context, service, who string, change func(*PhotoState) error) error {
	n.pho.mu.Lock()
	defer n.pho.mu.Unlock()
	set := n.loadPhotos(ctx, service)
	st := set.People[who]
	if st == nil {
		st = &PhotoState{Who: who, Choices: photoimport.DefaultChoices()}
		set.People[who] = st
	}
	if err := change(st); err != nil {
		return err
	}
	st.Updated = time.Now()
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	return n.State.SetSetting(ctx, photoKey(service), string(b))
}

// checkWho refuses a name that isn't a Roostack account's.
func checkWho(who string) error {
	if !auth.ValidUsername(who) {
		return formErrorf("Sign in to migrate photos.")
	}
	return nil
}

func (n *Local) PhotoMigration(ctx context.Context, service, who string) (PhotoView, error) {
	if _, err := n.photoService(service); err != nil {
		return PhotoView{}, err
	}
	n.resumePhotos(ctx)
	set := n.loadPhotos(ctx, service)
	v := PhotoView{}
	if st := set.People[who]; st != nil {
		v.PhotoState = *st
	} else {
		v.PhotoState = PhotoState{Who: who, Choices: photoimport.DefaultChoices()}
	}
	if b := n.photoBusy(ctx); b != nil && (b.Service != service || b.Who != who) {
		v.Busy = b
	}
	v.Ready = n.toolReady(ctx)
	if v.Run != nil {
		v.Log = photoLogTail(n.photoLog(service, who))
	}
	return v, nil
}

func (n *Local) PhotoList(ctx context.Context, service string) ([]PhotoSummary, error) {
	if _, err := n.photoService(service); err != nil {
		return nil, err
	}
	n.resumePhotos(ctx)
	set := n.loadPhotos(ctx, service)
	var out []PhotoSummary
	for _, who := range slices.Sorted(maps.Keys(set.People)) {
		out = append(out, summaryOf(service, set.People[who]))
	}
	slices.SortStableFunc(out, func(a, b PhotoSummary) int { return b.Updated.Compare(a.Updated) })
	return out, nil
}

func summaryOf(service string, st *PhotoState) PhotoSummary {
	s := PhotoSummary{Service: service, Who: st.Who, Kind: st.Kind, Run: st.Run, Scan: st.Scan, Updated: st.Updated}
	if st.Account != nil {
		s.Account = st.Account.Name
	}
	if st.Source != nil {
		s.Source = st.Source.Title
	}
	return s
}

// services is a copy of the services this server runs.
func (n *Local) services() []state.Service {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.assignment.Services)
}

// photoBusy is the migration running on this server, if any.
func (n *Local) photoBusy(ctx context.Context) *PhotoSummary {
	for _, sv := range n.services() {
		if !photoimport.Supports(sv.Package) {
			continue
		}
		set := n.loadPhotos(ctx, sv.Name())
		for _, st := range set.People {
			if st.Run != nil && st.Run.Running {
				s := summaryOf(sv.Name(), st)
				return &s
			}
		}
	}
	return nil
}

func (n *Local) SetPhotoKind(ctx context.Context, service, who, kind string) error {
	if err := n.photoReady(service, who); err != nil {
		return err
	}
	if !slices.Contains(photoimport.Kinds, kind) {
		return formErrorf("Choose where your photos are now.")
	}
	return n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		if err := notRunning(st); err != nil {
			return err
		}
		if st.Kind != kind {
			st.Kind, st.Guided, st.Scan, st.Check = kind, kind == photoimport.KindFolder, nil, nil
		}
		return nil
	})
}

func (n *Local) SetPhotoGuided(ctx context.Context, service, who string) error {
	if err := n.photoReady(service, who); err != nil {
		return err
	}
	return n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		st.Guided = true
		return nil
	})
}

// photoReady checks the service and the person.
func (n *Local) photoReady(service, who string) error {
	if _, err := n.photoService(service); err != nil {
		return err
	}
	return checkWho(who)
}

func notRunning(st *PhotoState) error {
	if st.Run != nil && st.Run.Running {
		return formErrorf("Your migration is running: stop it first.")
	}
	return nil
}

// photoDir is where a person's export is, as this process sees it.
func photoDir(service, who string, src *PhotoSource) string {
	if src == nil {
		return ""
	}
	if src.Kind == "location" {
		return filepath.Join(photoLibRoot, src.Location, filepath.FromSlash(src.Sub))
	}
	return filepath.Join(photoImportRoot, agent.PhotoSpotName(service, who))
}

func (n *Local) SetPhotoSource(ctx context.Context, service, who string, src PhotoSource) error {
	if err := n.photoReady(service, who); err != nil {
		return err
	}
	st := n.loadPhotos(ctx, service).People[who]
	if st != nil {
		if err := notRunning(st); err != nil {
			return err
		}
	}
	switch src.Kind {
	case "drive", "folder", "share":
		if _, err := n.PhotoAgent.MountPhotos(ctx, service, who, src.Library); err != nil {
			return formErrorf("Roostack couldn't open that folder: %v.", strings.TrimSuffix(err.Error(), "."))
		}
		if src.Library.Share != nil {
			share := *src.Library.Share
			share.Password = "" // the agent keeps it, root's only
			src.Library.Share = &share
		}
	case "location":
		if !agent.ValidLibraryName(src.Location) || (src.Sub != "" && !fs.ValidPath(src.Sub)) {
			return formErrorf("Choose one of the locations, and a folder in it.")
		}
		if st != nil && st.Source != nil && st.Source.Kind != "location" {
			n.PhotoAgent.UnmountPhotos(ctx, service, who)
		}
	default:
		return formErrorf("Choose a drive, a folder or a share.")
	}
	if err := n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		st.Source, st.Scan, st.ScanErr, st.Check, st.Scanning = &src, nil, "", nil, true
		return nil
	}); err != nil {
		return err
	}
	go n.scanPhotos(context.WithoutCancel(ctx), service, who)
	return nil
}

// scanPhotos has the quick look, in the background: a share or a slow
// drive takes a while for many sidecars.
func (n *Local) scanPhotos(ctx context.Context, service, who string) {
	st := n.loadPhotos(ctx, service).People[who]
	if st == nil || st.Source == nil {
		return
	}
	dir := photoDir(service, who, st.Source)
	scan, err := photoimport.ScanFS(os.DirFS(dir), st.Kind)
	n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		st.Scanning = false
		if err != nil {
			st.ScanErr = "Roostack couldn't read that folder: " + userError(err)
			return nil
		}
		st.Scan, st.ScanErr = scan, ""
		return nil
	})
}

// userError is an error's last part, for a sentence.
func userError(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:]
	}
	return strings.TrimSuffix(s, ".") + "."
}

// photoKeySecret is the Podman secret holding a person's key while their
// migration lasts.
func photoKeySecret(service, who string) string { return "roostack-photos-" + service + "-" + who }

func (n *Local) SetPhotoKey(ctx context.Context, service, who, key string) error {
	if err := n.photoReady(service, who); err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" || len(key) > 200 || strings.ContainsAny(key, " \t\r\n\x00") {
		return formErrorf("Paste the key Immich showed you: one long line of letters and digits.")
	}
	if st := n.loadPhotos(ctx, service).People[who]; st != nil {
		if err := notRunning(st); err != nil {
			return err
		}
	}
	acct, err := n.immichAccount(ctx, service, key)
	if err != nil {
		return err
	}
	if err := n.Secrets.Replace(ctx, photoKeySecret(service, who), []byte(key)); err != nil {
		return err
	}
	return n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		st.Account, st.HasKey, st.Check = &acct, true, nil
		return nil
	})
}

func (n *Local) SetPhotoChoices(ctx context.Context, service, who string, c photoimport.Choices) error {
	if err := n.photoReady(service, who); err != nil {
		return err
	}
	return n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		if err := notRunning(st); err != nil {
			return err
		}
		st.Choices, st.ChoicesSet, st.Check = c, true, nil
		return nil
	})
}

func (n *Local) CheckPhotos(ctx context.Context, service, who string) error {
	if err := n.photoReady(service, who); err != nil {
		return err
	}
	if err := n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		if err := notRunning(st); err != nil {
			return err
		}
		if st.Source == nil || !st.HasKey {
			return formErrorf("Choose where your export is, and give your key, first.")
		}
		st.Checking, st.CheckErr = true, ""
		return nil
	}); err != nil {
		return err
	}
	go n.checkPhotos(context.WithoutCancel(ctx), service, who)
	return nil
}

// checkPhotos looks in the export again (parts added since), at the
// account, and at the room on the library's disk.
func (n *Local) checkPhotos(ctx context.Context, service, who string) {
	fail := func(err error) {
		n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
			st.Checking, st.CheckErr = false, err.Error()
			return nil
		})
	}
	st := n.loadPhotos(ctx, service).People[who]
	if st == nil || st.Source == nil {
		return
	}
	scan, err := photoimport.ScanFS(os.DirFS(photoDir(service, who, st.Source)), st.Kind)
	if err != nil {
		fail(fmt.Errorf("Roostack couldn't read your export: %s", userError(err)))
		return
	}
	key, err := n.Secrets.Read(ctx, photoKeySecret(service, who))
	if err != nil {
		fail(errors.New("Roostack no longer has your key: give it again"))
		return
	}
	acct, err := n.immichAccount(ctx, service, string(key))
	if err != nil {
		fail(err)
		return
	}
	lib := n.photoLibrary(service)
	c := &PhotoCheck{At: time.Now(), Needs: scan.Bytes + scan.Bytes/10, Free: freeSpace(lib), Disk: diskWords(mountPoint(lib)), Account: acct}
	n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		st.Scan, st.ScanErr, st.Account, st.Check, st.Checking, st.CheckErr = scan, "", &acct, c, false, ""
		return nil
	})
}

// photoLibrary is Immich's library folder on this server.
func (n *Local) photoLibrary(service string) string {
	s, ok := n.service(service)
	if !ok {
		return ""
	}
	return generate.StoragePath(n.currentLab(), s.Package, s.Instance, photoimport.LibraryStorage)
}

// photoUnit is the unit a service's migration runs in.
func photoUnit(service string) string { return "roostack-photos-" + service }

// photoWork is a person's folder for immich-go's logs.
func (n *Local) photoWork(service, who string) string {
	return filepath.Join(n.currentLab().Paths.PhotosDir(), service, who)
}

// photoLog is a person's running or last run's log.
func (n *Local) photoLog(service, who string) string {
	return filepath.Join(n.photoWork(service, who), "immich-go.log")
}

func (n *Local) StartPhotos(ctx context.Context, service, who string) error {
	if err := n.photoReady(service, who); err != nil {
		return err
	}
	if b := n.photoBusy(ctx); b != nil {
		if b.Service == service && b.Who == who {
			return nil
		}
		return formErrorf("%s is migrating photos now: one migration runs at a time. Come back once it's done.", b.Who)
	}
	st := n.loadPhotos(ctx, service).People[who]
	if st == nil || st.Check == nil || st.Scan == nil {
		return formErrorf("Check first: it says whether everything's there and there's room.")
	}
	if p := st.Problems(); len(p) > 0 {
		return formErrorf("%s", p[0])
	}
	before := st.Check.Account
	if r := st.Run; r != nil && !r.Started.IsZero() && (r.Stopped || r.Interrupted || r.Err != "") {
		before = r.Before // carrying on: counts from the first start
	}
	if err := n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		run := &PhotoRun{Started: time.Now(), Running: true, Building: !n.toolReady(ctx), Before: before, Passes: 1,
			LibraryBefore: n.librarySize(ctx, service)}
		if old := st.Run; old != nil && !old.Finished.IsZero() && (old.Stopped || old.Interrupted || old.Err != "") {
			// Carrying on: the same migration, another pass.
			run.Passes, run.Earlier, run.FirstStarted, run.LibraryBefore = old.Passes+1, old.Migrated(), old.Began(), old.LibraryBefore
		}
		st.Run = run
		return nil
	}); err != nil {
		return err
	}
	go n.runPhotos(context.WithoutCancel(ctx), service, who)
	return nil
}

// librarySize is the library folder's size (-1 if it can't be counted).
func (n *Local) librarySize(ctx context.Context, service string) int64 {
	b, err := n.du(ctx, true, n.photoLibrary(service))
	if err != nil {
		return -1
	}
	return b
}

// toolReady says whether the migration tool's image is there.
func (n *Local) toolReady(ctx context.Context) bool {
	return n.Launch(ctx, nil, "podman", "image", "exists", photoimport.ToolImage).Run() == nil
}

// buildTool builds the migration tool's image (build/immich-go): about a
// minute and a half the first time, with Caddy's builder there already.
func (n *Local) buildTool(ctx context.Context, w io.Writer) error {
	cmd := n.Launch(ctx, nil, "podman", "build", "--tag", photoimport.ToolImage,
		"--file", photoimport.ToolBuildDir+"/Containerfile", photoimport.ToolBuildDir)
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// runPhotos builds the tool if need be, starts immich-go in its unit and
// follows it to the end.
func (n *Local) runPhotos(ctx context.Context, service, who string) {
	log := n.photoActivity(service, who)
	fail := func(err error) {
		log.Finish(err)
		n.endPhotos(ctx, service, who, func(r *PhotoRun) { r.Err = err.Error() })
	}
	st := n.loadPhotos(ctx, service).People[who]
	if st == nil || st.Run == nil {
		return
	}
	if st.Run.Building {
		log.Step("Getting the migration tool ready (the first time takes a minute or two)")
		if err := n.buildTool(ctx, log); err != nil {
			fail(fmt.Errorf("Roostack couldn't build the migration tool: %w", err))
			return
		}
		n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
			if st.Run != nil {
				st.Run.Building = false
			}
			return nil
		})
	}
	args, err := n.photoCommand(service, who, st)
	if err != nil {
		fail(err)
		return
	}
	work := n.photoWork(service, who)
	if err := os.MkdirAll(work, 0o700); err != nil {
		fail(err)
		return
	}
	os.Remove(n.photoLog(service, who)) // each pass logs afresh; the tally adds up
	log.Step("Migrating photos into %s's account", st.Account.Name)
	unit := photoUnit(service)
	n.PhotoUnits.Forget(ctx, unit) // a run before, done or failed
	if err := n.PhotoUnits.RunDetached(ctx, unit, "podman", args...); err != nil {
		fail(fmt.Errorf("starting the migration: %w", err))
		return
	}
	n.watchPhotos(ctx, service, who, log)
}

// photoCommand is podman's command line for a person's run: immich-go in
// its own container on Immich's network, the export read-only, their
// work folder for its log, and the key from its Podman secret.
func (n *Local) photoCommand(service, who string, st *PhotoState) ([]string, error) {
	s, err := n.photoService(service)
	if err != nil {
		return nil, err
	}
	container, port := photoimport.Server(s.Package)
	if container == "" {
		return nil, errors.New("Immich's package has no web address to reach it on")
	}
	var parts []string
	if st.Scan != nil {
		for _, p := range st.Scan.Parts {
			parts = append(parts, p.Name)
		}
	}
	run := photoimport.Run{Kind: st.Kind, Choices: st.Choices, Source: "/export", Parts: parts, Log: "/work/immich-go.log",
		Server: fmt.Sprintf("http://%s:%d", generate.ContainerName(service, container), port), Tasks: min(runtime.NumCPU(), 4)}
	tool, err := photoimport.Command(run)
	if err != nil {
		return nil, err
	}
	args := []string{"run", "--rm", "--name", photoUnit(service), "--network", generate.NetworkName(service),
		"--memory", photoimport.ToolMemory, "--pull", "never",
		"--volume", photoDir(service, who, st.Source) + ":/export:ro,rslave",
		"--volume", n.photoWork(service, who) + ":/work",
		"--secret", photoKeySecret(service, who) + ",type=env,target=" + photoimport.KeyEnv,
		"--label", "io.roostack.service=" + service, "--label", "io.roostack.component=photo-migration",
		photoimport.ToolImage}
	return append(args, tool...), nil
}

// photoActivity is a person's migration's live log (activity.PhotosFor).
func (n *Local) photoActivity(service, who string) *activity.Log {
	if n.Activity == nil {
		return activity.New().Start(activity.PhotosFor(service, who), "Migrating photos")
	}
	return n.Activity.Start(activity.PhotosFor(service, who), "Migrating photos")
}

// watchPhotos follows a run until its unit ends: the log read as it
// grows, the unit asked whether it's still going.
func (n *Local) watchPhotos(ctx context.Context, service, who string, log *activity.Log) {
	n.pho.mu.Lock()
	if n.pho.watching == nil {
		n.pho.watching = map[string]string{}
	}
	if n.pho.watching[service] == who {
		n.pho.mu.Unlock()
		return
	}
	n.pho.watching[service] = who
	n.pho.mu.Unlock()
	defer func() {
		n.pho.mu.Lock()
		delete(n.pho.watching, service)
		n.pho.mu.Unlock()
	}()
	unit := photoUnit(service)
	var offset int64
	for {
		offset = n.readPhotoLog(ctx, service, who, offset)
		running, code, found, err := n.PhotoUnits.Exited(ctx, unit)
		switch {
		case err != nil:
			n.Log.Warn("reading the photo migration's unit", "unit", unit, "err", err)
		case !found:
			n.readPhotoLog(ctx, service, who, offset)
			if st := n.loadPhotos(ctx, service).People[who]; st != nil && st.Run != nil && st.Run.Stopped {
				log.Finish(nil) // stopped, and systemd has cleared it already
				n.endPhotos(ctx, service, who, func(r *PhotoRun) {})
				return
			}
			// Gone without Roostack seeing it end: the server restarted.
			log.Finish(errors.New("it stopped when the server restarted"))
			n.endPhotos(ctx, service, who, func(r *PhotoRun) { r.Interrupted = true })
			return
		case !running:
			n.readPhotoLog(ctx, service, who, offset)
			n.PhotoUnits.Forget(ctx, unit)
			st := n.loadPhotos(ctx, service).People[who]
			stopped := st != nil && st.Run != nil && st.Run.Stopped
			switch {
			case stopped:
				log.Finish(nil)
				n.endPhotos(ctx, service, who, func(r *PhotoRun) {})
			case code != 0:
				err := fmt.Errorf("the migration tool stopped with an error (%d): its log under Details says why", code)
				log.Finish(err)
				n.endPhotos(ctx, service, who, func(r *PhotoRun) { r.Err = err.Error() })
			default:
				log.Finish(nil)
				n.finishPhotos(ctx, service, who)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(photoPoll):
		}
	}
}

// readPhotoLog adds the log's new lines to the run's tally, from offset;
// it returns where it got to.
func (n *Local) readPhotoLog(ctx context.Context, service, who string, offset int64) int64 {
	f, err := os.Open(n.photoLog(service, who))
	if err != nil {
		return offset
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset
	}
	// Only whole lines: the last may still be being written.
	b, err := io.ReadAll(io.LimitReader(f, 64<<20))
	if err != nil {
		return offset
	}
	end := strings.LastIndexByte(string(b), '\n') + 1
	if end == 0 {
		return offset
	}
	n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		if st.Run != nil {
			st.Run.Tally.Read(strings.NewReader(string(b[:end])))
		}
		return nil
	})
	return offset + int64(end)
}

// endPhotos records a run's end that isn't a finish (stopped, interrupted,
// failed): the person carries on, or forgets it, from the page.
func (n *Local) endPhotos(ctx context.Context, service, who string, change func(*PhotoRun)) {
	n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		if st.Run != nil {
			st.Run.Running, st.Run.Building, st.Run.Finished = false, false, time.Now()
			change(st.Run)
		}
		return nil
	})
	n.notePhotos(ctx, service, who)
}

// finishPhotos records a finished migration: Immich's counts and the
// library's size after it, then forgets the key and unmounts the export.
func (n *Local) finishPhotos(ctx context.Context, service, who string) {
	var after PhotoAccount
	if key, err := n.Secrets.Read(ctx, photoKeySecret(service, who)); err == nil {
		after, _ = n.immichAccount(ctx, service, string(key))
	}
	size := n.librarySize(ctx, service)
	free := freeSpace(n.photoLibrary(service))
	n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		if st.Run != nil {
			st.Run.Running, st.Run.Finished, st.Run.After, st.Run.LibraryAfter, st.Run.FreeAfter =
				false, time.Now(), after, size, free
		}
		st.HasKey = false
		return nil
	})
	n.Secrets.Remove(ctx, photoKeySecret(service, who))
	st := n.loadPhotos(ctx, service).People[who]
	if st != nil && st.Source != nil && st.Source.Kind != "location" {
		n.PhotoAgent.UnmountPhotos(ctx, service, who)
	}
	n.notePhotos(ctx, service, who)
}

// notePhotos tells the controller a migration ended (OnPhotos), for its
// email.
func (n *Local) notePhotos(ctx context.Context, service, who string) {
	if n.OnPhotos == nil {
		return
	}
	if st := n.loadPhotos(ctx, service).People[who]; st != nil {
		n.OnPhotos(ctx, summaryOf(service, st))
	}
}

func (n *Local) StopPhotos(ctx context.Context, service, who string) error {
	if err := n.photoReady(service, who); err != nil {
		return err
	}
	st := n.loadPhotos(ctx, service).People[who]
	if st == nil || st.Run == nil || !st.Run.Running {
		return nil
	}
	if err := n.updatePhotos(ctx, service, who, func(st *PhotoState) error {
		st.Run.Stopped = true
		return nil
	}); err != nil {
		return err
	}
	return n.PhotoUnits.Stop(ctx, photoUnit(service))
}

func (n *Local) ForgetPhotos(ctx context.Context, service, who string) error {
	if err := n.photoReady(service, who); err != nil {
		return err
	}
	st := n.loadPhotos(ctx, service).People[who]
	if st != nil {
		if err := notRunning(st); err != nil {
			return err
		}
		if st.Source != nil && st.Source.Kind != "location" {
			n.PhotoAgent.UnmountPhotos(ctx, service, who)
		}
	}
	n.Secrets.Remove(ctx, photoKeySecret(service, who))
	n.pho.mu.Lock()
	defer n.pho.mu.Unlock()
	set := n.loadPhotos(ctx, service)
	delete(set.People, who)
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	return n.State.SetSetting(ctx, photoKey(service), string(b))
}

// resumePhotos, once after Roostack starts, follows any run that was
// going: its unit may still be running (it outlives Roostack), or have
// ended meanwhile.
func (n *Local) resumePhotos(ctx context.Context) {
	n.pho.mu.Lock()
	if n.pho.resumed {
		n.pho.mu.Unlock()
		return
	}
	n.pho.resumed = true
	n.pho.mu.Unlock()
	for _, sv := range n.services() {
		if !photoimport.Supports(sv.Package) {
			continue
		}
		service := sv.Name()
		for who, st := range n.loadPhotos(ctx, service).People {
			if st.Run != nil && st.Run.Running {
				go n.watchPhotos(context.WithoutCancel(ctx), service, who, n.photoActivity(service, who))
			}
		}
	}
}

// photoLogTail is the end of a run's log, for Details: its lines made
// readable, and never a key (immich-go prints a key's last four
// characters among its flags; those lines are left out).
func photoLogTail(file string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	const tail = 48 << 10
	if fi, err := f.Stat(); err == nil && fi.Size() > tail {
		f.Seek(fi.Size()-tail, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	lines := strings.Split(string(b), "\n")
	if len(b) == tail {
		lines = lines[1:]
	}
	var out []string
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l), "api-key") || strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, photoimport.Readable(l))
	}
	if len(out) > 200 {
		out = out[len(out)-200:]
	}
	return strings.Join(out, "\n")
}

// immichAccount asks Immich about a key, from inside its own container
// (photoimport.AccountScript), the key passed as environment.
func (n *Local) immichAccount(ctx context.Context, service, key string) (PhotoAccount, error) {
	s, err := n.photoService(service)
	if err != nil {
		return PhotoAccount{}, err
	}
	container, port := photoimport.Server(s.Package)
	ctx, cancel := context.WithTimeout(ctx, actionTimeout)
	defer cancel()
	out, stderr, err := n.Exec(ctx, generate.ContainerName(service, container),
		map[string]string{"API_KEY": key, "PORT": fmt.Sprint(port)}, []string{"node", "-e", photoimport.AccountScript})
	if err != nil {
		return PhotoAccount{}, fmt.Errorf("asking Immich about the key: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	a, err := photoimport.ParseAccount(out)
	if err != nil {
		var ke *photoimport.KeyError
		if errors.As(err, &ke) {
			return PhotoAccount{}, formErrorf("%s", ke.Msg)
		}
		return PhotoAccount{}, err
	}
	return PhotoAccount{ID: a.ID, Name: a.Name, Email: a.Email, Missing: a.Missing, All: a.All, Photos: a.Photos, Videos: a.Videos}, nil
}

func (n *Local) PhotoFolders(ctx context.Context, location, sub string) ([]PhotoFolder, error) {
	if !agent.ValidLibraryName(location) || (sub != "" && !fs.ValidPath(sub)) {
		return nil, ErrNotFound
	}
	dir := filepath.Join(photoLibRoot, location, filepath.FromSlash(sub))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, formErrorf("Roostack couldn't open that location: %s", userError(err))
	}
	var out []PhotoFolder
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "@eaDir" || e.Name() == "#recycle" {
			continue
		}
		f := PhotoFolder{Name: e.Name()}
		if inner, err := os.ReadDir(filepath.Join(dir, e.Name())); err == nil {
			for _, i := range inner {
				if strings.HasSuffix(strings.ToLower(i.Name()), ".zip") {
					f.Zips++
				}
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// diskWords names the disk a folder is on, for a sentence: the server's own
// disk, or the one mounted at a place.
func diskWords(mount string) string {
	if mount == "/" || mount == "" {
		return "the server's own disk"
	}
	return "the disk at " + mount
}
