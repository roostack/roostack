package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// SSH hardening (SPEC.md §6.10 item 3): keys only, no passwords, no root.
// Roostack owns one drop-in in sshd's own folder and nothing else of sshd's
// configuration. Debian's sshd_config includes sshd_config.d/*.conf at its
// top and sshd keeps the first value it reads, so "10-" comes before the
// cloud images' 50-cloud-init.conf (docs/ASSUMPTIONS.md J3).
const (
	SSHDropIn = "/etc/ssh/sshd_config.d/10-roostack.conf"
	sshdBin   = "/usr/sbin/sshd"
)

// sshDropIn is the drop-in's whole content: fixed, so callers only say
// whether it's there.
const sshDropIn = `# Written by Roostack: SSH takes keys only, and nobody logs in as root
# (SPEC.md §6.10). Change it in Roostack, under Settings, Remote access.
# Roostack checks every change with "sshd -t" before reloading SSH.
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin no
`

// sshdExes are the programs that log sshd's logins: sshd-session since
// OpenSSH 9.8 (Debian 13), sshd itself before.
var sshdExes = []string{"/usr/lib/openssh/sshd-session", sshdBin}

// SSHStatus is what this server's SSH server does now.
type SSHStatus struct {
	// Installed: there is an SSH server (openssh-server). Without one,
	// there's nothing to harden.
	Installed bool `json:"installed"`
	// KeysOnly: Roostack's drop-in is there.
	KeysOnly bool `json:"keys_only,omitempty"`
	// Passwords: sshd takes passwords (password or keyboard-interactive
	// authentication), whoever turned them on, as `sshd -T` says.
	Passwords bool `json:"passwords,omitempty"`
	// RootLogin is sshd's PermitRootLogin ("no", "prohibit-password",
	// "yes"...); "without-password" is read as "prohibit-password".
	RootLogin string `json:"root_login,omitempty"`
	// KeyLogins are the latest key login of each account sshd logged.
	KeyLogins []KeyLogin `json:"key_logins,omitempty"`
	// Error says why it couldn't all be read (a broken configuration makes
	// `sshd -T` fail); what was read is still there.
	Error string `json:"error,omitempty"`
}

// KeyLogin is an account's latest login with an SSH key.
type KeyLogin struct {
	User string    `json:"user"`
	From string    `json:"from"`
	At   time.Time `json:"at"`
	// Sudo: the account isn't root, still exists, and is in the sudo group,
	// so it can still get root with keys only and no root logins.
	Sudo bool `json:"sudo,omitempty"`
}

// SudoKeyLogin returns the latest key login by an account that can use
// sudo: the proof that someone holds a key that keeps working once
// passwords and root logins are off.
func (s SSHStatus) SudoKeyLogin() (KeyLogin, bool) {
	for _, l := range s.KeyLogins {
		if l.Sudo {
			return l, true
		}
	}
	return KeyLogin{}, false
}

// SSHStatus reads sshd's settings (`sshd -T`) and its key logins.
func (h LinuxHost) SSHStatus() (SSHStatus, error) {
	if _, err := os.Stat(sshdBin); errors.Is(err, fs.ErrNotExist) {
		return SSHStatus{}, nil
	}
	st := SSHStatus{Installed: true}
	if _, err := os.Stat(SSHDropIn); err == nil {
		st.KeysOnly = true
	}
	var stderr bytes.Buffer
	cmd := exec.Command(sshdBin, "-T")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return st, fmt.Errorf("sshd -T: %w: %s", err, sshdOutput(stderr.Bytes()))
	}
	st.Passwords, st.RootLogin = sshdSettings(out)
	st.KeyLogins, err = keyLogins()
	return st, err
}

// sshdSettings reads `sshd -T`'s output: whether passwords are taken, and
// PermitRootLogin.
func sshdSettings(out []byte) (passwords bool, root string) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		key, value, _ := strings.Cut(sc.Text(), " ")
		switch key {
		case "passwordauthentication", "kbdinteractiveauthentication":
			passwords = passwords || value == "yes"
		case "permitrootlogin":
			root = value
			if root == "without-password" {
				root = "prohibit-password"
			}
		}
	}
	return passwords, root
}

