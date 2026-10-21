package manager

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/photoimport"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/store"
)

// Migrating photos into Immich (SPEC.md §8.5): the server Immich runs on
// keeps each person's migration and runs it (node/photos.go); the
// controller keeps the Owner's choices (who may migrate, and where from)
// and their additional locations, which are media libraries kept for
// migrations (store.MediaLibrary.PhotosFor), so they're mounted, checked
// and shown as libraries are, and no service reads them.

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
}

func photoSettingsKey(service string) string { return "photo-settings." + service }

// PhotoService is an Immich photos can be migrated into.
type PhotoService struct {
	Name, Title, Host string
}

// PhotoServices are the lab's Immich services.
func (m *Manager) PhotoServices(ctx context.Context) []PhotoService {
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		m.Log.Error("reading services", "err", err)
		return nil
	}
	var out []PhotoService
	for _, s := range services {
		if photoimport.Supports(s.Package) {
			out = append(out, PhotoService{Name: s.Name(), Title: s.Title(), Host: cmp.Or(s.Instance.Host, m.Lab.NodeID)})
		}
	}
	return out
}

// photoService is an Immich of the lab's, and its server.
func (m *Manager) photoService(service string) (PhotoService, error) {
	p, in, err := m.installedService(service)
	if err != nil {
		return PhotoService{}, err
	}
	if !photoimport.Supports(p) {
		return PhotoService{}, ErrNotFound
	}
	return PhotoService{Name: service, Title: cmp.Or(in.Title, p.Title), Host: cmp.Or(in.Host, m.Lab.NodeID)}, nil
}

// PhotosOf is migrating photos into a service, on its server.
func (m *Manager) PhotosOf(service string) (node.Photos, error) {
	ps, err := m.photoService(service)
	if err != nil {
		return nil, err
	}
	n := m.node(ps.Host)
	if n == nil {
		return nil, formErrorf("Roostack can't reach the server %s runs on.", ps.Title)
	}
	l, ok := n.(interface{ Photos() node.Photos })
	if !ok {
		return nil, formErrorf("Photos can be migrated only into an Immich on %s for now.", m.nameOf(m.Lab.NodeID))
	}
	x := l.Photos()
	if x == nil {
		return nil, formErrorf("Photos can't be migrated into services on that server.")
	}
	return x, nil
}

// PhotoSettings are the Owner's choices for a service.
func (m *Manager) PhotoSettings(ctx context.Context, service string) PhotoSettings {
	var s PhotoSettings
	if m.Settings != nil {
		if v, err := m.Settings.Setting(ctx, photoSettingsKey(service)); err == nil && v != "" {
			json.Unmarshal([]byte(v), &s)
		}
	}
	return s
}

// SetPhotoSettings keeps the Owner's choices for a service.
func (m *Manager) SetPhotoSettings(ctx context.Context, service string, s PhotoSettings) error {
	if _, err := m.photoService(service); err != nil {
		return err
	}
	if m.Settings == nil {
		return formErrorf("Settings can't be kept here.")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return m.Settings.SetSetting(ctx, photoSettingsKey(service), string(b))
}

// PhotoLocations are the Owner's additional locations for a service.
func (m *Manager) PhotoLocations(ctx context.Context, service string) []store.MediaLibrary {
	if m.Libraries == nil {
		return nil
	}
	libs, err := m.Libraries.MediaLibraries(ctx)
	if err != nil {
		m.Log.Error("reading media libraries", "err", err)
		return nil
	}
	return slices.DeleteFunc(libs, func(l store.MediaLibrary) bool { return l.PhotosFor != service })
}

// AddPhotoLocation adds a location for migrating photos into a service: a
// media library on its server that no service reads.
func (m *Manager) AddPhotoLocation(ctx context.Context, service string, req LibraryRequest) error {
	ps, err := m.photoService(service)
	if err != nil {
		return err
	}
	req.Server, req.PhotosFor, req.Services, req.AsOwner = ps.Host, service, nil, false
	return m.AddLibrary(ctx, req)
}

// RemovePhotoLocation removes one of a service's locations.
func (m *Manager) RemovePhotoLocation(ctx context.Context, service, name string) error {
	for _, l := range m.PhotoLocations(ctx, service) {
		if l.Name == name {
			return m.RemoveLibrary(ctx, l.ServerID, l.Name)
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
func (m *Manager) PhotoDrives(ctx context.Context, service string, admin bool) ([]PhotoDrive, error) {
	ps, err := m.photoService(service)
	if err != nil {
		return nil, err
	}
	media, err := m.libraryMedia(ps.Host)
	if err != nil {
		return nil, err
	}
	drives, err := media.Drives(ctx)
	if err != nil {
		return nil, err
	}
	libraryDrives := map[string]bool{}
	if m.Libraries != nil {
		if libs, err := m.Libraries.MediaLibraries(ctx); err == nil {
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
func (m *Manager) CheckPhotoSource(ctx context.Context, service string, src node.PhotoSource, admin bool) error {
	ps, err := m.photoService(service)
	if err != nil {
		return err
	}
	set := m.PhotoSettings(ctx, service)
	switch src.Kind {
	case "location":
		for _, l := range m.PhotoLocations(ctx, service) {
			if l.Name == src.Location && l.ServerID == ps.Host {
				return nil
			}
		}
		return formErrorf("That location isn't there any more: choose another.")
	case "drive":
		if !admin && set.NoDrives {
			return formErrorf("Drives can't be used here: choose one of the other places.")
		}
		drives, err := m.PhotoDrives(ctx, service, admin)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(drives, func(d PhotoDrive) bool { return d.UUID == src.Library.Drive })
		if i < 0 {
			return formErrorf("That drive isn't plugged in: plug it in and look again.")
		}
		if drives[i].Why != "" {
			return formErrorf("%s can't be used for migrating photos: choose another drive.", drives[i].Name)
		}
		return nil
	case "share":
		if !admin && set.NoShares {
			return formErrorf("Shares can't be used here: choose one of the other places.")
		}
		return nil
	case "folder":
		if !admin {
			return formErrorf("Only Owners and Admins can choose a folder on the server.")
		}
		return nil
	}
	return formErrorf("Choose where your export is.")
}
