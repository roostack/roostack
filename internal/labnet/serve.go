package labnet

import (
	"bytes"
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

	"github.com/mylab/mylab/internal/agent"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/nodeid"
)

// Serve runs the lab API on this machine's address on MyLab's network
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

// Caller is the verified machine making a lab API request, or "".
func Caller(r *http.Request) string { return PeerNode(r.TLS) }

// JoinHandler serves POST /v1/join: a machine presenting a join code.
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

// JoinLab joins this machine to a lab with a code (`mylab node join`):
// WireGuard comes up with the code's one-time key, the controller signs
// this machine's certificate and learns its own key, then WireGuard
// switches to that key.
func JoinLab(ctx context.Context, code JoinCode, self nodeid.Identity, stateDir string, ag WireGuardAgent, log func(string, ...any)) error {
	if m, ok, _ := LoadMembership(stateDir); ok {
		return fmt.Errorf("this machine is already a %s in a lab", m.Role)
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
	// The first WireGuard handshake can take a few seconds.
	deadline := time.Now().Add(45 * time.Second)
	for {
		err = post(ctx, client, url, body, &resp)
		var refused *RemoteError
		if err == nil || errors.As(err, &refused) || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	var refused *RemoteError
	if err != nil && !errors.As(err, &refused) {
		_, port, _ := net.SplitHostPort(code.Endpoint)
		return fmt.Errorf("couldn't reach the controller at %s over WireGuard (%v).\n"+
			"Check the address, and if the controller is on another network, that UDP port %s is forwarded to it on "+
			"that network's router (Settings → Machines explains how)", code.Endpoint, err, port)
	}
	if err != nil {
		return fmt.Errorf("the controller refused to add this machine: %w", err)
	}
	if err := id.Save(resp.Cert, resp.CA); err != nil {
		return err
	}
	log("Joined. Switching to this machine's own WireGuard key")
	wg.JoinKey = ownKey
	if _, err := ag.ApplyWireGuard(ctx, wg); err != nil {
		return err
	}
	return Membership{Role: config.RoleNode, Addr: code.Addr, PrefixBits: code.PrefixBits,
		ControllerID: code.ControllerID, ControllerAddr: code.ControllerAddr}.Save(stateDir)
}

// RemoteError is an error reported by the other machine.
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
