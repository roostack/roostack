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

	"roostack.cloud/roostack/authkit"
	"roostack.cloud/roostack/internal/systemd"
	"roostack.cloud/roostack/internal/words"
)

// Adding an SSH key from the Remote access page (SPEC.md §6.10 item 3): the
// Owner pastes their public key, and the agent adds it to an account that
// can use sudo, a new one (with a password, for sudo and the server's own
// screen) or one the server has already. The agent's sandbox can't change
// accounts or read home folders (roostack-agent.service), so a short
// systemd job of its own does it, as /etc/fstab is replaced (fstab.go).

// AddSSHKeyCommand is roostack-agent's argument for AddSSHKey, which that
// job runs with the request on its standard input.
const AddSSHKeyCommand = "add-ssh-key"

// accountName is a name Debian's adduser takes without --allow-bad-names
// (NAME_REGEX in adduser.conf), at most 32 characters.
var accountName = regexp.MustCompile(`^[a-z][-a-z0-9_]{0,31}$`)

// Check says what's wrong with the request, in a sentence for the page:
// the key isn't one public key, the name isn't one an account can have,
// the new account exists already or its password is too short, or the
// account it has already can't use sudo.
func (r SSHKeyRequest) Check() error {
	if _, err := parseSSHKey(r.Key); err != nil {
		return err
	}
	if !accountName.MatchString(r.User) {
		return errors.New("An account's name must start with a small letter and have only small letters, digits, “-” and “_”, at most 32 of them.")
	}
	_, err := user.Lookup(r.User)
	exists := err == nil
	switch {
	case r.New && exists:
		return fmt.Errorf("This server already has an account called %s: choose another name, or add the key to that account.", r.User)
	case r.New && strings.ContainsAny(r.Password, "\n\r\x00"):
		return errors.New("The password can't have a line break in it.")
	case r.New:
		if err := authkit.CheckPasswordPolicy(r.Password); err != nil {
			return errors.New(words.Capitalise(err.Error()) + ".")
		}
	case !exists:
		return fmt.Errorf("This server has no account called %s.", r.User)
	case !sudoer(r.User):
		return fmt.Errorf("%s can't use sudo, so it couldn't change the server once passwords are off: choose another account, or a new one.", r.User)
	}
	return nil
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
	if key.Type() == ssh.KeyAlgoDSA {
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
// outside the agent's sandbox, with the request (and its password) on the
// job's standard input, never in its command line.
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

// AddSSHKey reads a request from in and does it: it makes the new account
// (in the sudo group, with a home folder, its password set by chpasswd),
// then adds the key to its ~/.ssh/authorized_keys unless it's there
// already. If setting the password fails, the new account is removed
// again. Run as root outside the agent's sandbox (LinuxHost.AddSSHKey).
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
	if r.New {
		if out, err := exec.Command("useradd", "--create-home", "--groups", "sudo", "--shell", "/bin/bash", "--", r.User).CombinedOutput(); err != nil {
			return fmt.Errorf("useradd: %w: %s", err, strings.TrimSpace(string(out)))
		}
		chpasswd := exec.Command("chpasswd")
		chpasswd.Stdin = strings.NewReader(r.User + ":" + r.Password + "\n")
		if out, err := chpasswd.CombinedOutput(); err != nil {
			exec.Command("userdel", "--remove", "--", r.User).Run()
			return fmt.Errorf("chpasswd: %w: %s", err, strings.TrimSpace(string(out)))
		}
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
