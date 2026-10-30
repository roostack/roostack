package podman

import (
	"context"
	"strings"
)

// Networks manages the roostack user's Podman networks. Quadlet makes
// them; removing a .network file and stopping its unit leaves the network
// itself behind (docs/ASSUMPTIONS.md C9), so Roostack removes it.
type Networks struct {
	// Run runs podman with the given stdin; tests replace it.
	Run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

// NewNetworks returns a Networks that runs podman through the roostack
// user's systemd manager, as NewSecrets does.
func NewNetworks() *Networks { return &Networks{Run: run} }

// Remove deletes a network no container uses. A missing network is not an
// error; one a container still uses is (no --force, which would remove the
// container too).
func (n *Networks) Remove(ctx context.Context, name string) error {
	_, err := n.Run(ctx, nil, "network", "rm", name)
	if err != nil && strings.Contains(err.Error(), "network not found") {
		return nil
	}
	return err
}
