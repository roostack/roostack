package setup

import (
	"errors"
	"io/fs"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/usererr"
)

// Where the lab is: at home behind a router, or on the internet (a cloud
// server), SPEC.md §6.12. The wizard's first step asks, suggesting what
// the server's own addresses say; Settings can change it.

// NetworkChoice is the saved answer, if any, and the detected guess.
type NetworkChoice struct {
	Saved    string // config.NetworkHome, NetworkInternet, or "" if not asked yet
	Detected string
	// UITunnelOnly: Roostack's own pages aren't public on a lab on the
	// internet (config.Public.UITunnelOnly).
	UITunnelOnly bool
}

// Current is the saved answer, or the guess.
func (c NetworkChoice) Current() string {
	if c.Saved != "" {
		return c.Saved
	}
	return c.Detected
}

// Internet reports whether the lab is (or looks) on the internet.
func (c NetworkChoice) Internet() bool { return c.Current() == config.NetworkInternet }

// Network reads the saved choice from lab.yaml (a change waits for a
// restart, so the running lab may not have it yet) and detects a guess.
func (s *Setup) Network(detect func() string) NetworkChoice {
	lab := s.Lab
	// Load returns what it read even if it's incomplete (no domain yet).
	if l, err := config.Load(s.LabPath); !errors.Is(err, fs.ErrNotExist) {
		lab = l
	}
	c := NetworkChoice{Saved: lab.Network, UITunnelOnly: lab.Public.UITunnelOnly}
	if detect != nil {
		c.Detected = detect()
	}
	return c
}

// SetNetwork saves where the lab is, and whether Roostack's own pages stay
// off the internet there, then restarts Roostack (the wizard postpones
// that to its end).
func (s *Setup) SetNetwork(network string, uiTunnelOnly bool) error {
	if network != config.NetworkHome && network != config.NetworkInternet {
		return &usererr.Error{Msg: "Choose where this lab is."}
	}
	if err := config.Update(s.LabPath, func(lab *config.Lab) {
		lab.Network, lab.Public.UITunnelOnly = network, uiTunnelOnly
	}); err != nil {
		return err
	}
	if s.Restart != nil {
		s.Restart()
	}
	return nil
}
