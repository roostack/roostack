package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeHost struct {
	joinOpen  bool
	portsSet  bool
	ruleset   string
	tz        string
	updated   bool
	rebooted  bool
	updateErr error
	wg        *WireGuard
	links     *Links
	erased    string
}

func (f *fakeHost) ApplyLinks(l Links) error { f.links = &l; return nil }

func (f *fakeHost) SetUnprivilegedPortStart() error { f.portsSet = true; return nil }
func (f *fakeHost) ApplyFirewall(r string) error    { f.ruleset = r; return nil }
func (f *fakeHost) AllowWireGuardJoin() error       { f.joinOpen = true; return nil }
func (f *fakeHost) SetTimezone(name string) error   { f.tz = name; return nil }
func (f *fakeHost) RunUpdates(_ context.Context, w io.Writer) error {
	f.updated = true
	fmt.Fprint(w, "==> Installing updates\nNo packages found that can be upgraded unattended")
	if f.updateErr != nil {
		return f.updateErr
	}
	return nil
}
func (f *fakeHost) Reboot() error { f.rebooted = true; return nil }
func (f *fakeHost) ApplyWireGuard(wg WireGuard) (string, error) {
	f.wg = &wg
	return "cHVibGlj", nil
}
func (f *fakeHost) WireGuardStatus(string) (WireGuardStatus, error) { return WireGuardStatus{}, nil }
func (f *fakeHost) PrepareBackupFolder(dir string) (string, error)  { return dir + "/roostack", nil }
func (f *fakeHost) Drives() ([]Drive, error) {
	return []Drive{{ID: "system", Kind: "system", Usable: true}, {ID: "disk:/dev/sdb", Kind: "usb", Disk: "/dev/sdb", Erasable: true}}, nil
}
func (f *fakeHost) UseDrive(id string) (UsedDrive, error) {
	return UsedDrive{Dir: "/mnt/roostack-x", UUID: "u"}, nil
}
func (f *fakeHost) EraseDrive(disk, label string) error { f.erased = disk; return nil }
func (f *fakeHost) HostChanges() (HostChanges, error) {
	return HostChanges{Held: []string{"linux-image-amd64"}}, nil
}
func (f *fakeHost) WireGuardKeys() (map[string]string, error) {
	return map[string]string{"roostack": "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw="}, nil
}
func (f *fakeHost) RemoveWireGuard(string) error { f.wg = nil; return nil }

// start runs a real server on a Unix socket, so SO_PEERCRED is exercised.
func start(t *testing.T, allowed []uint32) (*Client, *fakeHost) {
	t.Helper()
	host := &fakeHost{}
	s := &Server{Host: host, AllowedUIDs: allowed, Version: "test",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	socket := filepath.Join(t.TempDir(), "agent.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Serve(ctx, socket, os.Getgid()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-errc; err != nil {
			t.Error(err)
		}
	})
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return NewClient(socket), host
}

func TestAPI(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid())})
	ctx := context.Background()

	if v, err := c.Health(ctx); err != nil || v != "test" {
		t.Fatalf("health = %q, %v", v, err)
	}
	if err := c.SetUnprivilegedPortStart(ctx); err != nil || !host.portsSet {
		t.Errorf("ports: %v, set=%v", err, host.portsSet)
	}
	if err := c.ApplyFirewall(ctx, DefaultFirewall()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(host.ruleset, "tcp dport 443 accept") {
		t.Errorf("ruleset not applied:\n%s", host.ruleset)
	}

	if err := c.SetTimezone(ctx, "Australia/Melbourne"); err != nil || host.tz != "Australia/Melbourne" {
		t.Errorf("timezone: %v, set=%q", err, host.tz)
	}
	for _, tz := range []string{"", "../etc/passwd", "-h", "Europe/../../x", "a/b/c/d", "Europe/Berlin;id"} {
		if err := c.SetTimezone(ctx, tz); err == nil {
			t.Errorf("timezone %q accepted", tz)
		}
	}

	var out strings.Builder
	if err := c.RunUpdates(ctx, &out); err != nil || !host.updated ||
		out.String() != "==> Installing updates\nNo packages found that can be upgraded unattended\n" {
		t.Errorf("updates: %q %v", out.String(), err)
	}
	host.updateErr = errors.New("exit status 100")
	if err := c.RunUpdates(ctx, io.Discard); err == nil || err.Error() != "exit status 100" {
		t.Errorf("failed updates: %v", err)
	}
	if err := c.Reboot(ctx); err != nil || !host.rebooted {
		t.Errorf("reboot: %v", err)
	}

	bad := DefaultFirewall()
	bad.Allow = append(bad.Allow, Rule{Port: 0, Proto: "tcp", From: FromAny})
	if err := c.ApplyFirewall(ctx, bad); err == nil || !strings.Contains(err.Error(), "invalid port") {
		t.Errorf("want invalid port error, got %v", err)
	}
}

