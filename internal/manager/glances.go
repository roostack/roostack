package manager

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/usererr"
)

// A server's live view (SPEC.md §6.15.1): Glances' official image, run on
// the server while an Owner or Admin has it on, served by the
// controller's Caddy at an address of its own behind single sign-on for
// Owners and Admins only, so its own page never runs on Roostack's
// address.

// GlancesOn reports whether a server's live view is on.
func (m *Manager) GlancesOn(server string) bool {
	return slices.Contains(m.labNow().Monitoring.Glances, server)
}

// SetGlances turns a server's live view on or off, and applies it.
func (m *Manager) SetGlances(server string, on bool) error {
	if !m.Lab.HasAddress() {
		return usererr.New("A live view needs your lab to have an address first: it opens at an address of its own.")
	}
	if m.labServerName(server) == "" {
		return usererr.New("That server isn't in the lab.")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.saveLab(func(l *config.Lab) {
		l.Monitoring.Glances = slices.DeleteFunc(l.Monitoring.Glances, func(id string) bool { return id == server })
		if on {
			l.Monitoring.Glances = append(l.Monitoring.Glances, server)
		}
	}); err != nil {
		return err
	}
	m.ssoMu.Lock()
	m.ssoClients = nil // the live view's client comes or goes
	m.ssoMu.Unlock()
	m.startApplyLocked(map[bool]string{true: "Turning on the live view of " + m.labServerName(server),
		false: "Turning off the live view of " + m.labServerName(server)}[on])
	return nil
}

// GlancesURL is a server's live view's address ("" while it's off).
func (m *Manager) GlancesURL(server string) string {
	if !m.GlancesOn(server) || !m.Lab.HasAddress() {
		return ""
	}
	return "https://" + glancesHost(m.labServerName(server), m.Lab.Domain)
}

// labServerName is a server's name, by ID ("" if it isn't in the lab).
func (m *Manager) labServerName(id string) string {
	if id == m.Lab.NodeID {
		return m.Node.Identity().Name
	}
	for _, r := range m.others() {
		if r.Identity().ID == id {
			return r.Identity().Name
		}
	}
	return ""
}

// glancesHost is a server's live view's address: glances-<server>.<domain>,
// the name made a DNS label.
func glancesHost(name, domain string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z' || c >= '0' && c <= '9':
			b.WriteRune(c)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	label := strings.Trim(b.String(), "-")
	if len(label) > 50 {
		label = strings.TrimRight(label[:50], "-")
	}
	return "glances-" + label + "." + domain
}

// glancesClient is a live view's single sign-on client.
func glancesClient(server string) string { return "roostack-glances-" + server }

// glancesSites are the live views that are on, for the controller's Caddy,
// and where each server's Glances listens, by server ID.
func (m *Manager) glancesSites() ([]config.GlancesSite, map[string]string) {
	if !m.Lab.HasAddress() {
		return nil, nil
	}
	var sites []config.GlancesSite
	binds := map[string]string{}
	for _, id := range m.labNow().Monitoring.Glances {
		name := m.labServerName(id)
		if name == "" {
			continue // a server that left the lab
		}
		bind, upstream := "127.0.0.1", fmt.Sprintf("%s:%d", generate.CaddyHostLoopback, generate.GlancesPort)
		if id != m.Lab.NodeID {
			bind = m.addressOf(id)
			upstream = bind + ":" + strconv.Itoa(generate.GlancesPort)
		}
		if bind == "" {
			continue
		}
		binds[id] = bind
		sites = append(sites, config.GlancesSite{Server: id, Host: glancesHost(name, m.Lab.Domain), Upstream: upstream, Client: glancesClient(id)})
	}
	return sites, binds
}

// glancesClients are the live views' single sign-on clients, for Owners
// and Admins only.
func (m *Manager) glancesClients() []sso.Client {
	sites, _ := m.glancesSites()
	var out []sso.Client
	for _, s := range sites {
		out = append(out, sso.Client{ID: s.Client, Title: "Live view of " + m.labServerName(s.Server), Host: s.Host, AdminsOnly: true})
	}
	return out
}
