package labnet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/nodeid"
)

// codePrefix marks a join code, so a pasted code is recognisable.
const codePrefix = "roostack-join-"

// JoinCode is everything a new server needs to join a lab (SPEC.md
// §10.2). It carries a one-time WireGuard key, so the server can reach
// the controller over WireGuard before it has registered its own key;
// the controller accepts that key only until the code is used or expires.
type JoinCode struct {
	// Endpoint is where the controller's WireGuard listens (host:port).
	// Empty with Listen: the controller can't be reached (at home, behind
	// its router), so it dials the new server instead (SPEC.md §10.2).
	Endpoint string `json:"e,omitempty"`
	// Listen: the new server listens on WireGuard's port for the
	// controller to dial it.
	Listen bool `json:"l,omitempty"`
	// ControllerKey is the controller's WireGuard public key.
	ControllerKey string `json:"k"`
	// ControllerID and ControllerAddr name the controller on the network.
	ControllerID   string     `json:"c"`
	ControllerAddr netip.Addr `json:"ca"`
	// Addr is the address the new server gets on Roostack's network.
	Addr netip.Addr `json:"a"`
	// PrefixBits is the network's prefix length (e.g. 24).
	PrefixBits int `json:"b"`
	// JoinKey is the one-time WireGuard private key.
	JoinKey string `json:"j"`
	// CA is the controller's CA fingerprint, so the new server can tell
	// it reached the real controller.
	CA string `json:"f"`
	// Token proves the code to the controller.
	Token string `json:"t"`
	// LabName and UI name the lab and where its pages are, for the new
	// server's setup page ("Open your lab"). Optional.
	LabName string `json:"n,omitempty"`
	UI      string `json:"u,omitempty"`
}

// String encodes the code for pasting into a terminal.
func (c JoinCode) String() string {
	data, _ := json.Marshal(c)
	return codePrefix + base64.RawURLEncoding.EncodeToString(data)
}

// ParseJoinCode decodes and checks a pasted join code.
func ParseJoinCode(s string) (JoinCode, error) {
	var c JoinCode
	raw, ok := strings.CutPrefix(strings.TrimSpace(s), codePrefix)
	if !ok {
		return c, errors.New("that isn't a Roostack join code (it should start with " + codePrefix + ")")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, errors.New("the join code is incomplete; copy all of it")
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("the join code is damaged: %w", err)
	}
	wg := agent.WireGuard{Address: fmt.Sprintf("%s/%d", c.Addr, c.PrefixBits), JoinKey: c.JoinKey,
		Peers: []agent.Peer{{PublicKey: c.ControllerKey, AllowedIPs: []string{fmt.Sprintf("%s/32", c.ControllerAddr)},
			Endpoint: c.Endpoint}}}
	if err := wg.Validate(); err != nil {
		return c, fmt.Errorf("the join code is damaged: %w", err)
	}
	if (c.Endpoint == "") == !c.Listen {
		return c, errors.New("the join code is damaged: it says neither where the controller is nor that it will connect")
	}
	if !nodeid.ValidID(c.ControllerID) || c.CA == "" || c.Token == "" {
		return c, errors.New("the join code is damaged")
	}
	return c, nil
}

// NewToken returns a join token and the hash the controller stores.
func NewToken() (token, hash string) {
	b := make([]byte, 16)
	rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token)
}

// HashToken is how the controller stores and looks up a join token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// JoinRequest is what a joining server sends the controller, over
// WireGuard, with TLS checked against the code's CA fingerprint.
type JoinRequest struct {
	Token  string `json:"token"`
	NodeID string `json:"node_id"`
	Name   string `json:"name"`
	// PublicKey is the server's own WireGuard key, replacing the
	// one-time key from the code.
	PublicKey string `json:"public_key"`
	// CSR is the certificate signing request (DER).
	CSR []byte `json:"csr"`
}

// JoinResponse is the server's certificate and the CA.
type JoinResponse struct {
	Cert []byte `json:"cert"` // PEM
	CA   []byte `json:"ca"`   // PEM
}
