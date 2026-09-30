// Package agent is roostack-agent: the small root helper that performs a fixed
// set of privileged operations for the unprivileged roostack service
// (SPEC.md §6.1).
//
// It serves HTTP+JSON on a Unix socket. Callers are identified by the
// kernel (SO_PEERCRED), not by anything they send, and only root and the
// roostack user are accepted. Every operation validates its input and takes no
// free-form commands or paths.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Default locations.
const (
	DefaultSocket       = "/run/roostack-agent.sock"
	DefaultFirewallFile = "/etc/roostack/firewall.nft"
	sysctlFile          = "/etc/sysctl.d/60-roostack.conf"
	unprivPortProc      = "/proc/sys/net/ipv4/ip_unprivileged_port_start"
)

// Host performs the privileged actions. Tests replace it.
type Host interface {
	// SetUnprivilegedPortStart lets rootless containers bind ports >= 80
	// (docs/ASSUMPTIONS.md C3), now and after reboot.
	SetUnprivilegedPortStart() error
	// ApplyFirewall checks and loads an nftables ruleset, and saves it so it
	// is re-applied when the agent starts.
	ApplyFirewall(ruleset string) error
	// SetTimezone sets the host's time zone (an IANA name, already checked
	// with ValidTimezone).
	SetTimezone(name string) error
	// RunUpdates refreshes the package lists and installs available
	// updates with unattended-upgrades, writing its output to w as it runs.
	RunUpdates(ctx context.Context, w io.Writer) error
	// Reboot restarts the host shortly after returning.
	Reboot() error
	// ApplyWireGuard configures one of Roostack's WireGuard interfaces
	// (already validated) and brings it up, returning its public key.
	ApplyWireGuard(wg WireGuard) (publicKey string, err error)
	// WireGuardStatus reads an interface's configuration.
	WireGuardStatus(iface string) (WireGuardStatus, error)
	// WireGuardKeys reads Roostack's interfaces' private keys.
	WireGuardKeys() (map[string]string, error)
	// HostChanges checks for changes Roostack didn't make (SPEC.md §3.2).
	HostChanges() (HostChanges, error)
	// PrepareBackupFolder readies a backup folder (folder.go).
	PrepareBackupFolder(dir string) (string, error)
	// Drives lists the places backups can go; UseDrive readies one;
	// EraseDrive erases a disk and prepares it (drives.go).
	Drives() ([]Drive, error)
	UseDrive(id string) (UsedDrive, error)
	EraseDrive(disk, label string) error
	// RemoveWireGuard takes an interface down and deletes its
	// configuration (e.g. the server left its lab).
	RemoveWireGuard(iface string) error
}

// Server is the agent's HTTP API.
type Server struct {
	Host Host
	// AllowedUIDs may call the API (root and the roostack user).
	AllowedUIDs []uint32
	Version     string
	Log         *slog.Logger
}

type ctxKey struct{}

