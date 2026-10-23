// Package agent is roostack-agent: the small root helper that performs a fixed
// set of privileged operations for the unprivileged roostack service
// (SPEC.md §6.1).
//
// It serves HTTP+JSON on a Unix socket. Callers are identified by the
// kernel (SO_PEERCRED), not by anything they send, and only root and the
// roostack user are accepted, and only from the host's own user namespace:
// a rootless container's root is the roostack user to the kernel too.
// Every operation validates its input and takes no free-form commands or
// paths.
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
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/fsutil"
)

// Default locations. The socket is in the agent's runtime directory, the
// one place in /run its sandbox lets it write (roostack-agent.service).
const (
	DefaultSocket       = "/run/roostack-agent/agent.sock"
	DefaultFirewallFile = "/etc/roostack/firewall.nft"
	sysctlFile          = "/etc/sysctl.d/60-roostack.conf"
	unprivPortProc      = "/proc/sys/net/ipv4/ip_unprivileged_port_start"
)

// Host performs the privileged actions. Tests replace it.
//
// The server checks every request before a Host sees it (wire.go): the
// names that make paths (services, libraries, people, interfaces), the
// time zone and the release series are valid. A Host's error is its own
// failure, which the caller gets as a 500, unless it's a refusal (refuse):
// something about what was asked that a person can change, a 400.
type Host interface {
	// SetUnprivilegedPortStart lets rootless containers bind ports >= 80
	// (docs/ASSUMPTIONS.md C3), now and after reboot.
	SetUnprivilegedPortStart() error
	// ApplyFirewall checks and loads an nftables ruleset, and saves it so it
	// is re-applied when the agent starts (firewall.go).
	ApplyFirewall(ruleset string) error
	// AllowWireGuardJoin lets WireGuard's port in until the firewall is
	// next applied: a server joining a lab whose controller dials it
	// (SPEC.md §10.2), before its own firewall says so.
	AllowWireGuardJoin() error
	// SetTimezone sets the host's time zone (timezone.go).
	SetTimezone(name string) error
	// RunUpdates refreshes the package lists and installs available
	// updates with unattended-upgrades, writing its output to w as it runs.
	// A series first moves Roostack's apt source to that release series
	// and installs it (updates.go).
	RunUpdates(ctx context.Context, series string, w io.Writer) error
	// UpdatesStatus reads the latest run of updates, which may have
	// outlived the agent that started it (updates.go).
	UpdatesStatus() (UpdatesRun, error)
	// Reboot restarts the host shortly after returning.
	Reboot() error
	// ApplyWireGuard configures one of Roostack's WireGuard interfaces
	// (already validated) and brings it up, returning its public key
	// (wireguard.go).
	ApplyWireGuard(wg WireGuard) (publicKey string, err error)
	// ApplyLinks sets the direct links to other servers on Roostack's
	// network, keeping the link to the controller.
	ApplyLinks(l Links) error
	// WireGuardStatus reads an interface's configuration.
	WireGuardStatus(iface string) (WireGuardStatus, error)
	// WireGuardKeys reads Roostack's interfaces' private keys.
	WireGuardKeys() (map[string]string, error)
	// RemoveWireGuard takes an interface down and deletes its
	// configuration (e.g. the server left its lab).
	RemoveWireGuard(iface string) error
	// HostChanges checks for changes Roostack didn't make (SPEC.md §3.2).
	HostChanges() (HostChanges, error)
	// PrepareBackupFolder readies a backup folder (folder.go).
	PrepareBackupFolder(dir string) (string, error)
	// Drives lists the places backups can go; UseDrive readies one;
	// EraseDrive erases a disk and prepares it (drives.go).
	Drives() ([]Drive, error)
	UseDrive(id string) (UsedDrive, error)
	EraseDrive(disk, label string) error
	// LibraryFolders lists the folders where a media library could be;
	// MountLibrary mounts one for good, UnmountLibrary forgets it, and
	// Libraries lists them (library.go).
	LibraryFolders(l Library) (FolderList, error)
	MountLibrary(l Library) error
	UnmountLibrary(name string) error
	Libraries() ([]LibraryStatus, error)
	// SetLibraryViews sets the libraries a service may read (its view
	// folder), and those at their old paths' shared parent (its second
	// view folder: name → library); RemoveLibraryViews removes its folders.
	SetLibraryViews(service string, libraries []string, at map[string]string) error
	RemoveLibraryViews(service string) error
	// CheckShare mounts a share for a moment to list it; SaveShareLogin
	// keeps an SMB login for a library, root's only (shares.go).
	CheckShare(s Share) (FolderList, error)
	SaveShareLogin(name string, s Share) error
	// MountImport mounts the place to bring a service's data in from,
	// read-only, returning where; UnmountImport forgets it (import.go).
	MountImport(service string, l Library) (string, error)
	UnmountImport(service string) error
	// MountPhotos mounts a person's export for migrating photos into a
	// service, read-only, returning where; UnmountPhotos forgets it
	// (photos.go).
	MountPhotos(service, who string, l Library) (string, error)
	UnmountPhotos(service, who string) error
	// SSHStatus reads what the SSH server does; SetSSHKeysOnly writes or
	// removes Roostack's drop-in in its configuration (ssh.go).
	SSHStatus() (SSHStatus, error)
	SetSSHKeysOnly(on bool) error
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

// nsKey marks a connection from another user namespace (peerInHostNS).
type nsKey struct{}

// Handler returns the API routes, wrapped in the caller check: the whole
// privilege boundary. Each route says what it changes, for the agent's
// log (reads say nothing), and the most its request's body may hold.
func (s *Server) Handler() http.Handler {
	h := s.Host
	mux := http.NewServeMux()
	handle := mux.HandleFunc
	handle("GET /v1/health", read(s, func() (Health, error) { return Health{OK: true, Version: s.Version}, nil }))

	// The firewall, the time zone, OS updates and rebooting.
	handle("POST /v1/ports/unprivileged-start", do(s, "set unprivileged port start", h.SetUnprivilegedPortStart))
	handle("PUT /v1/firewall", action(s, "apply firewall", 64<<10, func(fw Firewall) error { return h.ApplyFirewall(fw.Ruleset()) }))
	// No arguments: it only ever opens WireGuard's own port.
	handle("POST /v1/firewall/wireguard-join", do(s, "open wireguard for joining", h.AllowWireGuardJoin))
	handle("PUT /v1/timezone", action(s, "set time zone", 4<<10, func(r TimezoneRequest) error { return h.SetTimezone(r.Timezone) }))
	handle("POST /v1/updates/run", s.runUpdates)
	handle("GET /v1/updates/last", read(s, h.UpdatesStatus))
	handle("POST /v1/reboot", do(s, "reboot", h.Reboot))

	// Roostack's WireGuard interfaces. The keys go into Roostack's backups,
	// like its CA's key, so a restored lab keeps the keys its peers know.
	handle("GET /v1/wireguard", route(s, "", 0, func(r InterfaceRequest) (WireGuardStatus, error) { return h.WireGuardStatus(r.Interface) }))
	handle("PUT /v1/wireguard", route(s, "apply wireguard", 256<<10, func(wg WireGuard) (WireGuardApplied, error) {
		pub, err := h.ApplyWireGuard(wg)
		return WireGuardApplied{OK: true, PublicKey: pub}, err
	}))
	handle("DELETE /v1/wireguard", action(s, "remove wireguard", 0, func(r InterfaceRequest) error { return h.RemoveWireGuard(r.Interface) }))
	handle("PUT /v1/wireguard/links", action(s, "apply direct links", 64<<10, h.ApplyLinks))
	handle("GET /v1/wireguard/keys", read(s, h.WireGuardKeys))

	// Backups: drives and folders.
	handle("GET /v1/drives", read(s, h.Drives))
	handle("POST /v1/drives/use", route(s, "use backup drive", 4<<10, func(r UseDriveRequest) (UsedDrive, error) { return h.UseDrive(r.ID) }))
	handle("POST /v1/drives/erase", action(s, "erase disk for backups", 4<<10, func(r EraseDriveRequest) error {
		s.Log.Warn("erasing a disk for backups", "disk", r.Disk)
		return h.EraseDrive(r.Disk, r.Label)
	}))
	handle("POST /v1/backup-folder", route(s, "prepare backup folder", 4<<10, func(r BackupFolder) (BackupFolder, error) {
		dir, err := h.PrepareBackupFolder(r.Dir)
		return BackupFolder{Dir: dir}, err
	}))

	// Media libraries (SPEC.md §8.1) and the services that read them.
	handle("GET /v1/libraries", read(s, h.Libraries))
	handle("POST /v1/libraries/folders", route(s, "", 8<<10, h.LibraryFolders))
	handle("PUT /v1/libraries/{name}", action(s, "mount media library", 8<<10, func(r mountLibrary) error { return h.MountLibrary(r.Library) }))
	handle("DELETE /v1/libraries/{name}", action(s, "remove media library", 0, func(r LibraryRequest) error { return h.UnmountLibrary(r.Name) }))
	handle("PUT /v1/library-views/{service}", action(s, "set library views", 8<<10, func(r ViewsRequest) error {
		return h.SetLibraryViews(r.Service, r.Libraries, r.At)
	}))
	handle("DELETE /v1/library-views/{service}", action(s, "remove library views", 0, func(r ServiceRequest) error { return h.RemoveLibraryViews(r.Service) }))
	handle("POST /v1/shares/check", route(s, "", 8<<10, h.CheckShare))
	handle("PUT /v1/shares/{name}/login", action(s, "save share login", 8<<10, func(r ShareLoginRequest) error { return h.SaveShareLogin(r.Name, r.Share) }))

	// Bringing data into a service (SPEC.md §8.4), and migrating photos
	// into Immich (SPEC.md §8.5).
	handle("PUT /v1/imports/{service}", route(s, "mount place to bring data in from", 8<<10, func(r ImportRequest) (Mounted, error) {
		path, err := h.MountImport(r.Service, r.Library)
		return Mounted{Path: path}, err
	}))
	handle("DELETE /v1/imports/{service}", action(s, "unmount place to bring data in from", 0, func(r ServiceRequest) error { return h.UnmountImport(r.Service) }))
	handle("PUT /v1/photos/{service}/{who}", route(s, "mount export to migrate photos from", 8<<10, func(r PhotosRequest) (Mounted, error) {
		path, err := h.MountPhotos(r.Service, r.Who, r.Library)
		return Mounted{Path: path}, err
	}))
	handle("DELETE /v1/photos/{service}/{who}", action(s, "unmount export to migrate photos from", 0, func(r PhotosRequest) error {
		return h.UnmountPhotos(r.Service, r.Who)
	}))

	// The host itself: changes Roostack didn't make, and SSH hardening
	// (ssh.go), only on or off, the drop-in's content being the agent's own.
	handle("GET /v1/host/changes", read(s, h.HostChanges))
	// What could be read comes back with the error, so the page still
	// knows there's an SSH server.
	handle("GET /v1/ssh", read(s, func() (SSHStatus, error) {
		st, err := h.SSHStatus()
		if err != nil {
			st.Error = err.Error()
		}
		return st, nil
	}))
	handle("PUT /v1/ssh", action(s, "set ssh keys only", 4<<10, func(r SSHRequest) error { return h.SetSSHKeysOnly(r.KeysOnly) }))
	return s.checkCaller(mux)
}

// none is a request with nothing in it.
type none struct{}

// A request that takes values from its URL gets them from fromURL, and
// one with a Validate is refused (400) unless it passes, before the
// route's work starts. A request or answer with logAttrs says what it was
// in the agent's log, never a password.
type (
	urlRequest interface{ fromURL(r *http.Request) }
	validated  interface{ Validate() error }
	logged     interface{ logAttrs() []any }
)

// route serves a call that answers with a Resp. Its request is decoded
// from a body of at most limit bytes (0: it has none), filled in from the
// URL and validated. what says what the call changes, logged with the
// caller and the request whether it worked or not; a call that only reads
// has none.
func route[Req, Resp any](s *Server, what string, limit int64, fn func(Req) (Resp, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Req
		if limit > 0 && !decode(w, r, limit, &req) {
			return
		}
		if u, ok := any(&req).(urlRequest); ok {
			u.fromURL(r)
		}
		if v, ok := any(req).(validated); ok {
			if err := v.Validate(); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
		resp, err := fn(req)
		if what != "" {
			s.logCall(r, what, err, req, resp)
		}
		if err != nil {
			writeError(w, statusOf(err), err)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// action serves a call that changes something and answers only that it
// worked (route).
func action[Req any](s *Server, what string, limit int64, fn func(Req) error) http.HandlerFunc {
	return route(s, what, limit, func(req Req) (Answer, error) { return Answer{OK: true}, fn(req) })
}

// do serves an action with no request.
func do(s *Server, what string, fn func() error) http.HandlerFunc {
	return action(s, what, 0, func(none) error { return fn() })
}

// read serves a call that only reads, with no request.
func read[Resp any](s *Server, fn func() (Resp, error)) http.HandlerFunc {
	return route(s, "", 0, func(none) (Resp, error) { return fn() })
}

// logCall logs a call that changes something: what it did, or that it
// failed and why, with the caller and what its request (and, once it
// worked, its answer) say.
func (s *Server) logCall(r *http.Request, what string, err error, req, resp any) {
	uid, _ := r.Context().Value(ctxKey{}).(uint32)
	attrs := []any{"uid", uid}
	if l, ok := req.(logged); ok {
		attrs = append(attrs, l.logAttrs()...)
	}
	if err != nil {
		s.Log.Error(what+" failed", append(attrs, "err", err)...)
		return
	}
	if l, ok := resp.(logged); ok {
		attrs = append(attrs, l.logAttrs()...)
	}
	s.Log.Info(what, attrs...)
}

// refusal is a Host refusing what was asked, for a reason a person can
// change: a drive that isn't connected, a folder outside the allowed
// places. The caller gets it as a 400, where the agent's own failures are
// 500s.
type refusal struct{ error }

// refuse makes a refusal from a sentence, as fmt.Errorf does.
func refuse(format string, a ...any) error { return refusal{fmt.Errorf(format, a...)} }

// statusOf is the status for a Host's error.
func statusOf(err error) int {
	if errors.As(err, new(refusal)) {
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// runUpdates runs OS updates, streaming newline-delimited JSON
// (UpdatesMessage): each line of output as it arrives, then how it ended.
func (s *Server) runUpdates(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(ctxKey{}).(uint32)
	var req UpdatesRequest
	if r.ContentLength != 0 && !decode(w, r, 4<<10, &req) {
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	st := &lineStream{enc: json.NewEncoder(w), flush: w.(http.Flusher).Flush}
	// Once started, updates run to the end even if the caller goes
	// away (e.g. Roostack restarts): stopping dpkg halfway leaves packages
	// half-installed.
	err := s.Host.RunUpdates(context.WithoutCancel(r.Context()), req.Series, st)
	st.close()
	end := UpdatesMessage{Done: true}
	if err != nil {
		s.Log.Error("OS updates failed", "uid", uid, "series", req.Series, "err", err)
		end.Error = err.Error()
	} else {
		s.Log.Info("OS updates", "uid", uid, "series", req.Series)
	}
	st.mu.Lock()
	st.enc.Encode(end)
	st.flush()
	st.mu.Unlock()
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

// checkCaller rejects connections from users other than AllowedUIDs.
func (s *Server) checkCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uid, ok := r.Context().Value(ctxKey{}).(uint32)
		foreign, _ := r.Context().Value(nsKey{}).(bool)
		allowed := false
		for _, a := range s.AllowedUIDs {
			allowed = allowed || (ok && !foreign && uid == a)
		}
		if !allowed {
			s.Log.Warn("rejected caller", "uid", uid, "known", ok, "other_user_namespace", foreign, "path", r.URL.Path)
			writeError(w, http.StatusForbidden, errors.New("caller not allowed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Serve listens on the Unix socket until ctx is cancelled. The socket is
// created mode 0660 with the given group, so only root and that group can
// connect; SO_PEERCRED is still checked on every request, and the caller's
// user namespace (peerInHostNS).
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
			uid, pid, ok := peerCred(c)
			if !ok {
				return ctx
			}
			ctx = context.WithValue(ctx, ctxKey{}, uid)
			if !peerInHostNS(c, pid) {
				ctx = context.WithValue(ctx, nsKey{}, true)
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

// fdinfoPid reads the "Pid:" line of a pidfd's /proc/self/fdinfo entry
// (-1: the process has exited).
func fdinfoPid(info string) int32 {
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(line, "Pid:"); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
			if err != nil {
				return 0
			}
			return int32(n)
		}
	}
	return 0
}

// hostUIDMap reports whether a /proc/<pid>/uid_map is the host's: one
// line mapping every UID to itself.
func hostUIDMap(m string) bool {
	return slices.Equal(strings.Fields(m), []string{"0", "0", "4294967295"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, Answer{Error: err.Error()})
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
	if err := fsutil.WriteFile(sysctlFile, []byte(conf), 0o644); err != nil {
		return err
	}
	return os.WriteFile(unprivPortProc, []byte("80\n"), 0o644)
}
