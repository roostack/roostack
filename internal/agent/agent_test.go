package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/trust"
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
	keysOnly  bool
	series    string
	library   *Library
	unmounted string
	views     []string
	share     string
}

func (f *fakeHost) ApplyLinks(l Links) error { f.links = &l; return nil }

func (f *fakeHost) SetUnprivilegedPortStart() error { f.portsSet = true; return nil }
func (f *fakeHost) ApplyFirewall(r string) error    { f.ruleset = r; return nil }
func (f *fakeHost) AllowWireGuardJoin() error       { f.joinOpen = true; return nil }
func (f *fakeHost) SetTimezone(name string) error   { f.tz = name; return nil }
func (f *fakeHost) RunUpdates(_ context.Context, series string, w io.Writer) error {
	f.updated = true
	f.series = series
	fmt.Fprint(w, "==> Installing updates\nNo packages found that can be upgraded unattended")
	if f.updateErr != nil {
		return f.updateErr
	}
	return nil
}
func (f *fakeHost) UpdatesStatus() (UpdatesRun, error) {
	return UpdatesRun{Ended: time.Unix(1760000000, 0), Status: 100, Output: "==> Installing updates\n"}, nil
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
func (f *fakeHost) LibraryFolders(l Library) (FolderList, error) {
	return FolderList{Path: l.Sub, Folders: []FolderEntry{{Name: "Films", Folders: 2}}}, nil
}
func (f *fakeHost) MountLibrary(l Library) error     { f.library = &l; return nil }
func (f *fakeHost) UnmountLibrary(name string) error { f.unmounted = name; return nil }
func (f *fakeHost) SetLibraryViews(service string, libs []string) error {
	f.views = append([]string{service}, libs...)
	return nil
}
func (f *fakeHost) CheckShare(s Share) (FolderList, error) {
	return FolderList{Folders: []FolderEntry{{Name: s.Path}}}, nil
}
func (f *fakeHost) SaveShareLogin(name string, s Share) error {
	f.share = name + " " + s.Username
	return nil
}
func (f *fakeHost) RemoveLibraryViews(service string) error {
	f.views = []string{"removed", service}
	return nil
}
func (f *fakeHost) Libraries() ([]LibraryStatus, error) {
	return []LibraryStatus{{Name: "films", Mounted: true, Connected: true}}, nil
}
func (f *fakeHost) HostChanges() (HostChanges, error) {
	return HostChanges{Held: []string{"linux-image-amd64"}}, nil
}
func (f *fakeHost) WireGuardKeys() (map[string]string, error) {
	return map[string]string{"roostack": "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykw="}, nil
}
func (f *fakeHost) RemoveWireGuard(string) error { f.wg = nil; return nil }
func (f *fakeHost) SSHStatus() (SSHStatus, error) {
	return SSHStatus{Installed: true, KeysOnly: f.keysOnly, Passwords: !f.keysOnly}, nil
}
func (f *fakeHost) SetSSHKeysOnly(on bool) error { f.keysOnly = on; return nil }

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
	if err := c.RunUpdates(ctx, "", &out); err != nil || !host.updated || host.series != "" ||
		out.String() != "==> Installing updates\nNo packages found that can be upgraded unattended\n" {
		t.Errorf("updates: %q %v", out.String(), err)
	}
	// Moving to a new release series (SPEC.md §7): only a series, never
	// anything that could reach the script as more than that.
	if err := c.RunUpdates(ctx, "0.5", io.Discard); err != nil || host.series != "0.5" {
		t.Errorf("updates with a series: %v, series %q", err, host.series)
	}
	for _, bad := range []string{"0", "0.5.1", "0.5;id", "0.5\nx", "../0.5", " 0.5", "a.b"} {
		host.series = "unchanged"
		if err := c.RunUpdates(ctx, bad, io.Discard); err == nil || host.series != "unchanged" {
			t.Errorf("series %q: %v, ran with %q", bad, err, host.series)
		}
	}
	host.updateErr = errors.New("exit status 100")
	if err := c.RunUpdates(ctx, "", io.Discard); err == nil || err.Error() != "exit status 100" {
		t.Errorf("failed updates: %v", err)
	}
	if run, err := c.UpdatesStatus(ctx); err != nil || run.Running || run.Status != 100 || !run.Ended.Equal(time.Unix(1760000000, 0)) ||
		run.Output != "==> Installing updates\n" {
		t.Errorf("last updates: %+v %v", run, err)
	}
	if err := c.Reboot(ctx); err != nil || !host.rebooted {
		t.Errorf("reboot: %v", err)
	}

	bad := DefaultFirewall()
	bad.Allow = append(bad.Allow, Rule{Port: 0, Proto: "tcp", For: trust.Anyone})
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
	fw.Allow = append(fw.Allow, Rule{Port: 53, Proto: "udp", For: trust.Home, Reason: "pihole dns"})
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
	fw.Allow = append(fw.Allow, Rule{Port: WireGuardPort, Proto: "udp", For: trust.Anyone, Reason: "lab network"})
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
	if !strings.Contains(got, "elements = { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/12, 100.80.0.0/16, 100.82.0.0/15, "+
		"100.84.0.0/14, 100.88.0.0/13, 100.96.128.0/17, 100.97.0.0/16, 100.98.0.0/15, 100.104.0.0/13, 100.112.0.0/12 }") ||
		!strings.Contains(got, "elements = { fc00::/7, fe80::/10 }") {
		t.Errorf("LAN sets wrong:\n%s", got)
	}
}

func TestFirewallValidate(t *testing.T) {
	for _, fw := range []Firewall{
		{SSHFrom: "everyone"},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "sctp", For: trust.Anyone}}},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "tcp", For: "internet"}}},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "tcp", For: trust.Relay}}},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "tcp"}}},
		{SSHFrom: FromLAN, Allow: []Rule{{Port: 80, Proto: "tcp", For: trust.Anyone, Reason: "x\"\n}"}}},
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

// Only the host's user namespace maps every UID to itself; a rootless
// container's maps its root to the roostack user (docs/ASSUMPTIONS.md S6).
func TestHostUIDMap(t *testing.T) {
	for m, want := range map[string]bool{
		"         0          0 4294967295\n": true,
		"0 999 1\n1 200000 65536\n":          false,
		"         0        999          1\n": false,
		"0 0 4294967295\n1 1 1\n":            false,
		"":                                   false,
	} {
		if hostUIDMap(m) != want {
			t.Errorf("%q: want %v", m, want)
		}
	}
	info := "pos:\t0\nflags:\t02000002\nmnt_id:\t15\nino:\t1057\nPid:\t4242\nNSpid:\t4242\n"
	if got := fdinfoPid(info); got != 4242 {
		t.Errorf("pid = %d", got)
	}
	if got := fdinfoPid("Pid:\t-1\n"); got != -1 {
		t.Errorf("exited: %d", got)
	}
}
