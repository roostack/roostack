package public

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// CrowdSecCommunity asks CrowdSec (`cscli decisions list -a` in its
// container) for the community's blocklist it pulled: bans from its
// central API (origin CAPI) and from blocklists added in its console
// (origin lists), as addresses and ranges ("192.0.2.7", "198.51.100.0/24").
// With sharing off there are none (SPEC.md §6.10 item 4).
func CrowdSecCommunity(ctx context.Context, launch Launch, container string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := launch(ctx, nil, "podman", "exec", container, "cscli", "decisions", "list", "-a",
		"--limit", "0", "-o", "raw").Output()
	if err != nil {
		return nil, fmt.Errorf("asking CrowdSec: %w", err)
	}
	return parseCommunity(out)
}

// parseCommunity reads cscli's decisions as CSV (`-o raw`: id, source,
// ip, reason, action, … simulated, alert_id; ip is "Ip:192.0.2.7" or
// "Range:198.51.100.0/24"), keeping the community's bans.
func parseCommunity(out []byte) ([]string, error) {
	r := csv.NewReader(bytes.NewReader(out))
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, nil // no decisions at all
	}
	if err != nil {
		return nil, fmt.Errorf("reading CrowdSec's decisions: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, need := range []string{"source", "ip", "action", "simulated"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("reading CrowdSec's decisions: no %q column in %q", need, header)
		}
	}
	var addrs []string
	seen := map[string]bool{}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading CrowdSec's decisions: %w", err)
		}
		field := func(name string) string {
			if i := col[name]; i < len(rec) {
				return rec[i]
			}
			return ""
		}
		source := strings.ToLower(field("source"))
		if source != "capi" && source != "lists" && !strings.HasPrefix(source, "lists:") {
			continue // the lab's own CrowdSec, or bans made by hand
		}
		if field("action") != "ban" || field("simulated") == "true" {
			continue
		}
		scope, value, ok := strings.Cut(field("ip"), ":")
		if !ok || seen[value] {
			continue
		}
		switch strings.ToLower(scope) {
		case "ip", "range":
			seen[value] = true
			addrs = append(addrs, value)
		}
	}
	return addrs, nil
}
