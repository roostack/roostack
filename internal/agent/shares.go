package agent

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/fsutil"
)

// Network shares as media libraries (SPEC.md §8.1, "Media libraries"): an
// SMB or NFS share is mounted read-only from fstab, the whole share being
// the library (choosing a folder in it would need its password at every
// step; the NAS can share just that folder). An SMB login is kept in a file
// only root can read, which the web pages can never show again; the
// controller doesn't keep the password. Before a share is saved the agent
// checks it: mounted read-only for a moment, its folders listed.

// Share is an SMB or NFS share.
type Share struct {
	// Type is "smb" or "nfs".
	Type string `json:"type"`
	// Server is the NAS's name or address; Path is the SMB share's name or
	// the NFS export's path.
	Server string `json:"server"`
	Path   string `json:"path"`
	// Username and Password log in to an SMB share (empty: as a guest).
	// The password is only sent to check and save the share.
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// ShareLoginDir keeps SMB logins, root's only.
const ShareLoginDir = "/etc/roostack/shares"

var (
	shareServer = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252}[A-Za-z0-9])?$|^\[[0-9A-Fa-f:]+\]$`)
	// An SMB share's name, which may have spaces; an NFS export's path.
	smbShareName = regexp.MustCompile(`^[^/\\:*?"<>|\x00-\x1f#,=]{1,80}$`)
	nfsExport    = regexp.MustCompile(`^(/[A-Za-z0-9._+-]+)+$|^/$`)
	shareUser    = regexp.MustCompile(`^[^\x00-\x1f,=]{1,100}$`)
)

// checkShare says what's wrong with a share, in words.
func checkShare(s Share) error {
	if !shareServer.MatchString(s.Server) {
		return fmt.Errorf("%q isn't a server's name or address", printable(s.Server))
	}
	switch s.Type {
	case "smb":
		if !smbShareName.MatchString(s.Path) || strings.TrimSpace(s.Path) != s.Path {
			return fmt.Errorf("%q isn't a share's name: just its name, e.g. media", printable(s.Path))
		}
		if s.Username != "" && !shareUser.MatchString(s.Username) {
			return errors.New("that username has characters a login can't")
		}
		if strings.ContainsAny(s.Password, "\r\n\x00") {
			return errors.New("that password has a line break in it")
		}
		if _, err := os.Stat("/usr/sbin/mount.cifs"); err != nil {
			if _, err := os.Stat("/sbin/mount.cifs"); err != nil {
				return errors.New("this server can't mount SMB shares yet: it needs the cifs-utils package")
			}
		}
	case "nfs":
		if !nfsExport.MatchString(s.Path) {
			return fmt.Errorf("%q isn't an NFS export: its path, e.g. /volume1/media", printable(s.Path))
		}
		if s.Username != "" || s.Password != "" {
			return errors.New("an NFS share has no login: it trusts the server's address")
		}
		if _, err := os.Stat("/usr/sbin/mount.nfs"); err != nil {
			if _, err := os.Stat("/sbin/mount.nfs"); err != nil {
				return errors.New("this server can't mount NFS shares yet: it needs the nfs-common package")
			}
		}
	default:
		return errors.New("a share is SMB or NFS")
	}
	return nil
}

// shareSource is what mount is given: //server/share or server:/export.
func shareSource(s Share) string {
	if s.Type == "nfs" {
		return s.Server + ":" + s.Path
	}
	return "//" + s.Server + "/" + s.Path
}

// shareOptions are a share's mount options: read-only, and for SMB every
// file Roostack's (it has no Unix owners to keep), readable, never
// writable; login is the credentials option ("" for a guest).
func (h LinuxHost) shareOptions(s Share, login string) string {
	opts := "ro,nosuid,nodev,noexec,nofail,_netdev,x-systemd.automount,x-systemd.mount-timeout=30s"
	if s.Type == "nfs" {
		return opts + ",soft,timeo=150,retrans=3"
	}
	if login == "" {
		opts += ",guest"
	} else {
		opts += ",credentials=" + login
	}
	return opts + fmt.Sprintf(",uid=%d,gid=%d,file_mode=0444,dir_mode=0555,noperm", h.UID, h.GID)
}

// CheckShare mounts a share read-only for a moment, to say it can be one:
// its folders, and how many files Roostack's user could read.
func (h LinuxHost) CheckShare(s Share) (FolderList, error) {
	if err := checkShare(s); err != nil {
		return FolderList{}, err
	}
	b := make([]byte, 6)
	rand.Read(b)
	id := hex.EncodeToString(b)
	login := ""
	if s.Type == "smb" && s.Username != "" {
		login = filepath.Join("/run/roostack-agent", "check-"+id+".cred")
		if err := writeLogin(login, s); err != nil {
			return FolderList{}, err
		}
		defer os.Remove(login)
	}
	mp := lookPrefix + "share-" + id
	opts := strings.NewReplacer(",nofail", "", ",_netdev", "", ",x-systemd.automount", "", ",x-systemd.mount-timeout=30s", "").
		Replace(h.shareOptions(s, login))
	fsType := map[string]string{"smb": "cifs", "nfs": "nfs"}[s.Type]
	cmd := exec.Command("systemd-mount", "--no-ask-password", "--collect", "-t", fsType, "-o", opts, shareSource(s), mp)
	out, err := runWithin(cmd, 40*time.Second)
	if err != nil {
		// systemd-mount says only that the job failed: mount's own words
		// are in the mount unit's log.
		if unit, uerr := mountUnit(mp); uerr == nil {
			if log, lerr := exec.Command("journalctl", "--no-pager", "-o", "cat", "-n", "10", "-u", unit).Output(); lerr == nil {
				out = append(out, log...)
			}
		}
		return FolderList{}, fmt.Errorf("Roostack couldn't open the share: %s", shareFailure(string(out)))
	}
	defer exec.Command("systemd-umount", mp).Run()
	return listFolders(mp, "", h.UID, h.groups())
}

