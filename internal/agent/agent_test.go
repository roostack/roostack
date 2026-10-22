package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
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
	useErr    error
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
	if f.useErr != nil {
		return UsedDrive{}, f.useErr
	}
	return UsedDrive{Dir: "/mnt/roostack-x", UUID: "u"}, nil
}
func (f *fakeHost) EraseDrive(disk, label string) error { f.erased = disk; return nil }
func (f *fakeHost) LibraryFolders(l Library) (FolderList, error) {
	return FolderList{Path: l.Sub, Folders: []FolderEntry{{Name: "Films", Folders: 2}}}, nil
}
func (f *fakeHost) MountLibrary(l Library) error     { f.library = &l; return nil }
func (f *fakeHost) UnmountLibrary(name string) error { f.unmounted = name; return nil }
func (f *fakeHost) SetLibraryViews(service string, libs []string, at map[string]string) error {
	f.views = append([]string{service}, libs...)
	for _, name := range slices.Sorted(maps.Keys(at)) {
		f.views = append(f.views, name+"="+at[name])
	}
	return nil
}
func (f *fakeHost) MountImport(service string, l Library) (string, error) {
	f.library = &l
	return "/mnt/roostack-import/" + service, nil
}
func (f *fakeHost) UnmountImport(service string) error { f.unmounted = "import " + service; return nil }
func (f *fakeHost) MountPhotos(service, who string, l Library) (string, error) {
	f.library = &l
	return ImportRoot + "/" + PhotoSpotName(service, who), nil
}
func (f *fakeHost) UnmountPhotos(service, who string) error {
	f.unmounted = "photos " + service + " " + who
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
// Peer credentials are Linux's: elsewhere the server runs on a loopback
// port, and every connection is this process's user, as the kernel would
// say.
func start(t *testing.T, allowed []uint32) (*Client, *fakeHost) {
	c, host, _ := startLogged(t, allowed)
	return c, host
}

// startLogged is start, with what the agent logs.
func startLogged(t *testing.T, allowed []uint32) (*Client, *fakeHost, *strings.Builder) {
	t.Helper()
	host := &fakeHost{}
	var log strings.Builder
	s := &Server{Host: host, AllowedUIDs: allowed, Version: "test",
		Log: slog.New(slog.NewTextHandler(&syncWriter{w: &log}, nil))}
	if runtime.GOOS != "linux" {
		srv := httptest.NewUnstartedServer(s.Handler())
		srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, ctxKey{}, uint32(os.Getuid()))
		}
		srv.Start()
		t.Cleanup(srv.Close)
		c := NewClient("")
		c.http.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
		}}
		return c, host, &log
	}
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
	return NewClient(socket), host, &log
}

// syncWriter lets the server's handlers log at once.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
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

// The names that make paths are checked at the boundary: a bad one is
// refused with a 400 before the host is asked anything (the quality
// review of 2026-10-21, S13).
func TestBadNamesRefused(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid())})
	ctx := t.Context()
	l := Library{Drive: "1111-2222"}
	for what, call := range map[string]func() error{
		"a library's name":   func() error { return c.MountLibrary(ctx, Library{Name: "Films", Drive: "1111-2222"}) },
		"removing a library": func() error { return c.UnmountLibrary(ctx, "a.b") },
		"a service's views":  func() error { return c.SetLibraryViews(ctx, "Jellyfin", nil, nil) },
		"a library in views": func() error { return c.SetLibraryViews(ctx, "jellyfin", []string{"a b"}, nil) },
		"a folder of libraries": func() error {
			return c.SetLibraryViews(ctx, "jellyfin", []string{"films"}, map[string]string{".x": "films"})
		},
		"a library it doesn't read": func() error {
			return c.SetLibraryViews(ctx, "jellyfin", []string{"films"}, map[string]string{"Films": "music"})
		},
		"removing a service's views": func() error { return c.RemoveLibraryViews(ctx, "a.b") },
		"a share's library":          func() error { return c.SaveShareLogin(ctx, "a b", Share{Type: "smb", Server: "nas", Path: "m"}) },
		"an import's service": func() error {
			_, err := c.MountImport(ctx, "a.b", l)
			return err
		},
		"removing an import": func() error { return c.UnmountImport(ctx, "a/b") },
		"a migration's service": func() error {
			_, err := c.MountPhotos(ctx, "Immich", "anna", l)
			return err
		},
		"a migration's person": func() error {
			_, err := c.MountPhotos(ctx, "immich", ".anna", l)
			return err
		},
		"removing a migration": func() error { return c.UnmountPhotos(ctx, "immich", "a/b") },
		"an interface": func() error {
			_, err := c.WireGuardStatus(ctx, "eth0")
			return err
		},
		"removing an interface": func() error { return c.RemoveWireGuard(ctx, "eth0") },
		"a time zone":           func() error { return c.SetTimezone(ctx, "../etc/passwd") },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "400 Bad Request") {
			t.Errorf("%s: %v", what, err)
		}
		if !reflect.DeepEqual(*host, fakeHost{}) {
			t.Errorf("%s reached the host: %+v", what, *host)
			*host = fakeHost{}
		}
	}
}

