package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"

	"roostack.cloud/roostack/internal/systemd"
)

// Adding an SSH key from the Remote access page (SPEC.md §6.10 item 3): the
// Owner pastes their public key, and the agent adds it to an account the
// server has already, one that can use sudo but isn't root. It never makes
// an account or sets a password: anything that can reach the agent (the
// roostack user, so a container that escapes, and the controller for a
// node) could then make itself a sudo account it knows the password of.
// Making one stays a command the Owner runs as root, which the page shows.
// The agent's sandbox can't read home folders (roostack-agent.service), so
// a short systemd job of its own adds the key, as /etc/fstab is replaced
// (fstab.go).

// AddSSHKeyCommand is roostack-agent's argument for AddSSHKey, which that
// job runs with the request on its standard input.
const AddSSHKeyCommand = "add-ssh-key"

// accountName is a name Debian's adduser takes without --allow-bad-names
// (NAME_REGEX in adduser.conf), at most 32 characters.
var accountName = regexp.MustCompile(`^[a-z][-a-z0-9_]{0,31}$`)

// Check says what's wrong with the request, in a sentence for the page:
// the key isn't one public key, the name isn't one an account can have,
// the server has no such account, or the account can't use sudo (root
// can't either, here).
func (r SSHKeyRequest) Check() error {
	if _, err := parseSSHKey(r.Key); err != nil {
		return err
	}
	if !accountName.MatchString(r.User) {
		return errors.New("An account's name must start with a small letter and have only small letters, digits, “-” and “_”, at most 32 of them.")
	}
	if _, err := user.Lookup(r.User); err != nil {
		return fmt.Errorf("This server has no account called %s: make it first, as root, with the commands on this page.", r.User)
	}
	if !sudoer(r.User) {
		return fmt.Errorf("%s can't use sudo, so it couldn't change the server once passwords are off: choose another account, or make a new one.", r.User)
	}
	return nil
}

// SSHKeyFingerprint is the fingerprint of text as one public key
// ("SHA256:…", as ssh-keygen -l shows it), or what's wrong with it in a
// sentence for the page, which can then say so before it asks for the
// Owner's password and code.
func SSHKeyFingerprint(text string) (string, error) {
	key, err := parseSSHKey(text)
	if err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(key), nil
}

// parseSSHKey reads one public key, as a line of authorized_keys, and
// refuses a private key, more than one key, and DSA keys (which OpenSSH
// no longer takes).
func parseSSHKey(text string) (ssh.PublicKey, error) {
	text = strings.TrimSpace(text)
	if strings.Contains(text, "PRIVATE KEY") {
		return nil, errors.New("That's a private key, which must never leave your computer: paste the public key, from the file ending in .pub, and make a new key, since this one may have been seen.")
	}
	if text == "" {
		return nil, errors.New("Paste your public key: the one line from the file ending in .pub.")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(text))
	if err != nil {
		return nil, errors.New("That isn't an SSH public key: it should be one line starting with ssh-ed25519 or ssh-rsa, from the file ending in .pub.")
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, errors.New("Paste one key at a time.")
	}
	if len(options) > 0 {
		return nil, errors.New("Paste the key without options in front of it: from ssh-ed25519 or ssh-rsa on.")
	}
	if key.Type() == "ssh-dss" { // ssh.KeyAlgoDSA, deprecated
		return nil, errors.New("That's a DSA key, which SSH no longer accepts: make an ed25519 key instead.")
	}
	return key, nil
}

// authorizedKeyLine is the key as written in authorized_keys, with its
// comment (often who made it, and on which computer).
func authorizedKeyLine(text string) (string, error) {
	key, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(text)))
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	if comment = strings.Join(strings.Fields(comment), " "); comment != "" {
		line += " " + comment
	}
	return line, nil
}

// AddSSHKey checks the request and runs AddSSHKeyCommand in a systemd job
// outside the agent's sandbox, with the request on the job's standard
// input.
func (h LinuxHost) AddSSHKey(r SSHKeyRequest) error {
	if err := r.Check(); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	in, err := json.Marshal(r)
	if err != nil {
		return err
	}
	cmd := exec.Command("systemd-run", "--unit=roostack-ssh-key", "--wait", "--pipe", "--quiet", "--collect", systemd.NoExpand,
		"--", exe, AddSSHKeyCommand)
	cmd.Stdin = bytes.NewReader(in)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AddSSHKey reads a request from in, checks it again, and adds the key to
// the account's ~/.ssh/authorized_keys unless it's there already. Run as
// root outside the agent's sandbox (LinuxHost.AddSSHKey).
func AddSSHKey(in io.Reader) error {
	var r SSHKeyRequest
	if err := json.NewDecoder(in).Decode(&r); err != nil {
		return err
	}
	if err := r.Check(); err != nil {
		return err
	}
	line, err := authorizedKeyLine(r.Key)
	if err != nil {
		return err
	}
	u, err := user.Lookup(r.User)
	if err != nil {
		return err
	}
	return addAuthorizedKey(u, line)
}

// addAuthorizedKey appends line to the account's ~/.ssh/authorized_keys,
// making the folder (700) and the file (600), owned by the account, as
// sshd's StrictModes wants. A link in their place is refused rather than
// followed: the account can use sudo anyway, but root shouldn't be the
// one to write wherever it points.
func addAuthorizedKey(u *user.User, line string) error {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	dir := filepath.Join(u.HomeDir, ".ssh")
	if err := os.Mkdir(dir, 0o700); err == nil {
		if err := os.Chown(dir, uid, gid); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	if fi, err := os.Lstat(dir); err != nil {
		return err
	} else if !fi.IsDir() {
		return fmt.Errorf("%s isn't a folder", dir)
	}
	file := filepath.Join(dir, "authorized_keys")
	f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	defer f.Close()
	have, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	fields := strings.Fields(line)
	for l := range strings.Lines(string(have)) {
		// The same key, whatever its comment or options.
		if f := strings.Fields(l); len(f) >= 2 && slicesContainsPair(f, fields[0], fields[1]) {
			return nil
		}
	}
	if len(have) > 0 && have[len(have)-1] != '\n' {
		line = "\n" + line
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		return err
	}
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	return f.Sync()
}

// slicesContainsPair reports whether a and then b are next to each other in
// fields: a key's type and its base64, after any options.
func slicesContainsPair(fields []string, a, b string) bool {
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == a && fields[i+1] == b {
			return true
		}
	}
	return false
}
