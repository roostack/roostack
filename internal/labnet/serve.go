package labnet

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/nodeid"
)

// Serve runs the lab API on this server's address on Roostack's network
// until ctx ends. The address appears once WireGuard is up (on a new
// controller, when the first join code is made), so it keeps trying.
func Serve(ctx context.Context, stateDir string, handler http.Handler, log *slog.Logger) {
	var ln net.Listener
	for {
		m, ok, err := LoadMembership(stateDir)
		if err == nil && ok {
			addr := net.JoinHostPort(m.Addr.String(), strconv.Itoa(Port))
			if ln, err = net.Listen("tcp", addr); err == nil {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
	tlsConf, err := Identity{Dir: IdentityDir(stateDir)}.ServerTLS()
	if err != nil {
		log.Error("lab API", "err", err)
		ln.Close()
		return
	}
	srv := &http.Server{Handler: handler, TLSConfig: tlsConf, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	log.Info("lab API listening", "addr", ln.Addr())
	if err := srv.ServeTLS(ln, "", ""); !errors.Is(err, http.ErrServerClosed) {
		log.Error("lab API", "err", err)
	}
}

// Caller is the verified server making a lab API request, or "".
func Caller(r *http.Request) string { return PeerNode(r.TLS) }

// JoinHandler serves POST /v1/join: a server presenting a join code.
func (h *Hub) JoinHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req JoinRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			WriteError(w, http.StatusBadRequest, err)
			return
		}
		resp, err := h.Join(r.Context(), req)
		if err != nil {
			h.Log.Warn("join refused", "remote", r.RemoteAddr, "err", err)
			WriteError(w, http.StatusForbidden, err)
			return
		}
		WriteJSON(w, http.StatusOK, resp)
	}
}

// JoinLab joins this server to a lab with a code (`roostack node join`):
// WireGuard comes up with the code's one-time key, the controller signs
// this server's certificate and learns its own key, then WireGuard
// switches to that key.
func JoinLab(ctx context.Context, code JoinCode, self nodeid.Identity, stateDir string, ag WireGuardAgent, log func(string, ...any)) error {
	if m, ok, _ := LoadMembership(stateDir); ok {
		return fmt.Errorf("this server is already a %s in a lab", m.Role)
	}
	ownKey, err := agent.NewWireGuardKey()
	if err != nil {
		return err
	}
	ownPub, _ := agent.WireGuardPublicKey(ownKey)
	wg := agent.WireGuard{Address: fmt.Sprintf("%s/%d", code.Addr, code.PrefixBits),
		Peers: []agent.Peer{{PublicKey: code.ControllerKey, Endpoint: code.Endpoint, Keepalive: Keepalive,
			AllowedIPs: []string{netip.PrefixFrom(code.Addr, code.PrefixBits).Masked().String()}}}}

	log("Connecting to the controller at %s over WireGuard", code.Endpoint)
	wg.JoinKey = code.JoinKey
	if _, err := ag.ApplyWireGuard(ctx, wg); err != nil {
		return err
	}
	id := Identity{Dir: IdentityDir(stateDir)}
	csr, err := id.CSR(self.ID)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(JoinRequest{Token: code.Token, NodeID: self.ID, Name: self.Name, PublicKey: ownPub, CSR: csr})
	client := &http.Client{Timeout: 10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: JoinTLS(code.CA, code.ControllerID)}}
	url := fmt.Sprintf("https://%s/v1/join", net.JoinHostPort(code.ControllerAddr.String(), strconv.Itoa(Port)))
	var resp JoinResponse
	// The first WireGuard handshake can take a few seconds; none at all
	// after a while means the port can't be reached (as for storage at
	// another lab, guests.go).
	start := time.Now()
	handshake := false
	for {
		err = post(ctx, client, url, body, &resp)
		var refused *RemoteError
		if err == nil || errors.As(err, &refused) || time.Since(start) > joinTimeout {
			break
		}
		if !handshake {
			if handshake = linkUp(ctx, ag, agent.WireGuardInterface, code.ControllerKey); !handshake && time.Since(start) > handshakeTimeout {
				break
			}
		}
		time.Sleep(2 * time.Second)
	}
	var refused *RemoteError
	if err != nil && !errors.As(err, &refused) {
		_, port, _ := net.SplitHostPort(code.Endpoint)
		lab := cmp.Or(code.LabName, "your lab")
		if !handshake && !linkUp(ctx, ag, agent.WireGuardInterface, code.ControllerKey) {
			return fmt.Errorf("couldn't reach %s at %s. Its UDP port %s has to let this server in: at home, this server on the same "+
				"network as the lab's, or the port forwarded on the router; on a cloud server, the provider's firewall. "+
				"(Details: no WireGuard handshake in %d seconds.)", lab, code.Endpoint, port, int(time.Since(start).Seconds()))
		}
		return fmt.Errorf("the link to %s at %s works, but Roostack there didn't answer: it may be restarting, so try again "+
			"in a minute. (Details: %v)", lab, code.Endpoint, err)
	}
	if err != nil {
		return fmt.Errorf("the controller refused to add this server: %w", err)
	}
	if err := id.Save(resp.Cert, resp.CA); err != nil {
		return err
	}
	log("Joined. Switching to this server's own WireGuard key")
	wg.JoinKey = ownKey
	if _, err := ag.ApplyWireGuard(ctx, wg); err != nil {
		return err
	}
	return Membership{Role: config.RoleNode, Addr: code.Addr, PrefixBits: code.PrefixBits,
		ControllerID: code.ControllerID, ControllerAddr: code.ControllerAddr, LabName: code.LabName, UI: code.UI}.Save(stateDir)
}

// RemoteError is an error reported by the other server.
type RemoteError struct {
	Status int
	Msg    string
	// Kind classifies it (e.g. "not_found", "form"), so callers can
	// rebuild the error type.
	Kind   string
	Title  string
	Output string
}

func (e *RemoteError) Error() string { return e.Msg }

// WriteJSON writes a JSON response.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError writes an error response.
func WriteError(w http.ResponseWriter, status int, err error) {
	WriteJSON(w, status, RemoteError{Status: status, Msg: err.Error()})
}

func post(ctx context.Context, c *http.Client, url string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return DecodeResponse(resp, out)
}

// DecodeResponse reads a lab API response into out, or its error.
func DecodeResponse(resp *http.Response, out any) error {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		e := &RemoteError{Status: resp.StatusCode}
		if json.Unmarshal(data, e) != nil || e.Msg == "" {
			e.Msg = fmt.Sprintf("%s: %s", resp.Status, bytes.TrimSpace(data))
		}
		e.Status = resp.StatusCode
		return e
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
