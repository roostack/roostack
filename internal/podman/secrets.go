// Package podman wraps the few podman commands MyLab needs directly.
// Everything else goes through Quadlet and systemd.
package podman

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"github.com/mylab/mylab/internal/systemd"
)

// Secrets manages Podman secrets for the mylab user.
//
// Note: Podman's default "file" driver stores values unencrypted, readable
// only by the mylab user (docs/ASSUMPTIONS.md E2).
type Secrets struct {
	// Run runs podman with the given stdin; tests replace it.
	Run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error)
}

// NewSecrets returns a Secrets that runs podman through the mylab user's
// systemd manager (systemd-run --user), so it works from mylab's sandbox
// (SPEC.md §6.1).
func NewSecrets() *Secrets { return &Secrets{Run: run} }

var seq atomic.Int64

func run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	unit := fmt.Sprintf("mylab-podman-%d-%d", os.Getpid(), seq.Add(1))
	cmd := systemd.UserCommand(ctx, unit, nil, "podman", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Never include stdin (the secret value) in errors.
		return out, fmt.Errorf("podman %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// List returns the names of existing secrets.
func (s *Secrets) List(ctx context.Context) (map[string]bool, error) {
	out, err := s.Run(ctx, nil, "secret", "ls", "--noheading", "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names[line] = true
		}
	}
	return names, nil
}

// Create stores a new secret. The value is passed on stdin so it never
// appears in process listings.
func (s *Secrets) Create(ctx context.Context, name string, value []byte) error {
	_, err := s.Run(ctx, value, "secret", "create", name, "-")
	return err
}

// Replace creates or overwrites a secret (e.g. a new DNS token).
func (s *Secrets) Replace(ctx context.Context, name string, value []byte) error {
	_, err := s.Run(ctx, value, "secret", "create", "--replace", name, "-")
	return err
}