// Handler returns the API routes, wrapped in the caller check.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.Version})
	})
	mux.HandleFunc("POST /v1/ports/unprivileged-start", func(w http.ResponseWriter, r *http.Request) {
		s.do(w, r, "set unprivileged port start", s.Host.SetUnprivilegedPortStart)
	})
	mux.HandleFunc("PUT /v1/firewall", func(w http.ResponseWriter, r *http.Request) {
		var fw Firewall
		if !decode(w, r, 64<<10, &fw) {
			return
		}
		if err := fw.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.do(w, r, "apply firewall", func() error { return s.Host.ApplyFirewall(fw.Ruleset()) })
	})
	mux.HandleFunc("PUT /v1/timezone", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Timezone string `json:"timezone"`
		}
		if !decode(w, r, 4<<10, &req) {
			return
		}
		if !ValidTimezone(req.Timezone) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid time zone %q", req.Timezone))
			return
		}
		s.do(w, r, "set time zone "+req.Timezone, func() error { return s.Host.SetTimezone(req.Timezone) })
	})
	// Streams newline-delimited JSON: {"line": ...} as output arrives,
	// then {"done": true, "error": ...}.
	mux.HandleFunc("POST /v1/updates/run", func(w http.ResponseWriter, r *http.Request) {
		uid, _ := r.Context().Value(ctxKey{}).(uint32)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		st := &lineStream{enc: json.NewEncoder(w), flush: w.(http.Flusher).Flush}
		// Once started, updates run to the end even if the caller goes
		// away (e.g. Roostack restarts): stopping dpkg halfway leaves packages
		// half-installed.
		err := s.Host.RunUpdates(context.WithoutCancel(r.Context()), st)
		st.close()
		end := map[string]any{"done": true}
		if err != nil {
			s.Log.Error("OS updates failed", "uid", uid, "err", err)
			end["error"] = err.Error()
		} else {
			s.Log.Info("OS updates", "uid", uid)
		}
		st.mu.Lock()
		st.enc.Encode(end)
		st.flush()
		st.mu.Unlock()
	})
	mux.HandleFunc("POST /v1/reboot", func(w http.ResponseWriter, r *http.Request) {
		s.do(w, r, "reboot", s.Host.Reboot)
	})
	iface := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		name := r.URL.Query().Get("interface")
		if name == "" {
			name = WireGuardInterface
		}
		if !ValidInterface(name) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid interface %q", name))
			return "", false
		}
		return name, true
	}
	mux.HandleFunc("GET /v1/wireguard", func(w http.ResponseWriter, r *http.Request) {
		name, ok := iface(w, r)
		if !ok {
			return
		}
		st, err := s.Host.WireGuardStatus(name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("GET /v1/drives", func(w http.ResponseWriter, r *http.Request) {
		drives, err := s.Host.Drives()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, drives)
	})
	mux.HandleFunc("POST /v1/drives/use", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		if !decode(w, r, 4<<10, &req) {
			return
		}
		used, err := s.Host.UseDrive(req.ID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.Log.Info("backup drive ready", "id", req.ID, "dir", used.Dir)
		writeJSON(w, http.StatusOK, used)
	})
	mux.HandleFunc("POST /v1/drives/erase", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Disk  string `json:"disk"`
			Label string `json:"label"`
		}
		if !decode(w, r, 4<<10, &req) {
			return
		}
		uid, _ := r.Context().Value(ctxKey{}).(uint32)
		s.Log.Warn("erasing a disk for backups", "disk", req.Disk, "uid", uid)
		if err := s.Host.EraseDrive(req.Disk, req.Label); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /v1/backup-folder", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Dir string `json:"dir"`
		}
		if !decode(w, r, 4<<10, &req) {
			return
		}
		dir, err := s.Host.PrepareBackupFolder(req.Dir)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.Log.Info("prepared backup folder", "dir", dir)
		writeJSON(w, http.StatusOK, map[string]string{"dir": dir})
	})
	mux.HandleFunc("GET /v1/host/changes", func(w http.ResponseWriter, r *http.Request) {
		c, err := s.Host.HostChanges()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, c)
	})
	// The keys go into Roostack's backups, like its CA's key, so a restored
	// lab keeps the keys its peers know.
	mux.HandleFunc("GET /v1/wireguard/keys", func(w http.ResponseWriter, r *http.Request) {
		keys, err := s.Host.WireGuardKeys()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, keys)
	})
	mux.HandleFunc("DELETE /v1/wireguard", func(w http.ResponseWriter, r *http.Request) {
		name, ok := iface(w, r)
		if !ok {
			return
		}
		s.do(w, r, "remove wireguard "+name, func() error { return s.Host.RemoveWireGuard(name) })
	})
	mux.HandleFunc("PUT /v1/wireguard", func(w http.ResponseWriter, r *http.Request) {
		var wg WireGuard
		if !decode(w, r, 256<<10, &wg) {
			return
		}
		if err := wg.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		uid, _ := r.Context().Value(ctxKey{}).(uint32)
		pub, err := s.Host.ApplyWireGuard(wg)
		if err != nil {
			s.Log.Error("apply wireguard failed", "uid", uid, "err", err)
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		s.Log.Info("apply wireguard", "uid", uid, "address", wg.Address, "peers", len(wg.Peers))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "public_key": pub})
	})
	return s.checkCaller(mux)
}