// Every endpoint refuses fields it doesn't know, rather than ignoring them.
func TestUnknownFieldsRefused(t *testing.T) {
	c, _ := start(t, []uint32{uint32(os.Getuid())})
	for _, path := range []string{"/v1/backup-folder", "/v1/drives/use", "/v1/drives/erase"} {
		err := c.call(context.Background(), http.MethodPost, path, map[string]string{"surprise": "x"}, nil)
		if err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestRejectsOtherUsers(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid()) + 12345})
	if err := c.SetUnprivilegedPortStart(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("want 403, got %v", err)
	}
	if host.portsSet {
		t.Error("action must not run for a rejected caller")
	}
}

func TestRuleset(t *testing.T) {
	fw := DefaultFirewall()
	fw.Allow = append(fw.Allow, Rule{Port: 53, Proto: "udp", From: FromLAN, Reason: "pihole dns"})
	got := fw.Ruleset()
	for _, want := range []string{
		"policy drop;",
		"ip saddr @lan4 tcp dport 22 accept", // SSH is LAN-only by default
		"ip6 saddr @lan6 udp dport 53 accept",
		"tcp dport 80 accept",
		"udp dport 443 accept",
		"delete table inet roostack",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ruleset missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "dport 22") > strings.Index(got, "dport 53") {
		t.Error("rules should be sorted by port")
	}
}

// New WireGuard handshakes are limited per source address, before the
// port's accept; other ports aren't.
func TestRulesetWireGuardLimit(t *testing.T) {
	fw := DefaultFirewall()
	fw.Allow = append(fw.Allow, Rule{Port: WireGuardPort, Proto: "udp", From: FromAny, Reason: "lab network"})
	got := fw.Ruleset()
	limit := "udp dport 51820 @th,64,8 1 add @wg_handshakes4 { ip saddr limit rate over 5/second burst 20 packets } drop"
	if !strings.Contains(got, limit) || !strings.Contains(got, "@wg_handshakes6 { ip6 saddr") ||
		strings.Index(got, limit) > strings.Index(got, "udp dport 51820 accept") {
		t.Errorf("handshake limit missing or after the accept:\n%s", got)
	}
	if strings.Contains(got, "udp dport 443 @th") {
		t.Error("only WireGuard's ports are limited")
	}
}

func TestRulesetSSHOff(t *testing.T) {
	fw := DefaultFirewall()
	fw.SSHFrom = FromOff
	got := fw.Ruleset()
	if strings.Contains(got, "dport 22") || !strings.Contains(got, "udp dport 546 accept") {
		t.Errorf("ssh off: want no port 22 rule and DHCPv6 allowed:\n%s", got)
	}
	if !strings.Contains(got, "elements = { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10 }") ||
		!strings.Contains(got, "elements = { fc00::/7, fe80::/10 }") {
		t.Errorf("LAN sets wrong:\n%s", got)
	}
}

func TestIsLAN(t *testing.T) {
	for addr, want := range map[string]bool{"192.168.86.20": true, "10.0.2.2": true, "100.101.1.1": true,
		"::ffff:192.168.1.5": true, "fe80::1": true, "8.8.8.8": false, "2001:db8::1": false} {
		if got := IsLAN(netip.MustParseAddr(addr)); got != want {
			t.Errorf("IsLAN(%s) = %v", addr, got)
		}
	}
}

func TestFirewallValidate(t *testing.T) {
	for _, fw := range []Firewall{
		{SSHFrom: "everyone"},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "sctp", From: FromAny}}},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "tcp", From: "internet"}}},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "tcp", From: FromAny, Reason: "x\"\n}"}}},
	} {
		if err := fw.Validate(); err == nil {
			t.Errorf("expected an error for %+v", fw)
		}
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestLineStreamOutlivesCaller(t *testing.T) {
	st := &lineStream{enc: json.NewEncoder(failingWriter{}), flush: func() {}}
	for range 3 {
		if n, err := st.Write([]byte("Unpacking curl\n")); err != nil || n != 15 {
			t.Fatalf("write after the caller went away: %d %v", n, err)
		}
	}
}

func TestWireGuardKeysForBackups(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid())})
	c.KeysFile = filepath.Join(t.TempDir(), "wireguard-keys.json")
	ctx := context.Background()
	wg := WireGuard{Address: "100.96.0.2/24"}
	// Nothing saved yet: no key offered.
	if _, err := c.ApplyWireGuard(ctx, wg); err != nil || host.wg.SavedKey != "" {
		t.Fatalf("apply: %v %+v", err, host.wg)
	}
	if err := c.SaveWireGuardKeys(ctx); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(c.KeysFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("keys file: %v %v", err, fi)
	}
	// A saved key is offered for its interface (used only if it has none).
	if _, err := c.ApplyWireGuard(ctx, wg); err != nil || host.wg.SavedKey != "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw=" {
		t.Errorf("the saved key should be offered: %v %+v", err, host.wg)
	}
	guests := WireGuard{Interface: GuestsInterface, Address: "100.100.0.1/24"}
	if _, err := c.ApplyWireGuard(ctx, guests); err != nil || host.wg.SavedKey != "" {
		t.Errorf("only the interface's own key: %v %+v", err, host.wg)
	}
	wg.NewKey = true
	if _, err := c.ApplyWireGuard(ctx, wg); err != nil || host.wg.SavedKey != "" {
		t.Errorf("a new key replaces it: %v %+v", err, host.wg)
	}
}
