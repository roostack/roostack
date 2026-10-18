package public

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"
)

// Block is a request CrowdSec's AppSec component refused (SPEC.md §6.10
// item 4): one of its rules matched it, so Caddy answered 403 and the
// service never saw it. Only what the Owner needs to recognise it: never
// the request's body, headers or query, which can hold passwords and
// tokens.
type Block struct {
	At time.Time
	// Host is the public address it was for (vault.lab.example.com).
	Host string
	// Rule is the AppSec rule that matched (vpatch-CVE-2017-9841) and
	// What its description ("PHPUnit RCE (CVE-2017-9841)").
	Rule, What string
	// Path is the request's path, without its query.
	Path string
	// From is the visitor's address.
	From string
}

// CrowdSecBlocks asks CrowdSec (`cscli alerts list` in its container)
// which requests AppSec refused since since ago, newest first. Each
// refused request is an alert of its own, of kind "waf"; the ban that
// follows a second one is a separate alert (CrowdSecAttacks).
func CrowdSecBlocks(ctx context.Context, launch Launch, container string, since time.Duration) ([]Block, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := launch(ctx, nil, "podman", "exec", container, "cscli", "alerts", "list", "--kind", "waf",
		"--since", fmt.Sprintf("%dh", int(since.Hours())), "--limit", "0", "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("asking CrowdSec: %w", err)
	}
	return parseBlocks(out)
}

// wafAlert is one of cscli's alerts from AppSec: its events, one per rule
// that matched, carry the rule, its description, the path (with the
// query) and the public address (docs/ASSUMPTIONS.md P10).
type wafAlert struct {
	Kind     string `json:"kind"`
	Scenario string `json:"scenario"`
	StartAt  string `json:"start_at"`
	Source   struct {
		IP    string `json:"ip"`
		Value string `json:"value"`
	} `json:"source"`
	Events []struct {
		Meta []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"meta"`
	} `json:"events"`
}

// parseBlocks reads cscli's AppSec alerts (JSON; null when there are
// none), newest first.
func parseBlocks(out []byte) ([]Block, error) {
	var alerts []wafAlert
	if err := json.Unmarshal(out, &alerts); err != nil {
		return nil, fmt.Errorf("reading CrowdSec's alerts: %w", err)
	}
	var blocks []Block
	for _, al := range alerts {
		if al.Kind != "waf" {
			continue
		}
		at, _ := time.Parse(time.RFC3339, al.StartAt)
		b := Block{At: at, Rule: shortName(al.Scenario), From: cmpOr(al.Source.IP, al.Source.Value)}
		// The event of the rule the alert is named after, or the first.
		for i, ev := range al.Events {
			meta := map[string]string{}
			for _, m := range ev.Meta {
				meta[m.Key] = m.Value
			}
			if i > 0 && meta["rule_name"] != al.Scenario {
				continue
			}
			b.What, b.Host = meta["message"], hostOnly(meta["target_fqdn"])
			b.Path, _, _ = strings.Cut(meta["uri"], "?")
			if meta["rule_name"] == al.Scenario {
				break
			}
		}
		blocks = append(blocks, b)
	}
	slices.SortStableFunc(blocks, func(a, b Block) int { return b.At.Compare(a.At) })
	return blocks, nil
}

// shortName drops a Hub item's author: crowdsecurity/vpatch-env-access →
// vpatch-env-access.
func shortName(name string) string {
	if _, n, ok := strings.Cut(name, "/"); ok {
		return n
	}
	return name
}

// hostOnly drops a port from a Host header, and lowers its case.
func hostOnly(h string) string {
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(h)
}

// AppSecUp says whether CrowdSec's AppSec component listens at addr
// (127.0.0.1:8475). It sends no request: one without the bouncer's key,
// which Roostack doesn't hold, would be logged as an error each time.
// Connecting isn't enough either: pasta accepts on the host's port even
// while nothing listens in the container, then closes the connection at
// once (docs/ASSUMPTIONS.md P10). AppSec keeps it open, waiting for a
// request, so a connection still open after a moment means it's there.
func AppSecUp(ctx context.Context, addr string) bool {
	d := net.Dialer{Timeout: 3 * time.Second}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(appSecWait))
	_, err = c.Read(make([]byte, 1))
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// appSecWait is how long AppSecUp waits for a connection to be closed.
var appSecWait = 500 * time.Millisecond