// keyLogins reads sshd's "Accepted publickey" lines from the journal,
// newest first, keeping each account's latest. Only lines journald says
// came from root running sshd's own program count: any user can log a
// line that claims to be sshd's (logger -t sshd-session), but not set
// _UID or _EXE, which journald fills in itself (docs/ASSUMPTIONS.md J3).
func keyLogins() ([]KeyLogin, error) {
	args := []string{"_UID=0"}
	for _, exe := range sshdExes {
		args = append(args, "_EXE="+exe)
	}
	args = append(args, "--grep", "^Accepted publickey for ", "--reverse", "--lines", "500",
		"--output", "json", "--output-fields", "MESSAGE,__REALTIME_TIMESTAMP", "--no-pager", "--quiet")
	out, err := exec.Command("journalctl", args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && len(bytes.TrimSpace(out)) == 0 {
		return nil, nil // no key logins logged
	}
	if err != nil {
		return nil, fmt.Errorf("journalctl: %w", err)
	}
	return parseKeyLogins(out, sudoer), nil
}

// acceptedRe matches sshd's line for a key login.
var acceptedRe = regexp.MustCompile(`^Accepted publickey for ([a-z_][a-z0-9_.-]{0,31}\$?) from (\S+) port \d+`)

// parseKeyLogins reads journalctl's JSON lines (newest first), keeping the
// first of each account, at most 20. canSudo says whether an account can
// use sudo.
func parseKeyLogins(out []byte, canSudo func(name string) bool) []KeyLogin {
	var logins []KeyLogin
	seen := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() && len(logins) < 20 {
		var e struct {
			Message string `json:"MESSAGE"`
			At      string `json:"__REALTIME_TIMESTAMP"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		m := acceptedRe.FindStringSubmatch(e.Message)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		usec, _ := strconv.ParseInt(e.At, 10, 64)
		logins = append(logins, KeyLogin{User: m[1], From: m[2], At: time.UnixMicro(usec).UTC(), Sudo: canSudo(m[1])})
	}
	return logins
}

// sudoer reports whether an account exists, isn't root, and is in the sudo
// group (Debian's way to give an account sudo; the cloud images' default
// user is in it too).
func sudoer(name string) bool {
	u, err := user.Lookup(name)
	if err != nil || u.Uid == "0" {
		return false
	}
	g, err := user.LookupGroup("sudo")
	if err != nil {
		return false
	}
	ids, err := u.GroupIds()
	return err == nil && slices.Contains(ids, g.Gid)
}

// ErrNoSSHServer: the server has no SSH server to change.
var ErrNoSSHServer = errors.New("this server has no SSH server (openssh-server isn't installed)")

// SetSSHKeysOnly writes or removes Roostack's drop-in, checks the whole
// configuration with `sshd -t`, and reloads SSH. If sshd refuses it, the
// drop-in is put back as it was and SSH isn't reloaded, so it keeps
// running with its old settings. Logged-in sessions stay either way.
func (h LinuxHost) SetSSHKeysOnly(on bool) error {
	if _, err := os.Stat(sshdBin); err != nil {
		return ErrNoSSHServer
	}
	old, err := os.ReadFile(SSHDropIn)
	had := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if on == had && (!on || string(old) == sshDropIn) {
		return nil
	}
	put := func(on bool, content []byte) error {
		if !on {
			return os.Remove(SSHDropIn)
		}
		// Written beside it under a name sshd doesn't include (not *.conf),
		// then renamed: sshd never reads half a file.
		tmp := filepath.Join(filepath.Dir(SSHDropIn), "."+filepath.Base(SSHDropIn)+".tmp")
		if err := os.WriteFile(tmp, content, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, SSHDropIn)
	}
	if err := put(on, []byte(sshDropIn)); err != nil {
		return err
	}
	if out, err := exec.Command(sshdBin, "-t").CombinedOutput(); err != nil {
		if rerr := put(had, old); rerr != nil {
			return fmt.Errorf("sshd -t refused the change (%s), and putting the old file back failed: %w",
				sshdOutput(out), rerr)
		}
		return fmt.Errorf("sshd -t refused the change, so SSH keeps its old settings: %s", sshdOutput(out))
	}
	// Reloads a running sshd (its ExecReload runs sshd -t again). Without
	// one (socket activation), each connection reads the files anyway.
	if out, err := exec.Command("systemctl", "try-reload-or-restart", "ssh.service").CombinedOutput(); err != nil {
		return fmt.Errorf("reloading SSH: %w: %s", err, out)
	}
	return nil
}

// sshdOutput is sshd's complaint on one line: it ends its lines with
// "\r\n" when it has no terminal.
func sshdOutput(out []byte) string {
	var lines []string
	for line := range strings.Lines(string(out)) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "; ")
}
