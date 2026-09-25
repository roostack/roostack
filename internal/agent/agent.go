// Package agent is mylab-agent: the small root helper that performs a fixed
// set of privileged operations for the unprivileged mylab service
// (SPEC.md §6.1).
//
// It serves HTTP+JSON on a Unix socket. Callers are identified by the
// kernel (SO_PEERCRED), not by anything they send, and only root and the
// mylab user are accepted. Every operation validates its input and takes no
// free-form commands or paths.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// Default locations.
const (
	DefaultSocket       = "/run/mylab-agent.sock"
	DefaultFirewallFile = "/etc/mylab/firewall.nft"
	sysctlFile          = "/etc/sysctl.d/60-mylab.conf"
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
}

// Server is the agent's HTTP API.
type Server struct {
	Host Host
	// AllowedUIDs may call the API (root and the mylab user).
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
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&fw); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := fw.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		s.do(w, r, "apply firewall", func() error { return s.Host.ApplyFirewall(fw.Ruleset()) })
	})
	return s.checkCaller(mux)
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
}

// SetUnprivilegedPortStart writes the sysctl for now and for boot.
func (h LinuxHost) SetUnprivilegedPortStart() error {
	const conf = "# Managed by mylab-agent: lets rootless containers bind ports 80 and 443.\n" +
		"net.ipv4.ip_unprivileged_port_start = 80\n"
	if err := os.WriteFile(sysctlFile, []byte(conf), 0o644); err != nil {
		return err
	}
	return os.WriteFile(unprivPortProc, []byte("80\n"), 0o644)
}

// ApplyFirewall checks the ruleset with `nft -c`, loads it, then saves it.
func (h LinuxHost) ApplyFirewall(ruleset string) error {
	tmp, err := os.CreateTemp("", "mylab-firewall-*.nft")
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
