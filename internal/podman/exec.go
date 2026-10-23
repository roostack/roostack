package podman

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/systemd"
)

// ErrBadEnvValue means an environment value can't be passed to a container
// (it contains a newline or NUL).
var ErrBadEnvValue = errors.New("value contains a line break")

// RunFunc runs podman with stdin, returning its standard output and
// standard error apart.
type RunFunc func(ctx context.Context, stdin []byte, args ...string) (stdout, stderr []byte, err error)

// Exec runs a command in a running container, like `podman exec`, and
// returns its standard output and standard error apart: a hook that hands
// Roostack a key prints only the key on standard output (SPEC.md §5.2).
//
// env is passed as an env file on stdin, so values (passwords) never
// appear in a command line or unit property.
func Exec(ctx context.Context, run RunFunc, container string, env map[string]string, command ...string) (stdout, stderr []byte, err error) {
	var envFile bytes.Buffer
	for _, k := range slices.Sorted(maps.Keys(env)) {
		if strings.ContainsAny(env[k], "\n\r\x00") {
			return nil, nil, fmt.Errorf("%s: %w", k, ErrBadEnvValue)
		}
		fmt.Fprintf(&envFile, "%s=%s\n", k, env[k])
	}
	args := []string{"exec", "--env-file", "/dev/stdin", container}
	return run(ctx, envFile.Bytes(), append(args, command...)...)
}

// RunSplit runs podman through the roostack user's systemd manager, with
// stdin, keeping its standard error apart from its output. Errors never
// include stdin.
func RunSplit(ctx context.Context, stdin []byte, args ...string) (stdout, stderr []byte, err error) {
	cmd := systemd.UserCommand(ctx, systemd.TransientUnit("podman", args...), nil, "podman", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		err = fmt.Errorf("podman %s: %w", args[0], err)
	}
	return out, errBuf.Bytes(), err
}
