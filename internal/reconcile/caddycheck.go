package reconcile

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strings"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/podman"
)

// CaddyValidate is CheckCaddy for the running Caddy container: `caddy
// validate` on the staged Caddyfile, as Caddy sees its folder
// (/etc/caddy). Caddy exits 1 when it refuses the configuration; anything
// else (the container isn't running, podman failed) means it couldn't be
// asked.
func CaddyValidate(run podman.RunFunc) func(ctx context.Context, rel string) error {
	return func(ctx context.Context, rel string) error {
		_, stderr, err := podman.Exec(ctx, run, generate.CaddyUnit, nil,
			"caddy", "validate", "--adapter", "caddyfile", "--config", "/etc/caddy/"+rel)
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return &CaddyRefused{Reason: caddyReason(string(stderr))}
		}
		return err
	}
}

// staging is the staged folder in a path Caddy names, which the reason
// leaves out: it names the file as it will be.
var staging = regexp.MustCompile(regexp.QuoteMeta(caddyCheckDir) + `[0-9]+/`)

// caddyReason is the reason in Caddy's output: its last "Error:" line,
// or its last line.
func caddyReason(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	reason := strings.TrimSpace(lines[len(lines)-1])
	for i := len(lines) - 1; i >= 0; i-- {
		if r, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "Error: "); ok {
			reason = r
			break
		}
	}
	return staging.ReplaceAllString(reason, "")
}
