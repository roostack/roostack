package manager

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/auth"
	"roostack.cloud/roostack/internal/mail"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/photoimport"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// Migrating photos into Immich (SPEC.md §8.5): the server Immich runs on
// keeps each person's migration and runs it (node/photos.go); the
// controller keeps the Owner's choices (who may migrate, and where from)
// and their additional locations, which are media libraries kept for
// migrations (store.MediaLibrary.PhotosFor), so they're mounted, checked
// and shown as libraries are, and no service reads them.

// Photos is migrating photos into the lab's Immich services, as the
// controller sees it (Manager.Photos): the Owner's choices and locations,
// who may migrate from where, and the emails when a migration starts and
// ends.
type Photos struct {
	core photoCore
	// Mail sends the emails, to the people in Users (nil: none are sent).
	Mail  Mailer
	Users Users
}

// photoCore is what photos use of the Manager: besides the core, the
// controller's settings and the lab's media libraries.
type photoCore interface {
	core
	settings() KV
	libraries() LibraryStore
	AddLibrary(ctx context.Context, req LibraryRequest) error
	RemoveLibrary(ctx context.Context, serverID, name string) error
	libraryMedia(serverID string) (node.Media, error)
}

func (m *Manager) settings() KV            { return m.Settings }
func (m *Manager) libraries() LibraryStore { return m.Libraries }

// Mailer sends email (mail.Service).
type Mailer interface {
	Send(ctx context.Context, msg mail.Message) error
}

// Users are the lab's people (store.Store).
type Users interface {
	ListUsers(ctx context.Context) ([]store.User, error)
	UserByUsername(ctx context.Context, username string) (*store.User, error)
}

// PhotoSettings are the Owner's choices for migrations into one Immich.
// The zero value is the default: everyone who uses Immich, from their own
// drives and shares.
type PhotoSettings struct {
	// OwnersOnly: only Owners and Admins may migrate.
	OwnersOnly bool `json:"owners_only,omitempty"`
	// NoDrives and NoShares: others may not use a drive they plug in, or
	// a share with their own login.
	NoDrives bool `json:"no_drives,omitempty"`
	NoShares bool `json:"no_shares,omitempty"`
	// Memory: how much of the server's memory a migration may use
	// (photoimport.MemoryShares, "" for half), kept on Immich's server too.
	Memory string `json:"memory,omitempty"`
}

func photoSettingsKey(service string) string { return "photo-settings." + service }

// PhotoService is an Immich photos can be migrated into.
type PhotoService struct {
	Name, Title, Host string
}

// PhotoServices are the lab's Immich services.
func (p *Photos) PhotoServices(ctx context.Context) []PhotoService {
	services, err := state.LoadServices(p.core.labConfig())
	if err != nil {
		p.core.logger().Error("reading services", "err", err)
		return nil
	}
	var out []PhotoService
	for _, s := range services {
		if photoimport.Supports(s.Package) {
			out = append(out, PhotoService{Name: s.Name(), Title: s.Title(), Host: s.Instance.HostOr(p.core.labConfig().NodeID)})
		}
	}
	return out
}

// photoService is an Immich of the lab's, and its server.
func (p *Photos) photoService(service string) (PhotoService, error) {
	pkg, in, err := p.core.installedService(service)
	if err != nil {
		return PhotoService{}, err
	}
	if !photoimport.Supports(pkg) {
		return PhotoService{}, ErrNotFound
	}
	return PhotoService{Name: service, Title: cmp.Or(in.Title, pkg.Title), Host: in.HostOr(p.core.labConfig().NodeID)}, nil
}

// PhotosOf is migrating photos into a service, on its server.
func (p *Photos) PhotosOf(service string) (node.Photos, error) {
	ps, err := p.photoService(service)
	if err != nil {
		return nil, err
	}
	n := p.core.node(ps.Host)
	if n == nil {
		return nil, usererr.New("Roostack can't reach the server %s runs on.", ps.Title)
	}
	// Not on the lab API yet: another server's Photos is nil.
	if id := p.core.labConfig().NodeID; n != p.core.node(id) {
		return nil, usererr.New("Photos can be migrated only into an Immich on %s for now.", p.core.nameOf(id))
	}
	x := n.Photos()
	if x == nil {
		return nil, usererr.New("Photos can't be migrated into services on that server.")
	}
	return x, nil
}

