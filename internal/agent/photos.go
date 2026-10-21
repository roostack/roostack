package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
)

// Migrating photos into Immich (SPEC.md §8.5): a person's export (on a
// drive, a share or a folder on the server) is mounted read-only for
// Roostack to read and immich-go to send, one place per service and
// person, as a place to bring data in from is (import.go). It's in
// fstab, automounted, so a migration carries on after Roostack restarts,
// until it's done or forgotten and unmounted.

// whoName is a Roostack account's name, as auth.ValidUsername allows.
var whoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// PhotoSpotName is the folder under ImportRoot where a person's export
// for a service is: the dot keeps it apart from a service's own place
// (service names have none).
func PhotoSpotName(service, who string) string { return service + ".photos." + who }

// photoTag marks a person's export in fstab.
func photoTag(service, who string) string {
	return "# Added by Roostack: migrating photos into " + service + " for " + who
}

// photoSpot is a person's place to migrate photos from.
func photoSpot(service, who string) mountSpot {
	tag := photoTag(service, who)
	return mountSpot{target: filepath.Join(ImportRoot, PhotoSpotName(service, who)), folderTag: tag, shareTag: tag,
		login: filepath.Join(ShareLoginDir, "photos-"+service+"-"+who+".cred")}
}

// checkPhotoNames refuses a service or person that isn't one.
func checkPhotoNames(service, who string) error {
	if !serviceName.MatchString(service) {
		return fmt.Errorf("%q isn't a service's name", printable(service))
	}
	if !whoName.MatchString(who) {
		return fmt.Errorf("%q isn't an account's name", printable(who))
	}
	return nil
}

// MountPhotos mounts a person's export for migrating photos into a
// service, read-only, replacing what was there, and says where.
func (h LinuxHost) MountPhotos(service, who string, l Library) (string, error) {
	if err := checkPhotoNames(service, who); err != nil {
		return "", err
	}
	return h.mountPlace(photoSpot(service, who), l)
}

// UnmountPhotos unmounts a person's export and forgets it, with its login.
func (h LinuxHost) UnmountPhotos(service, who string) error {
	if err := checkPhotoNames(service, who); err != nil {
		return err
	}
	return unmountPlace(photoSpot(service, who))
}
