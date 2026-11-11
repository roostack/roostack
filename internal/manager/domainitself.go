package manager

import (
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/usererr"
)

// DomainItself is what the bare domain shows, as last chosen (SPEC.md
// §5.3).
func (m *Manager) DomainItself() config.DomainItself { return m.labNow().DomainItself }

// SetDomainItself saves what the bare domain shows: lab.yaml keeps it, and
// the apply serves it (Caddy) and makes it public or home-only (DNS
// records, edges, Roostack Cloud's relay).
func (m *Manager) SetDomainItself(d config.DomainItself) error {
	if !m.Lab.HasAddress() {
		return usererr.New("Your lab needs an address first.")
	}
	if err := d.Validate(); err != nil {
		return usererr.New("%s", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLab(func(l *config.Lab) { l.DomainItself = d }); err != nil {
		return err
	}
	m.startApplyLocked("Changing what " + m.Lab.Domain + " shows")
	return nil
}
