package reconcile

import "testing"

func TestCaddyReason(t *testing.T) {
	out := `{"level":"info","msg":"using config from file"}
Error: adapting config using caddyfile: /etc/caddy/.check-4127/sites/x.caddy:3: unrecognized directive: nonsense
`
	if got := caddyReason(out); got != "adapting config using caddyfile: /etc/caddy/sites/x.caddy:3: unrecognized directive: nonsense" {
		t.Errorf("reason = %q", got)
	}
	if got := caddyReason("just this\n"); got != "just this" {
		t.Errorf("no Error line: %q", got)
	}
}
