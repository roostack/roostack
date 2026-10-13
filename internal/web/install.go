package web

import (
	"fmt"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/node"
)

// Installing a service, step by step (SPEC.md §6.3.3): the service's page,
// the Services list and Home show node.InstallProgress while it lasts.

// longStep is how long a step runs before the page says it's taking longer
// than usual (and whether it's still moving).
const longStep = 5 * time.Minute

func init() {
	templateFuncs["took"] = tookText
	templateFuncs["add1"] = func(i int) int { return i + 1 }
	templateFuncs["lowerFirst"] = func(s string) string {
		if s == "" {
			return s
		}
		return strings.ToLower(s[:1]) + s[1:]
	}
	// slowStep: the install's current step has run for longer than usual.
	templateFuncs["slowStep"] = func(p *node.InstallProgress) bool {
		s := p.Step()
		return !s.Started.IsZero() && time.Since(s.Started) > longStep
	}
}

// tookText is how long something took, or has taken so far (to is zero):
// "14 s", "3 min", "1 h 5 min".
func tookText(from, to time.Time) string {
	if from.IsZero() {
		return ""
	}
	if to.IsZero() {
		to = time.Now()
	}
	d := to.Sub(from).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}
