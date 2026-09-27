package generate

import (
	"fmt"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/quadlet"
)

// A machine can offer its disk for backups over MyLab's network (SPEC.md
// §10.10) with restic's rest-server, in two containers sharing one folder:
//   - the store, append-only: machines back up to it, each only to its own
//     repositories (--private-repos), so a compromised machine can add
//     backups but not delete old ones;
//   - the admin one, only for the controller, which can also delete (to
//     prune old backups).

// Storage ports on the machine's address on MyLab's network.
const (
	StoragePort      = 8000
	StorageAdminPort = 8001
)

// RestServerImage is restic's rest-server. TODO: pin by digest.
const RestServerImage = "docker.io/restic/rest-server:0.14.0"

// Storage unit names.
const (
	StorageUnit      = "mylab-backup-store"
	StorageAdminUnit = "mylab-backup-store-admin"
)

// Password files in the store's folder.
const (
	StorePasswords = ".htpasswd"
	AdminPasswords = ".htpasswd-admin"
)

// Storage generates the rest-server units for a machine at addr (its
// address on MyLab's network).
func Storage(lab config.Lab, addr string) ([]File, []string, error) {
	var files []File
	for _, s := range []struct {
		unit, title string
		port        int
		args        string
	}{
		{StorageUnit, "backups other machines store here", StoragePort,
			"--append-only --private-repos --htpasswd-file /data/" + StorePasswords},
		{StorageAdminUnit, "the controller's access to stored backups", StorageAdminPort,
			"--htpasswd-file /data/" + AdminPasswords},
	} {
		var u quadlet.Unit
		u.Section("Unit").Add("Description", "MyLab: "+s.title+" (rest-server)")
		cs := u.Section("Container")
		cs.Add("ContainerName", s.unit)
		cs.Add("Image", RestServerImage)
		addTimezone(cs, lab)
		// Only on MyLab's network; the firewall lets in only that interface.
		cs.Add("PublishPort", fmt.Sprintf("%s:%d:8000", addr, s.port))
		cs.Add("Volume", lab.Paths.BackupStoreDir()+":/data")
		cs.Add("Exec", "rest-server --path /data --listen :8000 "+s.args)
		cs.Add("Label", "io.mylab.service=_system")
		cs.Add("Label", "io.mylab.component=backup-store")
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