// decode reads a request body of at most limit bytes into v, refusing
// unknown fields (the agent never guesses at its input). It writes the 400
// itself and reports whether to go on.
func decode(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func (s *Server) do(w http.ResponseWriter, r *http.Request, what string, fn func() error) {
	uid, _ := r.Context().Value(ctxKey{}).(uint32)
	if err := fn(); err != nil {
		s.Log.Error(what+" failed", "uid", uid, "err", err)
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.Log.Info(what, "uid", uid)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// checkCaller rejects connections from users other than AllowedUIDs.
func (s *Server) checkCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := r.Context().Value(ctxKey{}).(uint32)
		allowed := false
		for _, a := range s.AllowedUIDs {
			allowed = allowed || (ok && uid == a)
		}
		if !allowed {
			s.Log.Warn("rejected caller", "uid", uid, "known", ok, "path", r.URL.Path)
			writeError(w, http.StatusForbidden, errors.New("caller not allowed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Serve listens on the Unix socket until ctx is cancelled. The socket is
// created mode 0660 with the given group, so only root and that group can
// connect; SO_PEERCRED is still checked on every request.
func (s *Server) Serve(ctx context.Context, socket string, gid int) error {
	os.Remove(socket) // stale socket from a previous run
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err := os.Chown(socket, -1, gid); err != nil {
		ln.Close()
		return err
	}
	if err := os.Chmod(socket, 0o660); err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{
		Handler: s.Handler(),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if uid, ok := peerUID(c); ok {
				return context.WithValue(ctx, ctxKey{}, uid)
			}
			return ctx
		},
	}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// peerUID returns the UID of the process on the other end of a Unix socket.
func peerUID(c net.Conn) (uint32, bool) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *syscall.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if err != nil || credErr != nil {
		return 0, false
	}
	return cred.Uid, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"ok": false, "error": err.Error()})
}

// LinuxHost performs actions on the real host.
type LinuxHost struct {
	FirewallFile string
	// UID and GID are the roostack user's (backup folders it may use).
	UID, GID int
}

// SetUnprivilegedPortStart writes the sysctl for now and for boot.
func (h LinuxHost) SetUnprivilegedPortStart() error {
	const conf = "# Managed by roostack-agent: lets rootless containers bind ports 80 and 443.\n" +
		"net.ipv4.ip_unprivileged_port_start = 80\n"
	if err := os.WriteFile(sysctlFile, []byte(conf), 0o644); err != nil {
		return err
	}
	return os.WriteFile(unprivPortProc, []byte("80\n"), 0o644)
}

// ApplyFirewall checks the ruleset with `nft -c`, loads it, then saves it.
func (h LinuxHost) ApplyFirewall(ruleset string) error {
	tmp, err := os.CreateTemp("", "roostack-firewall-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(ruleset); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if out, err := exec.Command("nft", "-c", "-f", tmp.Name()).CombinedOutput(); err != nil {
		return fmt.Errorf("nft check: %w: %s", err, out)
	}
	if out, err := exec.Command("nft", "-f", tmp.Name()).CombinedOutput(); err != nil {
		return fmt.Errorf("nft apply: %w: %s", err, out)
	}
	if err := os.MkdirAll(filepath.Dir(h.FirewallFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(h.FirewallFile, []byte(ruleset), 0o644)
}

// SetTimezone runs timedatectl, which also checks the zone exists.
func (h LinuxHost) SetTimezone(name string) error {
	if out, err := exec.Command("timedatectl", "set-timezone", name).CombinedOutput(); err != nil {
		return fmt.Errorf("timedatectl: %w: %s", err, out)
	}
	return nil
}

// updatesTimeout bounds one run of OS updates.
const updatesTimeout = time.Hour

// updatesScript announces its steps with "==> " lines (see
// internal/activity).
const updatesScript = `echo "==> Downloading the list of available updates"
apt-get update -q || exit
echo "==> Installing updates"
unattended-upgrade -v`

// RunUpdates runs apt-get update and unattended-upgrade in a transient
// systemd unit, outside the agent's own sandbox (dpkg scripts may touch
// anything), streaming their output to w.
func (h LinuxHost) RunUpdates(ctx context.Context, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, updatesTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemd-run", "--unit=roostack-os-updates", "--wait", "--pipe", "--quiet",
		"--collect", "--setenv=DEBIAN_FRONTEND=noninteractive", "--", "sh", "-c", updatesScript)
	out := &lineFilter{w: w, drop: dpkgLogMarker}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	out.flush()
	return err
}

// dpkgLogMarker: unattended-upgrade -v prints "Log started: <time>" and
// "Log ended: <time>" around every dpkg run (dozens in one update), which
// tell nobody anything.
func dpkgLogMarker(line string) bool {
	return strings.HasPrefix(line, "Log started: ") || strings.HasPrefix(line, "Log ended: ")
}

// lineFilter passes output on a line at a time, leaving out the lines drop
// says to.
type lineFilter struct {
	mu      sync.Mutex
	w       io.Writer
	drop    func(line string) bool
	partial string
}

func (f *lineFilter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	lines := strings.Split(f.partial+string(p), "\n")
	f.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		if f.drop(strings.TrimRight(line, "\r")) {
			continue
		}
		if _, err := io.WriteString(f.w, line+"\n"); err != nil {
			return len(p), err
		}
	}
	return len(p), nil
}

// flush passes on an unfinished last line.
func (f *lineFilter) flush() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.partial != "" && !f.drop(f.partial) {
		io.WriteString(f.w, f.partial)
	}
	f.partial = ""
}

// lineStream sends written output as {"line": ...} JSON lines. If the
// caller has gone, output is dropped rather than failing the write, which
// would close the command's output and kill it (SIGPIPE).
type lineStream struct {
	mu      sync.Mutex
	enc     *json.Encoder
	flush   func()
	partial string
	gone    bool
}

func (s *lineStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return len(p), nil
	}
	lines := strings.Split(s.partial+string(p), "\n")
	s.partial = lines[len(lines)-1]
	for _, line := range lines[:len(lines)-1] {
		if err := s.enc.Encode(map[string]string{"line": line}); err != nil {
			s.gone = true
			return len(p), nil
		}
	}
	s.flush()
	return len(p), nil
}

func (s *lineStream) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.partial != "" {
		s.enc.Encode(map[string]string{"line": s.partial})
		s.partial = ""
	}
}