// PhotoSettings are the Owner's choices for a service.
func (p *Photos) PhotoSettings(ctx context.Context, service string) PhotoSettings {
	var s PhotoSettings
	if kv := p.core.settings(); kv != nil {
		s, _ = store.GetJSON[PhotoSettings](ctx, kv, photoSettingsKey(service))
	}
	return s
}

// SetPhotoSettings keeps the Owner's choices for a service.
func (p *Photos) SetPhotoSettings(ctx context.Context, service string, s PhotoSettings) error {
	if _, err := p.photoService(service); err != nil {
		return err
	}
	kv := p.core.settings()
	if kv == nil {
		return usererr.New("Settings can't be kept here.")
	}
	if s.Memory == "" {
		s.Memory = photoimport.MemoryHalf
	}
	x, err := p.PhotosOf(service)
	if err != nil {
		return err
	}
	if err := x.SetPhotoMemory(ctx, service, s.Memory); err != nil {
		return err
	}
	return store.PutJSON(ctx, kv, photoSettingsKey(service), s)
}

// PhotoLocations are the Owner's additional locations for a service.
func (p *Photos) PhotoLocations(ctx context.Context, service string) []store.MediaLibrary {
	lib := p.core.libraries()
	if lib == nil {
		return nil
	}
	libs, err := lib.MediaLibraries(ctx)
	if err != nil {
		p.core.logger().Error("reading media libraries", "err", err)
		return nil
	}
	return slices.DeleteFunc(libs, func(l store.MediaLibrary) bool { return l.PhotosFor != service })
}

// AddPhotoLocation adds a location for migrating photos into a service: a
// media library on its server that no service reads.
func (p *Photos) AddPhotoLocation(ctx context.Context, service string, req LibraryRequest) error {
	ps, err := p.photoService(service)
	if err != nil {
		return err
	}
	req.Server, req.PhotosFor, req.Services, req.AsOwner = ps.Host, service, nil, false
	return p.core.AddLibrary(ctx, req)
}

// RemovePhotoLocation removes one of a service's locations.
func (p *Photos) RemovePhotoLocation(ctx context.Context, service, name string) error {
	for _, l := range p.PhotoLocations(ctx, service) {
		if l.Name == name {
			return p.core.RemoveLibrary(ctx, l.ServerID, l.Name)
		}
	}
	return ErrNotFound
}

// PhotoDrive is a drive a person may, or may not, migrate photos from.
type PhotoDrive struct {
	agent.Drive
	// Why it can't be used: "backups", "unreadable" or "media" (it holds
	// the lab's media libraries); "" when it can.
	Why string
}

// PhotoDrives are the drives on a service's server for its step 3: for
// someone who isn't an Owner or Admin, never the backups' drive, the
// system disk, or a drive holding a media library (their files aren't
// theirs to read).
func (p *Photos) PhotoDrives(ctx context.Context, service string, admin bool) ([]PhotoDrive, error) {
	ps, err := p.photoService(service)
	if err != nil {
		return nil, err
	}
	media, err := p.core.libraryMedia(ps.Host)
	if err != nil {
		return nil, err
	}
	drives, err := media.Drives(ctx)
	if err != nil {
		return nil, err
	}
	libraryDrives := map[string]bool{}
	if lib := p.core.libraries(); lib != nil {
		if libs, err := lib.MediaLibraries(ctx); err == nil {
			for _, l := range libs {
				if l.Drive != "" && l.PhotosFor == "" {
					libraryDrives[l.Drive] = true
				}
			}
		}
	}
	var out []PhotoDrive
	for _, d := range drives {
		if d.Kind != "usb" && d.Kind != "disk" {
			continue
		}
		pd := PhotoDrive{Drive: d}
		switch {
		case d.Backup:
			pd.Why = "backups"
		case !d.Usable || d.UUID == "":
			pd.Why = "unreadable"
		case !admin && libraryDrives[d.UUID]:
			pd.Why = "media"
		}
		out = append(out, pd)
	}
	return out, nil
}