// SaveShareLogin keeps an SMB share's login for a library, root's only.
func (h LinuxHost) SaveShareLogin(name string, s Share) error {
	if !ValidLibraryName(name) {
		return fmt.Errorf("%q can't name a library", printable(name))
	}
	if err := checkShare(s); err != nil {
		return err
	}
	if s.Type != "smb" || s.Username == "" {
		os.Remove(shareLoginFile(name))
		return nil
	}
	if err := os.MkdirAll(ShareLoginDir, 0o700); err != nil {
		return err
	}
	return writeLogin(shareLoginFile(name), s)
}

// shareLoginFile is where a library's SMB login is kept.
func shareLoginFile(name string) string { return filepath.Join(ShareLoginDir, name+".cred") }

// writeLogin writes mount.cifs's credentials file, root's only.
func writeLogin(path string, s Share) error {
	return fsutil.WritePrivate(path, fmt.Appendf(nil, "username=%s\npassword=%s\n", s.Username, s.Password))
}

// mountShare mounts a share library for good: its line in fstab, at the
// library's mount point, automounted.
func (h LinuxHost) mountShare(l Library, spot mountSpot) error {
	if h.UID == 0 {
		return errors.New("the roostack user isn't known")
	}
	s := *l.Share
	s.Password = "" // the login is in its file
	if err := checkShare(s); err != nil {
		return err
	}
	login := ""
	if s.Type == "smb" && s.Username != "" {
		login = spot.login
		if _, err := os.Stat(login); err != nil {
			return errors.New("the share's login isn't on this server: add the library again")
		}
	}
	target := spot.target
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	fsType := map[string]string{"smb": "cifs", "nfs": "nfs"}[s.Type]
	line, err := checkedFstabLine([]string{fstabEscape(shareSource(s)), target, fsType, h.shareOptions(s, login), "0", "0"})
	if err != nil {
		return err
	}
	unit, err := mountUnit(target)
	if err != nil {
		return err
	}
	automount := strings.TrimSuffix(unit, ".mount") + ".automount"
	if err := editFstab(func(tab string) (string, error) {
		exec.Command("systemctl", "stop", automount, unit).Run()
		tab = removeBlock(removeBlock(tab, spot.folderTag), spot.shareTag)
		return withBlock(tab, spot.shareTag, line), nil
	}); err != nil {
		return err
	}
	// The automount only: the share is mounted when a service first looks,
	// so a NAS that's off doesn't hold anything up.
	if out, err := exec.Command("systemctl", "start", automount).CombinedOutput(); err != nil {
		return fmt.Errorf("mounting it: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// shareTag marks a share library's line in fstab.
func shareTag(name string) string { return "# Added by Roostack: media library " + name + " (a share)" }

// shareEntries reads the share libraries' lines in fstab.
func shareEntries(tab string) []libraryEntry {
	var out []libraryEntry
	lines := strings.Split(tab, "\n")
	for i, l := range lines {
		rest, ok := strings.CutPrefix(l, "# Added by Roostack: media library ")
		name, isShare := strings.CutSuffix(rest, " (a share)")
		if !ok || !isShare || i+1 >= len(lines) {
			continue
		}
		if f := strings.Fields(lines[i+1]); len(f) >= 4 {
			out = append(out, libraryEntry{name: name, source: FstabUnescape(f[0]), share: true})
		}
	}
	return out
}

// shareFailure says why mounting a share failed, from mount's words.
func shareFailure(out string) string {
	switch {
	case strings.Contains(out, "Permission denied") || strings.Contains(out, "error(13)"):
		return "the NAS refused the login (check the username and password, and that the account may open this share)"
	case strings.Contains(out, "No such file or directory") || strings.Contains(out, "error(2)"):
		return "the NAS has no share by that name"
	case strings.Contains(out, "Host is down") || strings.Contains(out, "error(112)") || strings.Contains(out, "No route to host") ||
		strings.Contains(out, "Connection refused") || strings.Contains(out, "timed out") || strings.Contains(out, "could not resolve"):
		return "the NAS didn't answer (check its name or address, and that it's on)"
	case strings.Contains(out, "access denied by server"):
		return "the NAS doesn't let this server mount the export (allow its address in the NAS's NFS settings)"
	}
	return strings.TrimSpace(out)
}

// runWithin runs a command, giving up after d.
func runWithin(cmd *exec.Cmd, d time.Duration) ([]byte, error) {
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
		return out, err
	case <-time.After(d):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return []byte("timed out"), errors.New("timed out")
	}
}
