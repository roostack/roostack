package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSSHAPI(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid())})
	ctx := context.Background()
	if err := c.SetSSHKeysOnly(ctx, true); err != nil || !host.keysOnly {
		t.Fatalf("keys only: %v, set=%v", err, host.keysOnly)
	}
	st, err := c.SSHStatus(ctx)
	if err != nil || !st.Installed || !st.KeysOnly || st.Passwords {
		t.Errorf("status = %+v, %v", st, err)
	}
	if err := c.SetSSHKeysOnly(ctx, false); err != nil || host.keysOnly {
		t.Errorf("off: %v, set=%v", err, host.keysOnly)
	}
}

func TestSSHDSettings(t *testing.T) {
	out := []byte("port 22\npasswordauthentication no\nkbdinteractiveauthentication yes\npermitrootlogin without-password\n")
	if pw, root := sshdSettings(out); !pw || root != "prohibit-password" {
		t.Errorf("got passwords=%v root=%q", pw, root)
	}
	out = []byte("passwordauthentication no\nkbdinteractiveauthentication no\npermitrootlogin no\n")
	if pw, root := sshdSettings(out); pw || root != "no" {
		t.Errorf("hardened: passwords=%v root=%q", pw, root)
	}
}

// Each account's latest key login is kept (journalctl --reverse lists the
// newest first); lines that aren't a key login, or name an account sshd
// couldn't have, are skipped.
func TestParseKeyLogins(t *testing.T) {
	out := []byte(`{"MESSAGE":"Accepted publickey for tom from 192.168.1.20 port 51234 ssh2: ED25519 SHA256:abc","__REALTIME_TIMESTAMP":"1792207593387818"}
{"MESSAGE":"Accepted publickey for root from 203.0.113.9 port 40000 ssh2: ED25519 SHA256:def","__REALTIME_TIMESTAMP":"1792207000000000"}
{"MESSAGE":"Accepted publickey for tom from 10.0.0.2 port 50000 ssh2: RSA SHA256:old","__REALTIME_TIMESTAMP":"1792200000000000"}
{"MESSAGE":"Accepted publickey for Bad\nName from 1.2.3.4 port 1 ssh2: x","__REALTIME_TIMESTAMP":"1"}
{"MESSAGE":"Accepted password for alice from 1.2.3.4 port 1 ssh2","__REALTIME_TIMESTAMP":"1"}
not json
`)
	got := parseKeyLogins(out, func(name string) bool { return name == "tom" })
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
	want := KeyLogin{User: "tom", From: "192.168.1.20", At: time.UnixMicro(1792207593387818).UTC(), Sudo: true}
	if got[0] != want {
		t.Errorf("first = %+v, want %+v", got[0], want)
	}
	if got[1].User != "root" || got[1].Sudo {
		t.Errorf("second = %+v", got[1])
	}
	st := SSHStatus{KeyLogins: got}
	if l, ok := st.SudoKeyLogin(); !ok || l.User != "tom" {
		t.Errorf("sudo key login = %+v, %v", l, ok)
	}
	if _, ok := (SSHStatus{KeyLogins: got[1:]}).SudoKeyLogin(); ok {
		t.Error("root's key login counted")
	}
}

func TestSSHDOutput(t *testing.T) {
	out := []byte("/etc/ssh/sshd_config.d/30-x.conf: line 1: Bad configuration option: X\r\n/etc/ssh/sshd_config.d/30-x.conf: terminating\r\n")
	if got, want := sshdOutput(out), "/etc/ssh/sshd_config.d/30-x.conf: line 1: Bad configuration option: X; /etc/ssh/sshd_config.d/30-x.conf: terminating"; got != want {
		t.Errorf("got %q", got)
	}
}

// Match blocks are read from the main file and every file it includes,
// in order, whatever the keyword's case.
func TestMatchBlocks(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "sshd_config")
	os.MkdirAll(filepath.Join(dir, "d"), 0o755)
	os.WriteFile(main, []byte("Include "+filepath.Join(dir, "d", "*.conf")+"\nPort 22\nMatch User backup\n  PasswordAuthentication yes\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "d", "20-a.conf"), []byte("  match Address 192.168.7.0/24,!192.168.7.9\n\tPasswordAuthentication yes\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "d", "10-b.conf"), []byte("PasswordAuthentication no\n"), 0o644)
	got, err := matchBlocks(main)
	want := []string{"match Address 192.168.7.0/24,!192.168.7.9", "Match User backup"}
	if err != nil || !slices.Equal(got, want) {
		t.Errorf("matchBlocks = %q, %v; want %q", got, err, want)
	}
}

// The connections asked about cover every home range, Roostack's network,
// the internet, and what each Match block names.
func TestTestConnections(t *testing.T) {
	if got := testConnections(nil); got != nil {
		t.Errorf("no Match blocks: %v", got)
	}
	m := "Match Address 192.168.7.0/24,!192.168.7.9,10.1.*.*"
	conns := testConnections([]string{m})
	find := func(user, addr string) (testConn, bool) {
		for _, c := range conns {
			if c.user == user && c.addr.String() == addr {
				return c, true
			}
		}
		return testConn{}, false
	}
	for _, a := range []string{"192.168.0.1", "100.64.0.1", "100.96.0.1", "203.0.113.10", "2001:db8::10", "fc00::1"} {
		if c, ok := find(sshdNoUser, a); !ok || c.match != "" {
			t.Errorf("from %s: %+v, %v", a, c, ok)
		}
	}
	for _, a := range []string{"192.168.7.1", "10.1.1.1"} {
		if c, ok := find(sshdNoUser, a); !ok || c.match != m {
			t.Errorf("from %s (named): %+v, %v", a, c, ok)
		}
	}
	if _, ok := find(sshdNoUser, "192.168.7.9"); ok {
		t.Error("a negated address was asked about")
	}
	if _, ok := find("root", "192.168.0.1"); ok {
		t.Error("no block names a user, so one user is enough")
	}
	if c := conns[0]; c.spec() != "user=roostack-check,host="+c.addr.String()+",addr="+c.addr.String()+",laddr="+c.laddr.String()+",lport=22" {
		t.Errorf("spec = %q", c.spec())
	}
	// A local port: every connection is also made to it.
	lp := "Match LocalPort 2222"
	conns = testConnections([]string{lp})
	n := 0
	for _, c := range conns {
		if c.lport == 2222 && c.match == lp {
			n++
		}
	}
	if n == 0 || n*2 != len(conns) {
		t.Errorf("%d of %d connections to port 2222", n, len(conns))
	}
	// A block naming a user: that user from every address.
	u := "Match User backup"
	conns = testConnections([]string{m, u})
	if c, ok := find("backup", "203.0.113.10"); !ok || c.match != u {
		t.Errorf("a named user: %+v, %v", c, ok)
	}
}

func TestPatternAddr(t *testing.T) {
	for pattern, want := range map[string]string{"192.168.7.0/24": "192.168.7.1", "10.0.0.5": "10.0.0.5",
		"10.0.0.5/32": "10.0.0.5", "192.168.*.*": "192.168.1.1", "fd00::/8": "fd00::1", "*.example.com": ""} {
		a, ok := patternAddr(pattern)
		if (want == "") == ok || (ok && a.String() != want) {
			t.Errorf("patternAddr(%q) = %v, %v; want %q", pattern, a, ok, want)
		}
	}
}