// CheckPhotoSource says whether a person may migrate from a place: Owners
// and Admins from anywhere Roostack can read; others only from what the
// Owner allows (SPEC.md §8.5).
func (p *Photos) CheckPhotoSource(ctx context.Context, service string, src node.PhotoSource, admin bool) error {
	ps, err := p.photoService(service)
	if err != nil {
		return err
	}
	set := p.PhotoSettings(ctx, service)
	switch src.Kind {
	case "location":
		for _, l := range p.PhotoLocations(ctx, service) {
			if l.Name == src.Location && l.ServerID == ps.Host {
				return nil
			}
		}
		return usererr.New("That location isn't there any more: choose another.")
	case "drive":
		if !admin && set.NoDrives {
			return usererr.New("Drives can't be used here: choose one of the other places.")
		}
		drives, err := p.PhotoDrives(ctx, service, admin)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(drives, func(d PhotoDrive) bool { return d.UUID == src.Library.Drive })
		if i < 0 {
			return usererr.New("That drive isn't plugged in: plug it in and look again.")
		}
		if drives[i].Why != "" {
			return usererr.New("%s can't be used for migrating photos: choose another drive.", drives[i].Name)
		}
		return nil
	case "share":
		if !admin && set.NoShares {
			return usererr.New("Shares can't be used here: choose one of the other places.")
		}
		return nil
	case "folder":
		if !admin {
			return usererr.New("Only Owners and Admins can choose a folder on the server.")
		}
		return nil
	}
	return usererr.New("Choose where your export is.")
}

// photoTitle is what people call a service photos are migrated into.
func (p *Photos) photoTitle(ctx context.Context, service string) string {
	for _, ps := range p.PhotoServices(ctx) {
		if ps.Name == service {
			return ps.Title
		}
	}
	return service
}

// MigrationStarted tells the lab's Owners that someone else started
// migrating photos (the Owner's card says they're told), by email.
func (p *Photos) MigrationStarted(ctx context.Context, service string, u *store.User) {
	if p.Mail == nil || p.Users == nil || u.Role == auth.RoleOwner {
		return
	}
	title := p.photoTitle(ctx, service)
	body := fmt.Sprintf("%s started migrating photos into their %s account. You'll get another email when it ends.\n\n"+
		"Follow it on %s's page: https://%s/services/%s#photos\n", cmp.Or(u.Name, u.Username), title, title, p.core.labConfig().UIHost(), service)
	p.mailOwners(ctx, fmt.Sprintf("%s is migrating photos into %s", cmp.Or(u.Name, u.Username), title), body)
}

// MigrationEnded tells the person whose migration ended, and the Owners
// if it isn't theirs, how it went (node.Local.OnPhotos).
func (p *Photos) MigrationEnded(ctx context.Context, m node.PhotoSummary) {
	if p.Mail == nil || p.Users == nil || m.Run == nil {
		return
	}
	title := p.photoTitle(ctx, m.Service)
	t := m.Run.Tally
	var subject, what string
	switch {
	case m.Run.Err != "":
		subject, what = "Migrating photos into "+title+" stopped", "It stopped with an error: "+m.Run.Err+"."
	case m.Run.Stopped:
		subject, what = "Migrating photos into "+title+" was stopped", "It was stopped. What's in Immich stays there; carry on whenever you like."
	case m.Run.Interrupted:
		subject, what = "Migrating photos into "+title+" was interrupted", "It stopped when the server restarted. What's in Immich stays there; carry on from the page."
	default:
		subject = "Your photos are in " + title
		what = fmt.Sprintf("%s migrated, %s already there.", words.Count(m.Run.Migrated(), "photo or video", "photos and videos"),
			words.Count(m.Run.HadBefore(), "was", "were"))
		if t.Failed > 0 {
			what += fmt.Sprintf(" %s couldn't be migrated: the page says which.", words.Count(t.Failed, "file", "files"))
		}
	}
	link := fmt.Sprintf("https://%s/immich-photos/%s", p.core.labConfig().UIHost(), m.Service)
	body := what + "\n\nThe details: " + link + "\n"
	if u, err := p.Users.UserByUsername(ctx, m.Who); err == nil && u.Email != "" {
		if err := p.Mail.Send(ctx, mail.Message{To: []string{u.Email}, Subject: subject, Body: body}); err != nil && err != mail.ErrNotConfigured {
			p.core.logger().Warn("emailing the end of a photo migration", "err", err)
		}
		if u.Role == auth.RoleOwner {
			return
		}
	}
	p.mailOwners(ctx, m.Who+": "+subject, body)
}

// mailOwners emails the lab's Owners who have an address.
func (p *Photos) mailOwners(ctx context.Context, subject, body string) {
	users, err := p.Users.ListUsers(ctx)
	if err != nil {
		return
	}
	for _, u := range users {
		if u.Role != auth.RoleOwner || u.Email == "" {
			continue
		}
		if err := p.Mail.Send(ctx, mail.Message{To: []string{u.Email}, Subject: subject, Body: body}); err != nil {
			if err != mail.ErrNotConfigured {
				p.core.logger().Warn("emailing an Owner about a photo migration", "err", err)
			}
			return
		}
	}
}