// A host's refusal is the caller's to fix (400); any other error is the
// agent's own failure (500).
func TestHostErrorStatus(t *testing.T) {
	c, host := start(t, []uint32{uint32(os.Getuid())})
	for err, want := range map[error]string{
		refuse("that drive isn't connected any more"):                         "400 Bad Request: that drive isn't connected any more",
		fmt.Errorf("mounting it: %w", refuse("that drive has no filesystem")): "400 Bad Request: mounting it: that drive has no filesystem",
		errors.New("lsblk: exit status 1"):                                    "500 Internal Server Error: lsblk: exit status 1",
	} {
		host.useErr = err
		if _, got := c.UseDrive(t.Context(), "uuid:x"); got == nil || !strings.HasSuffix(got.Error(), want) {
			t.Errorf("%v: %v, want %q", err, got, want)
		}
	}
}

// Every route that changes something logs what it did, and who asked.
func TestChangesAreLogged(t *testing.T) {
	c, _, log := startLogged(t, []uint32{uint32(os.Getuid())})
	ctx := t.Context()
	l := Library{Drive: "1111-2222", Share: &Share{Type: "smb", Server: "nas", Path: "m", Password: "hunter2"}}
	for msg, call := range map[string]func() error{
		"set unprivileged port start": func() error { return c.SetUnprivilegedPortStart(ctx) },
		"apply firewall":              func() error { return c.ApplyFirewall(ctx, DefaultFirewall()) },
		"open wireguard for joining":  func() error { return c.AllowWireGuardJoin(ctx) },
		"set time zone":               func() error { return c.SetTimezone(ctx, "Europe/Berlin") },
		"OS updates":                  func() error { return c.RunUpdates(ctx, "", io.Discard) },
		"reboot":                      func() error { return c.Reboot(ctx) },
		"apply wireguard": func() error {
			_, err := c.ApplyWireGuard(ctx, WireGuard{Address: "100.96.0.2/24"})
			return err
		},
		"remove wireguard":   func() error { return c.RemoveWireGuard(ctx, WireGuardInterface) },
		"apply direct links": func() error { return c.ApplyLinks(ctx, Links{}) },
		"use backup drive": func() error {
			_, err := c.UseDrive(ctx, "system")
			return err
		},
		"erase disk for backups": func() error { return c.EraseDrive(ctx, "/dev/sdb", "BACKUPS") },
		"prepare backup folder": func() error {
			_, err := c.PrepareBackupFolder(ctx, "/mnt/backups")
			return err
		},
		"mount media library":  func() error { return c.MountLibrary(ctx, Library{Name: "films", Drive: "1111-2222"}) },
		"remove media library": func() error { return c.UnmountLibrary(ctx, "films") },
		"set library views":    func() error { return c.SetLibraryViews(ctx, "jellyfin", []string{"films"}, nil) },
		"remove library views": func() error { return c.RemoveLibraryViews(ctx, "jellyfin") },
		"save share login":     func() error { return c.SaveShareLogin(ctx, "media", *l.Share) },
		"mount place to bring data in from": func() error {
			_, err := c.MountImport(ctx, "immich", l)
			return err
		},
		"unmount place to bring data in from": func() error { return c.UnmountImport(ctx, "immich") },
		"mount export to migrate photos from": func() error {
			_, err := c.MountPhotos(ctx, "immich", "anna", l)
			return err
		},
		"unmount export to migrate photos from": func() error { return c.UnmountPhotos(ctx, "immich", "anna") },
		"set ssh keys only":                     func() error { return c.SetSSHKeysOnly(ctx, true) },
	} {
		before := log.Len()
		if err := call(); err != nil {
			t.Errorf("%s: %v", msg, err)
			continue
		}
		got := log.String()[before:]
		if !strings.Contains(got, fmt.Sprintf("msg=%q", msg)) && !strings.Contains(got, "msg="+msg+" ") ||
			!strings.Contains(got, fmt.Sprintf("uid=%d", os.Getuid())) {
			t.Errorf("%s logged %q", msg, got)
		}
	}
	if strings.Contains(log.String(), "hunter2") {
		t.Errorf("a password reached the log:\n%s", log)
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
