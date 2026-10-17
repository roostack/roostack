package agent

import (
	"context"
	"os"
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
