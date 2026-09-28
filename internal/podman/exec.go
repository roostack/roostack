package podman

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ErrBadEnvValue means an environment value can't be passed to a container
// (it contains a newline or NUL).
var ErrBadEnvValue = errors.New("value contains a line break")

// Exec runs a command in a running container, like `podman exec`, and
// returns its combined output.
//
// env is passed as an env file on stdin, so values (passwords) never
// appear in a command line or unit property.
func Exec(ctx context.Context, run func(ctx context.Context, stdin []byte, args ...string) ([]byte, error),
	container string, env map[string]string, command ...string) ([]byte, error) {
	var envFile bytes.Buffer
	for _, k := range slices.Sorted(maps.Keys(env)) {
		if strings.ContainsAny(env[k], "\n\r\x00") {
			return nil, fmt.Errorf("%s: %w", k, ErrBadEnvValue)
		}
		fmt.Fprintf(&envFile, "%s=%s\n", k, env[k])
	}
	args := []string{"exec", "--env-file", "/dev/stdin", container}
	return run(ctx, envFile.Bytes(), append(args, command...)...)
}

// Run runs podman through the roostack user's systemd manager, with stdin.
// Errors include podman's stderr, never stdin.
func Run(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	return run(ctx, stdin, args...)
}
