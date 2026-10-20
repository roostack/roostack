package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"roostack.cloud/roostack/internal/config"
)

// Bringing your own data into a service (SPEC.md §8.4): the person's copy
// of a service's files (on a drive, a share or a folder on the server) is
// mounted read-only for Roostack to read and copy, one place per service,
// as a media library is: so the original can be the source and can't be
// changed even by mistake. It's in fstab, automounted, so a copy carries
// on after a restart, until the import is done and it's unmounted.

// ImportRoot holds the places data is brought in from, one folder per
// service: root's, like LibraryRoot.
const ImportRoot = config.ImportRoot

// importTag marks a place to bring data in from in fstab.
func importTag(service string) string { return "# Added by Roostack: bringing data into " + service }

// importSpot is a service's place to bring data in from.
func importSpot(service string) mountSpot {
	return mountSpot{target: filepath.Join(ImportRoot, service), folderTag: importTag(service), shareTag: importTag(service),
		login: filepath.Join(ShareLoginDir, "import-"+service+".cred")}
}

// MountImport mounts the place to bring data into a service from (a
// library's place: a folder on a drive or the server, or a share with its
// login, which is kept root's only until it's unmounted), read-only at
// ImportRoot/<service>, replacing what was there. It returns the path.
func (h LinuxHost) MountImport(service string, l Library) (string, error) {
	if !serviceName.MatchString(service) {
		return "", fmt.Errorf("%q isn't a service's name", printable(service))
	}
	spot := importSpot(service)
	if l.Share != nil {
		if err := checkShare(*l.Share); err != nil {
			return "", err
		}
		os.Remove(spot.login)
		if l.Share.Type == "smb" && l.Share.Username != "" {
			if err := os.MkdirAll(ShareLoginDir, 0o700); err != nil {
				return "", err
			}
			if err := writeLogin(spot.login, *l.Share); err != nil {
				return "", err
			}
		}
	}
	if err := h.mountAt(l, spot); err != nil {
		return "", err
	}
	return spot.target, nil
}

// UnmountImport unmounts a service's place to bring data in from and
// forgets it, with its login; a drive mounted for it goes too once nothing
// else mounts from it.
func (h LinuxHost) UnmountImport(service string) error {
	if !serviceName.MatchString(service) {
		return fmt.Errorf("%q isn't a service's name", printable(service))
	}
	spot := importSpot(service)
	stopMount(spot.target)
	data, err := os.ReadFile(fstab)
	if err != nil {
		return err
	}
	tab := pruneMediaDrives(removeBlock(string(data), spot.folderTag))
	if err := os.WriteFile(fstab, []byte(tab), 0o644); err != nil {
		return err
	}
	exec.Command("systemctl", "daemon-reload").Run()
	os.Remove(spot.login)
	if err := os.Remove(spot.target); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Still mounted (busy): it goes once it's free and unmounted.
		return fmt.Errorf("unmounting %s: %s", spot.target, strings.TrimPrefix(err.Error(), "remove "+spot.target+": "))
	}
	return nil
}