// Reboot asks systemd to reboot after a moment, so the API call returns.
func (h LinuxHost) Reboot() error {
	return exec.Command("systemd-run", "--on-active=5", "--unit=roostack-reboot", "--collect",
		"systemctl", "reboot").Run()
}

// WireGuardKeys reads Roostack's interfaces' private keys.
func (h LinuxHost) WireGuardKeys() (map[string]string, error) { return WireGuardKeys() }

// ApplyWireGuard writes an interface's wg-quick file and brings it up. A
// running interface takes new peers without dropping connections (wg
// syncconf); a new address or key restarts it.
func (h LinuxHost) ApplyWireGuard(wg WireGuard) (string, error) {
	iface := wg.Iface()
	file := wireGuardConf(iface)
	old, _ := os.ReadFile(file)
	key := confKey(string(old))
	switch {
	case wg.JoinKey != "":
		key = wg.JoinKey
	case key == "" && wg.SavedKey != "" && !wg.NewKey:
		key = wg.SavedKey
	case key == "" || wg.NewKey:
		var err error
		if key, err = NewWireGuardKey(); err != nil {
			return "", err
		}
	}
	pub, err := WireGuardPublicKey(key)
	if err != nil {
		return "", err
	}
	conf := wg.Config(key)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(file+".tmp", []byte(conf), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(file+".tmp", file); err != nil {
		return "", err
	}
	unit := "wg-quick@" + iface
	if out, err := exec.Command("systemctl", "enable", unit).CombinedOutput(); err != nil {
		return "", fmt.Errorf("enabling %s: %w: %s", unit, err, out)
	}
	_, statErr := os.Stat("/sys/class/net/" + iface)
	sameIface := statErr == nil && confKey(string(old)) == key && confAddress(string(old)) == wg.Address
	if sameIface {
		// wg syncconf takes the file without wg-quick's extra keys.
		strip, err := exec.Command("wg-quick", "strip", iface).Output()
		if err != nil {
			return "", fmt.Errorf("wg-quick strip: %w", err)
		}
		cmd := exec.Command("wg", "syncconf", iface, "/dev/stdin")
		cmd.Stdin = strings.NewReader(string(strip))
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("wg syncconf: %w: %s", err, out)
		}
		return pub, nil
	}
	if out, err := exec.Command("systemctl", "restart", unit).CombinedOutput(); err != nil {
		return "", fmt.Errorf("starting %s: %w: %s", unit, err, out)
	}
	return pub, nil
}

// RemoveWireGuard stops and disables an interface and deletes its file.
func (h LinuxHost) RemoveWireGuard(iface string) error {
	unit := "wg-quick@" + iface
	if out, err := exec.Command("systemctl", "disable", "--now", unit).CombinedOutput(); err != nil {
		return fmt.Errorf("stopping %s: %w: %s", unit, err, out)
	}
	if err := os.Remove(wireGuardConf(iface)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// WireGuardStatus reads an interface's wg-quick file and when each peer
// last connected.
func (h LinuxHost) WireGuardStatus(iface string) (WireGuardStatus, error) {
	file := wireGuardConf(iface)
	conf, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return WireGuardStatus{}, nil
	}
	if err != nil {
		return WireGuardStatus{}, err
	}
	pub, err := WireGuardPublicKey(confKey(string(conf)))
	if err != nil {
		return WireGuardStatus{}, fmt.Errorf("%s: %w", file, err)
	}
	st := WireGuardStatus{Configured: true, PublicKey: pub, Address: confAddress(string(conf)), Handshakes: map[string]int64{}}
	if out, err := exec.Command("wg", "show", iface, "latest-handshakes").Output(); err == nil {
		st.Handshakes = parseHandshakes(string(out))
	}
	return st, nil
}

// RestoreFirewall re-applies the saved ruleset at agent start, if any.
func (h LinuxHost) RestoreFirewall() error {
	if _, err := os.Stat(h.FirewallFile); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if out, err := exec.Command("nft", "-f", h.FirewallFile).CombinedOutput(); err != nil {
		return fmt.Errorf("nft restore: %w: %s", err, out)
	}
	return nil
}
