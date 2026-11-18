package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}

// A request is refused, with a sentence for the page, unless it's one
// public key and an account name Debian takes; a new account mustn't
// exist and needs a password long enough, and an existing one must.
func TestSSHKeyRequestCheck(t *testing.T) {
	key := testPublicKey(t)
	for _, c := range []struct {
		r    SSHKeyRequest
		want string // in the error; "" for none
	}{
		{SSHKeyRequest{Key: key + " tom@laptop", User: "tom-2", New: true, Password: "a long password"}, ""},
		{SSHKeyRequest{Key: "  \n" + key + "\n", User: "tom", New: true, Password: "a long password"}, ""},
		{SSHKeyRequest{Key: "", User: "tom", New: true, Password: "a long password"}, "Paste your public key"},
		{SSHKeyRequest{Key: "-----BEGIN OPENSSH PRIVATE KEY-----\nb3Bl\n-----END OPENSSH PRIVATE KEY-----", User: "tom"}, "That's a private key"},
		{SSHKeyRequest{Key: "ssh-ed25519 notbase64", User: "tom"}, "That isn't an SSH public key"},
		{SSHKeyRequest{Key: key + "\n" + key, User: "tom"}, "one key at a time"},
		{SSHKeyRequest{Key: `from="10.0.0.1" ` + key, User: "tom"}, "without options"},
		{SSHKeyRequest{Key: key, User: "Tom", New: true, Password: "a long password"}, "must start with a small letter"},
		{SSHKeyRequest{Key: key, User: "tom;rm", New: true, Password: "a long password"}, "must start with a small letter"},
		{SSHKeyRequest{Key: key, User: "-tom", New: true, Password: "a long password"}, "must start with a small letter"},
		{SSHKeyRequest{Key: key, User: "tom", New: true, Password: "short"}, "Password must be at least"},
		{SSHKeyRequest{Key: key, User: "tom", New: true, Password: "a long\npassword"}, "line break"},
		{SSHKeyRequest{Key: key, User: "root", New: true, Password: "a long password"}, "already has an account called root"},
		{SSHKeyRequest{Key: key, User: "nosuchaccount-rs"}, "no account called nosuchaccount-rs"},
		{SSHKeyRequest{Key: key, User: "root"}, "root can't use sudo"},
	} {
		err := c.r.Check()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%+v: %v", c.r.User, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%q for %q: %v, want %q", c.r.Key, c.r.User, err, c.want)
		}
	}
}

// The key is added once, with its comment, after what's there; the folder
// and file are made private; a link in the file's place is refused.
func TestAddAuthorizedKey(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	u := &user.User{Uid: me.Uid, Gid: me.Gid, HomeDir: home}
	key := testPublicKey(t)
	line, err := authorizedKeyLine("  " + key + "   tom@laptop  home\n")
	if err != nil {
		t.Fatal(err)
	}
	if line != key+" tom@laptop home" {
		t.Errorf("line = %q", line)
	}
	if err := addAuthorizedKey(u, line); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, ".ssh", "authorized_keys")
	if fi, err := os.Stat(filepath.Dir(file)); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf(".ssh = %v %v", fi.Mode(), err)
	}
	// Already there, under another comment and with options: not again.
	other := testPublicKey(t)
	os.WriteFile(file, []byte(`no-pty `+key+" old\n"+other), 0o644)
	if err := addAuthorizedKey(u, line); err != nil {
		t.Fatal(err)
	}
	third := testPublicKey(t) + " work"
	if err := addAuthorizedKey(u, third); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(file)
	if want := "no-pty " + key + " old\n" + other + "\n" + third + "\n"; string(data) != want {
		t.Errorf("authorized_keys = %q, want %q", data, want)
	}
	if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o600 {
		t.Errorf("authorized_keys = %v", fi.Mode())
	}

	// A link in the file's place.
	linked := t.TempDir()
	os.Mkdir(filepath.Join(linked, ".ssh"), 0o700)
	target := filepath.Join(t.TempDir(), "elsewhere")
	os.Symlink(target, filepath.Join(linked, ".ssh", "authorized_keys"))
	if err := addAuthorizedKey(&user.User{Uid: me.Uid, Gid: me.Gid, HomeDir: linked}, line); err == nil {
		t.Error("followed a link")
	}
	if _, err := os.Stat(target); err == nil {
		t.Error("wrote where the link points")
	}
}
