package generate

import (
	"fmt"
	"path/filepath"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/quadlet"
)

// A server can offer its disk for backups over Roostack's network (SPEC.md
// §10.10) with restic's rest-server, in two containers sharing one folder:
//   - the store, append-only: servers back up to it, each only to its own
//     repositories (--private-repos), so a compromised server can add
//     backups but not delete old ones;
//   - the admin one, only for the controller, which can also delete (to
//     prune old backups).

// Storage ports on the server's address on Roostack's network.
const (
	StoragePort      = 8000
	StorageAdminPort = 8001
)

// RestServerImage is restic's rest-server, pinned by tag and digest (the
// multi-arch index) like every image (SPEC.md §8.1).
const RestServerImage = "docker.io/restic/rest-server:0.14.0@sha256:d2aff06f47eb38637dff580c3e6bce4af98f386c396a25d32eb6727ec96214a5"

// Storage unit names.
const (
	StorageUnit      = "roostack-backup-store"
	StorageAdminUnit = "roostack-backup-store-admin"
)

// Password files in the store's folder.
const (
	StorePasswords = ".htpasswd"
	AdminPasswords = ".htpasswd-admin"
)

// Storage generates the rest-server units for a server at addr (its
// address on Roostack's network).
func Storage(lab config.Lab, addr string) ([]File, []string, error) {
	var files []File
	for _, s := range []struct {
		unit, title string
		port        int
		args        string
	}{
		{StorageUnit, "backups other servers store here", StoragePort,
			"--append-only --private-repos --htpasswd-file /data/" + StorePasswords},
		{StorageAdminUnit, "the controller's access to stored backups", StorageAdminPort,
			"--htpasswd-file /data/" + AdminPasswords},
	} {
		var u quadlet.Unit
		u.Section("Unit").Add("Description", "Roostack: "+s.title+" (rest-server)")
		cs := u.Section("Container")
		cs.Add("ContainerName", s.unit)
		cs.Add("Image", RestServerImage)
		addTimezone(cs, lab)
		// Only on Roostack's network; the firewall lets in only that interface.
		cs.Add("PublishPort", fmt.Sprintf("%s:%d:8000", addr, s.port))
		cs.Add("Volume", lab.Paths.BackupStoreDir()+":/data")
		cs.Add("Exec", "rest-server --path /data --listen :8000 "+s.args)
		cs.Add("Label", "io.roostack.service=_system")
		cs.Add("Label", "io.roostack.component=backup-store")
		svc := u.Section("Service")
		svc.Add("Restart", "always")
		u.Section("Install").Add("WantedBy", "default.target")
		if err := u.Validate(); err != nil {
			return nil, nil, err
		}
		files = append(files, File{Name: s.unit + ".container", Content: u.String()})
	}
	return files, []string{StorageUnit + ".service", StorageAdminUnit + ".service"}, nil
}

// GuestStorage is one other lab's space on this server (SPEC.md §10.10):
// its own folder, size limit and ports on the guests' WireGuard link.
type GuestStorage struct {
	User  string `json:"user"` // rest-server user, e.g. g3
	Port  int    `json:"port"` // its store; admin access on Port+1
	Quota int64  `json:"quota"`
	// Addr is its address on the guests' link: only it reaches its store
	// (the firewall, SPEC.md §6.10).
	Addr string `json:"addr,omitempty"`
	// StoreHash and AdminHash are bcrypt hashes of its passwords.
	StoreHash string `json:"store_hash"`
	AdminHash string `json:"admin_hash"`
}

// GuestStoreDir is where a guest lab's backups are kept.
func GuestStoreDir(lab config.Lab, user string) string {
	return filepath.Join(lab.Paths.BackupStoreDir(), "guests", user)
}

// GuestStorageUnits generates a guest's two rest-server units at addr
// (this lab's address on the guests' link).
func GuestStorageUnits(lab config.Lab, addr string, g GuestStorage) ([]File, []string, error) {
	var files []File
	var units []string
	for _, s := range []struct {
		unit, title string
		port        int
		args        string
	}{
		{"roostack-guest-" + g.User, "backups another lab stores here", g.Port,
			"--append-only --private-repos --htpasswd-file /data/" + StorePasswords},
		{"roostack-guest-" + g.User + "-admin", "another lab's own access to its backups here", g.Port + 1,
			"--htpasswd-file /data/" + AdminPasswords},
	} {
		var u quadlet.Unit
		u.Section("Unit").Add("Description", "Roostack: "+s.title+" (rest-server)")
		cs := u.Section("Container")
		cs.Add("ContainerName", s.unit)
		cs.Add("Image", RestServerImage)
		addTimezone(cs, lab)
		cs.Add("PublishPort", fmt.Sprintf("%s:%d:8000", addr, s.port))
		cs.Add("Volume", GuestStoreDir(lab, g.User)+":/data")
		cs.Add("Exec", fmt.Sprintf("rest-server --path /data --listen :8000 --max-size %d %s", g.Quota, s.args))
		cs.Add("Label", "io.roostack.service=_system")
		cs.Add("Label", "io.roostack.component=guest-store")
		u.Section("Service").Add("Restart", "always")
		u.Section("Install").Add("WantedBy", "default.target")
		if err := u.Validate(); err != nil {
			return nil, nil, err
		}
		files = append(files, File{Name: s.unit + ".container", Content: u.String()})
		units = append(units, s.unit+".service")
	}
	return files, units, nil
}
