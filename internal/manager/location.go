package manager

import (
	"context"
	"fmt"
)

// Where each server is, for the 3-2-1 checks and direct links (SPEC.md
// §6.9, §10.10): at home, or somewhere else (a VPS, a relative's house).
// Roostack can't tell, so the Owner says, once per server. Kept in the
// controller's settings (LocationKey).

// ServerLocation is where a server is: LocationHome, LocationAway, or ""
// if nobody has said.
func (m *Manager) ServerLocation(ctx context.Context, id string) string {
	v := m.setting(ctx, LocationKey+id)
	if v != LocationHome && v != LocationAway {
		return ""
	}
	return v
}

// SetServerLocation records where a server is (LocationHome or
// LocationAway). Where a server is decides how its direct links reach it,
// so the lab's servers are given their links again.
func (m *Manager) SetServerLocation(ctx context.Context, id, loc string) error {
	if loc != LocationHome && loc != LocationAway {
		return fmt.Errorf("unknown location %q", loc)
	}
	if m.Settings == nil {
		return fmt.Errorf("no settings to keep a server's location in")
	}
	if err := m.Settings.SetSetting(ctx, LocationKey+id, loc); err != nil {
		return err
	}
	m.ApplyBecause("A server's location changed")
	return nil
}

// ServerPlace says whether a backup on server host is at home or away,
// seen from server from: at home if both are at home; away if they're in
// different places or both away from home. Unknown lists the servers
// (from, host, or both) whose location is needed and nobody has said;
// then it's neither.
func (m *Manager) ServerPlace(ctx context.Context, from, host string) (atHome, away bool, unknown []string) {
	here, there := m.ServerLocation(ctx, from), m.ServerLocation(ctx, host)
	if here == "" {
		unknown = append(unknown, from)
	}
	if there == "" {
		unknown = append(unknown, host)
	}
	switch {
	case unknown != nil:
		return false, false, unknown
	case here == LocationHome && there == LocationHome:
		return true, false, nil
	}
	return false, true, nil
}
