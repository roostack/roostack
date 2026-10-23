package agent

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/trust"
)

// SSH hardening (SPEC.md §6.10 item 3): keys only, no passwords, no root.
// Roostack owns one drop-in in sshd's own folder and nothing else of sshd's
// configuration. Debian's sshd_config includes sshd_config.d/*.conf at its
// top and sshd keeps the first value it reads, so "10-" comes before the
// cloud images' 50-cloud-init.conf (docs/ASSUMPTIONS.md J3).
const (
	SSHDropIn = "/etc/ssh/sshd_config.d/10-roostack.conf"
	sshdBin   = "/usr/sbin/sshd"
	// sshdConfig is sshd's main file; Match blocks are read from it and
	// the files it includes.
	sshdConfig = "/etc/ssh/sshd_config"
	// sshdRunDir is sshd's privilege separation folder, which `sshd -t`
	// and `sshd -T` need: ssh.service's RuntimeDirectory, so it's gone
	// while a socket-activated sshd waits for its first connection
	// (docs/ASSUMPTIONS.md J4).
	sshdRunDir = "/run/sshd"
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
	// authentication) from the home network, whoever turned them on, as
	// `sshd -T` says: everywhere, or in a Match block for a home address
	// (trust.IsHome), or Roostack's network.
	Passwords bool `json:"passwords,omitempty"`
	// PasswordsOutside: a Match block turns passwords on for addresses
	// outside the home network only (it matters once SSH is open to them).
	PasswordsOutside bool `json:"passwords_outside,omitempty"`
	// PasswordsMatch is the first Match block that turns passwords on,
	// e.g. "Match Address 192.168.7.0/24".
	PasswordsMatch string `json:"passwords_match,omitempty"`
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

// SSHStatus reads sshd's settings (`sshd -T`, and `sshd -T -C` for each
// connection its Match blocks may treat differently) and its key logins.
func (h LinuxHost) SSHStatus() (SSHStatus, error) {
	if _, err := os.Stat(sshdBin); errors.Is(err, fs.ErrNotExist) {
		return SSHStatus{}, nil
	}
	st := SSHStatus{Installed: true}
	if _, err := os.Stat(SSHDropIn); err == nil {
		st.KeysOnly = true
	}
	// Without its folder (a socket-activated sshd that isn't running),
	// sshd -T can't run; sshd -G prints the same settings without it, but
	// can't say what a Match block does for a connection.
	running := true
	if _, err := os.Stat(sshdRunDir); errors.Is(err, fs.ErrNotExist) {
		running = false
	}
	out, err := sshd(map[bool]string{true: "-T", false: "-G"}[running])
	if err != nil && running && strings.Contains(err.Error(), "not in connection test specification") {
		// A Match LocalPort or LocalAddress makes sshd want a connection
		// even for its global settings: one no block names, then.
		neutral := netip.MustParseAddr("192.0.2.1") // documentation (RFC 5737)
		out, err = sshd("-T", "-C", testConn{user: sshdNoUser, addr: neutral, laddr: netip.IPv4Unspecified(), lport: 22}.spec())
	}
	if err != nil {
		return st, err
	}
	st.Passwords, st.RootLogin = sshdSettings(out)
	matches, err := matchBlocks(sshdConfig)
	switch {
	case err != nil:
		return st, fmt.Errorf("reading SSH's Match blocks: %w", err)
	case len(matches) > 0 && !running:
		return st, errors.New("SSH is started by its socket (ssh.socket) and isn't running now, so Roostack " +
			"couldn't check what its Match blocks do")
	}
	// Which Match block turns passwords on, when they're off otherwise:
	// one made for a block names it.
	global := st.Passwords
	for _, c := range testConnections(matches) {
		out, err := sshd("-T", "-C", c.spec())
		if err != nil {
			return st, err
		}
		if passwords, _ := sshdSettings(out); !passwords || global {
			continue
		}
		if trust.IsHome(c.addr) || trust.ServersRange.Contains(c.addr) {
			st.Passwords = true
		} else {
			st.PasswordsOutside = true
		}
		if c.match != "" && (st.PasswordsMatch == "" || st.PasswordsMatch == someMatch) {
			st.PasswordsMatch = c.match
		} else if st.PasswordsMatch == "" {
			st.PasswordsMatch = someMatch
		}
	}
	if st.PasswordsMatch == someMatch && len(matches) == 1 {
		st.PasswordsMatch = matches[0]
	}
	st.KeyLogins, err = keyLogins()
	return st, err
}

// sshd runs sshd to print its settings.
func sshd(args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.Command(sshdBin, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("sshd %s: %w: %s", strings.Join(args, " "), err, sshdOutput(stderr.Bytes()))
	}
	return out, nil
}

// matchBlocks returns the Match lines in sshd's configuration: the main
// file and every file it includes, in order.
func matchBlocks(file string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	var read func(file string, depth int) error
	read = func(file string, depth int) error {
		if seen[file] || depth > 8 {
			return nil
		}
		seen[file] = true
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		for line := range strings.Lines(string(data)) {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			switch strings.ToLower(f[0]) {
			case "match":
				out = append(out, strings.Join(f, " "))
			case "include":
				for _, pattern := range f[1:] {
					if !filepath.IsAbs(pattern) {
						pattern = filepath.Join(filepath.Dir(sshdConfig), pattern)
					}
					files, _ := filepath.Glob(pattern)
					for _, inc := range files {
						if err := read(inc, depth+1); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}
	return out, read(file, 0)
}

// testConn is a connection to ask sshd about (`sshd -T -C`): from user
// at addr, to laddr:lport (sshd refuses a Match LocalAddress or LocalPort
// without them).
type testConn struct {
	user        string
	addr, laddr netip.Addr
	lport       int
	match       string // the Match block it was made for, or ""
}

func (c testConn) spec() string {
	return fmt.Sprintf("user=%s,host=%s,addr=%s,laddr=%s,lport=%d", c.user, c.addr, c.addr, c.laddr, c.lport)
}

// sshdNoUser is a user name nobody has, for Match blocks that name users.
const sshdNoUser = "roostack-check"

// someMatch names the Match block that turns passwords on when Roostack
// can't tell which of several it is.
const someMatch = "one of its Match blocks"

// testConnections are the connections whose settings a Match block may
// change: from an address in each home range, Roostack's network and the
// internet, and from each address and user a block's criteria name (a
// range's first address, a group's members), each from every address, to
// port 22 or a local address or port a block names. A connection made for
// a block names it. None without Match blocks.
func testConnections(matches []string) []testConn {
	if len(matches) == 0 {
		return nil
	}
	addrs := map[netip.Addr]string{}
	for _, p := range trust.HomeRanges {
		addrs[p.Masked().Addr().Next()] = ""
	}
	for _, a := range []netip.Addr{trust.ServersRange.Masked().Addr().Next(), netip.MustParseAddr("203.0.113.10"),
		netip.MustParseAddr("2001:db8::10")} {
		addrs[a] = ""
	}
	users := map[string]string{sshdNoUser: ""}
	locals := map[netip.Addr]string{netip.IPv4Unspecified(): "", netip.IPv6Unspecified(): ""}
	ports := map[int]string{22: ""}
	for _, m := range matches {
		f := strings.Fields(strings.ReplaceAll(m, "=", " "))
		for i := 1; i+1 < len(f); i += 2 {
			for _, pattern := range strings.Split(f[i+1], ",") {
				if strings.HasPrefix(pattern, "!") {
					continue
				}
				var named []string
				switch strings.ToLower(f[i]) {
				case "address":
					if a, ok := patternAddr(pattern); ok {
						if _, seen := addrs[a]; !seen || addrs[a] == "" {
							addrs[a] = m
						}
					}
				case "user":
					if !strings.ContainsAny(pattern, "*?") {
						named = []string{pattern}
					}
				case "group":
					named = groupMembers(pattern)
				case "localaddress":
					if a, ok := patternAddr(pattern); ok && locals[a] == "" {
						locals[a] = m
					}
				case "localport":
					if p, err := strconv.Atoi(pattern); err == nil && ports[p] == "" {
						ports[p] = m
					}
				}
				for _, u := range named {
					if users[u] == "" {
						users[u] = m
					}
				}
			}
		}
	}
	const most = 256
	byAddr := func(a, b netip.Addr) int { return a.Compare(b) }
	var out []testConn
	for _, u := range slices.Sorted(maps.Keys(users)) {
		for _, a := range slices.SortedFunc(maps.Keys(addrs), byAddr) {
			for _, l := range slices.SortedFunc(maps.Keys(locals), byAddr) {
				for _, p := range slices.Sorted(maps.Keys(ports)) {
					// The local address is the same family as the client's.
					if l.Is4() == a.Is4() && len(out) < most {
						out = append(out, testConn{user: u, addr: a, laddr: l, lport: p,
							match: cmp.Or(users[u], addrs[a], locals[l], ports[p])})
					}
				}
			}
		}
	}
	return out
}

// patternAddr is an address a Match Address pattern matches: the address,
// a range's first address after its network's, or the pattern with each
// wildcard as 1 (192.168.1.* → 192.168.1.1).
func patternAddr(pattern string) (netip.Addr, bool) {
	if p, err := netip.ParsePrefix(pattern); err == nil {
		if p.IsSingleIP() {
			return p.Addr(), true
		}
		return p.Masked().Addr().Next(), true
	}
	a, err := netip.ParseAddr(strings.NewReplacer("*", "1", "?", "1").Replace(pattern))
	return a, err == nil
}

// groupMembers lists a group's members (/etc/group), for Match Group.
func groupMembers(name string) []string {
	g, err := user.LookupGroup(name)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile("/etc/group")
	if err != nil {
		return nil
	}
	for line := range strings.Lines(string(data)) {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) == 4 && f[0] == g.Name && f[3] != "" {
			return strings.Split(f[3], ",")
		}
	}
	return nil
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
		return fsutil.WriteFile(SSHDropIn, content, 0o644)
	}
	if err := put(on, []byte(sshDropIn)); err != nil {
		return err
	}
	// sshd -t also checks the host keys, but needs sshd's folder, gone
	// while a socket-activated sshd waits for a connection; sshd -G then
	// checks the files (docs/ASSUMPTIONS.md J4).
	check := "-t"
	if _, err := os.Stat(sshdRunDir); errors.Is(err, fs.ErrNotExist) {
		check = "-G"
	}
	if out, err := exec.Command(sshdBin, check).CombinedOutput(); err != nil {
		if rerr := put(had, old); rerr != nil {
			return fmt.Errorf("sshd %s refused the change (%s), and putting the old file back failed: %w", check,
				sshdOutput(out), rerr)
		}
		return fmt.Errorf("sshd %s refused the change, so SSH is keeping its old settings: %s", check, sshdOutput(out))
	}
	// Reloads a running sshd (its ExecReload runs sshd -t again). One
	// started by ssh.socket can't reload: on SIGHUP sshd starts itself
	// again and can't bind port 22, which systemd holds, so it's
	// restarted instead (ExecStartPre checks again; logged-in sessions
	// stay, KillMode=process). Not running, the next connection starts it
	// with the new files (docs/ASSUMPTIONS.md J4).
	reload := "try-reload-or-restart"
	if exec.Command("systemctl", "is-active", "--quiet", "ssh.socket").Run() == nil {
		reload = "try-restart"
	}
	if out, err := exec.Command("systemctl", reload, "ssh.service").CombinedOutput(); err != nil {
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
