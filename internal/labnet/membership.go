package labnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"

	"roostack.cloud/roostack/internal/agent"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/nodeid"
)

// Network defaults. The controller is the hub other servers dial (SPEC.md
// §10.2: hub and spoke).
var (
	DefaultPrefix  = netip.MustParsePrefix("100.96.0.0/24")
	ControllerAddr = netip.MustParseAddr("100.96.0.1")
)

// Membership is this server's place in a lab with a network. A server
// without one is a lab of its own (the controller, with no network yet).
type Membership struct {
	Role string     `json:"role"` // config.RoleController or config.RoleNode
	Addr netip.Addr `json:"addr"` // this server on Roostack's network
	// PrefixBits is the network's prefix length.
	PrefixBits int `json:"prefix_bits"`
	// ControllerID and ControllerAddr name the controller (for nodes).
	ControllerID   string     `json:"controller_id,omitempty"`
	ControllerAddr netip.Addr `json:"controller_addr,omitzero"`
}

// MembershipFile is where a server keeps its membership.
func MembershipFile(stateDir string) string {
	return filepath.Join(IdentityDir(stateDir), "membership.json")
}

// LoadMembership reads the membership; ok is false if the server has none.
func LoadMembership(stateDir string) (m Membership, ok bool, err error) {
	data, err := os.ReadFile(MembershipFile(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, false, fmt.Errorf("%s: %w", MembershipFile(stateDir), err)
	}
	if m.Role != config.RoleController && m.Role != config.RoleNode {
		return m, false, fmt.Errorf("%s: unknown role %q", MembershipFile(stateDir), m.Role)
	}
	if m.Role == config.RoleNode && !nodeid.ValidID(m.ControllerID) {
		return m, false, fmt.Errorf("%s: no controller", MembershipFile(stateDir))
	}
	return m, true, nil
}

// Save writes the membership.
func (m Membership) Save(stateDir string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(IdentityDir(stateDir), 0o700); err != nil {
		return err
	}
	file := MembershipFile(stateDir)
	if err := os.WriteFile(file+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(file+".tmp", file)
}

// Apply sets the lab's runtime role and address from the membership.
func (m Membership) Apply(lab *config.Lab) {
	lab.Role, lab.OverlayAddr = m.Role, m.Addr.String()
}

// ControllerURL is the lab API on the controller.
func (m Membership) ControllerURL() string {
	return fmt.Sprintf("https://%s:%d", m.ControllerAddr, Port)
}

// Leave takes this server out of its lab: Roostack's WireGuard interface
// goes, and so do its certificate, membership and saved assignment. It is
// then a lab of its own again.
func Leave(ctx context.Context, stateDir string, ag interface {
	RemoveWireGuard(context.Context, string) error
}) error {
	if err := ag.RemoveWireGuard(ctx, agent.WireGuardInterface); err != nil {
		return err
	}
	return os.RemoveAll(IdentityDir(stateDir))
}
