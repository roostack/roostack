package public

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Attacks is what CrowdSec saw on the public addresses over a while
// (SPEC.md §6.11, §6.13: the weekly summary).
type Attacks struct {
	// Alerts is how many attacks it detected, from Addresses different
	// addresses; Blocked how many of those it blocked.
	Alerts, Addresses, Blocked int
	// Kinds are the commonest kinds of attack, most first ("http-probing").
	Kinds []string
}

// Launch runs a command as the roostack user (in its systemd session),
// like cmd/roostack's userLaunch.
type Launch func(ctx context.Context, env []string, name string, args ...string) *exec.Cmd

// CrowdSecAttacks asks CrowdSec (`cscli alerts list` in its container)
// what it saw since since ago: its scenarios' alerts, not each request
// AppSec refused (CrowdSecBlocks), though a second refused request bans
// the address, which is one of these.
func CrowdSecAttacks(ctx context.Context, launch Launch, container string, since time.Duration) (Attacks, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := launch(ctx, nil, "podman", "exec", container, "cscli", "alerts", "list",
		"--since", fmt.Sprintf("%dh", int(since.Hours())), "--limit", "0", "-o", "json").Output()
	if err != nil {
		return Attacks{}, fmt.Errorf("asking CrowdSec: %w", err)
	}
	return parseAlerts(out)
}

// parseAlerts sums cscli's alerts (JSON).
func parseAlerts(out []byte) (Attacks, error) {
	var alerts []struct {
		Kind     string `json:"kind"`
		Scenario string `json:"scenario"`
		Source   struct {
			Value string `json:"value"`
			IP    string `json:"ip"`
		} `json:"source"`
		Decisions []json.RawMessage `json:"decisions"`
	}
	if err := json.Unmarshal(out, &alerts); err != nil {
		return Attacks{}, fmt.Errorf("reading CrowdSec's alerts: %w", err)
	}
	var a Attacks
	addrs, kinds := map[string]bool{}, map[string]int{}
	for _, al := range alerts {
		if al.Kind == "waf" {
			continue // a request AppSec refused (CrowdSecBlocks)
		}
		a.Alerts++
		addrs[cmpOr(al.Source.IP, al.Source.Value)] = true
		if len(al.Decisions) > 0 {
			a.Blocked++
		}
		kind := al.Scenario
		if _, k, ok := strings.Cut(kind, "/"); ok {
			kind = k // crowdsecurity/http-probing → http-probing
		}
		kinds[kind]++
	}
	a.Addresses = len(addrs)
	for k := range kinds {
		a.Kinds = append(a.Kinds, k)
	}
	slices.SortFunc(a.Kinds, func(x, y string) int {
		if kinds[x] != kinds[y] {
			return kinds[y] - kinds[x]
		}
		return strings.Compare(x, y)
	})
	if len(a.Kinds) > 3 {
		a.Kinds = a.Kinds[:3]
	}
	return a, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// CrowdSecBans are the addresses and ranges CrowdSec bans now, newest
// first: its own decisions and those made by hand (`cscli`), not the
// community's lists (Roostack Cloud's relay refuses them at its edges for
// this lab only: its DESIGN §5, "Collective defence").
func CrowdSecBans(ctx context.Context, launch Launch, container string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := launch(ctx, nil, "podman", "exec", container, "cscli", "decisions", "list", "--limit", "0", "-o", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("asking CrowdSec: %w", err)
	}
	return parseBans(out)
}

// decisionAlert is one of cscli's alerts, with its decisions.
type decisionAlert struct {
	CreatedAt time.Time `json:"created_at"`
	Decisions []struct {
		Origin    string `json:"origin"`
		Scope     string `json:"scope"`
		Type      string `json:"type"`
		Value     string `json:"value"`
		Simulated bool   `json:"simulated"`
	} `json:"decisions"`
}

// parseBans reads cscli's decisions (JSON: alerts, each with its
// decisions; null when there are none).
func parseBans(out []byte) ([]string, error) {
	var alerts []decisionAlert
	if err := json.Unmarshal(out, &alerts); err != nil {
		return nil, fmt.Errorf("reading CrowdSec's decisions: %w", err)
	}
	slices.SortStableFunc(alerts, func(a, b decisionAlert) int { return b.CreatedAt.Compare(a.CreatedAt) })
	var bans []string
	seen := map[string]bool{}
	for _, al := range alerts {
		for _, d := range al.Decisions {
			own := d.Origin == "crowdsec" || d.Origin == "cscli"
			scope := strings.ToLower(d.Scope)
			if !own || d.Simulated || d.Type != "ban" || (scope != "ip" && scope != "range") || seen[d.Value] {
				continue
			}
			seen[d.Value] = true
			bans = append(bans, d.Value)
		}
	}
	return bans, nil
}
