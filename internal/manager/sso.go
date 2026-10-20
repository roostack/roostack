package manager

import (
	"context"
	"slices"
	"time"

	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/sso"
	"roostack.cloud/roostack/internal/state"
)

// Single sign-on (SPEC.md §5.2): turning it on for a service, and the
// services the sign-in page knows.

// SSOAvailable says whether a service can have single sign-on, and if not,
// why, in a sentence for its page ("" if it can).
func (m *Manager) SSOAvailable(name string) (bool, string) {
	p, in, err := m.installedService(name)
	if err != nil {
		return false, ""
	}
	switch {
	case p.SSO == nil:
		return false, ""
	case !m.Lab.HasAddress():
		return false, "Single sign-on needs your lab's address first: the sign-in page is at login.<your address>."
	case in.Host != "" && in.Host != m.Lab.NodeID:
		return false, "Single sign-on works for services on this server (the lab's main one) for now. Move " + p.Title + " here to use it."
	}
	return true, ""
}

// SetSSO turns single sign-on on or off for a service, then applies the
// change: the app's settings and secret, or its wall, and the sign-in
// page (SPEC.md §5.2).
func (m *Manager) SetSSO(ctx context.Context, name string, on bool) error {
	if on {
		if ok, why := m.SSOAvailable(name); !ok {
			if why == "" {
				return formErrorf("This service can't use single sign-on: its package doesn't say how.")
			}
			return formErrorf("%s", why)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Name() == name })
	if i < 0 {
		return ErrNotFound
	}
	in := services[i].Instance
	if in.SSO == on {
		return nil
	}
	in.SSO = on
	if err := in.Save(state.InstanceFile(m.Lab, name)); err != nil {
		return err
	}
	m.ssoMu.Lock()
	m.ssoClients = nil // read again
	m.ssoMu.Unlock()
	if on {
		m.applyReason = "Turning on single sign-on for " + name
	} else {
		m.applyReason = "Turning off single sign-on for " + name
	}
	m.startApplyLocked()
	return nil
}

// ssoCache is how long the services the sign-in page knows are kept: it
// asks on every request to a walled service.
const ssoCache = 5 * time.Second

// SSOClients are the services with single sign-on on, for the sign-in
// page: an OIDC app's exact redirect addresses, or a walled service's
// address.
func (m *Manager) SSOClients() []sso.Client {
	m.ssoMu.Lock()
	defer m.ssoMu.Unlock()
	if m.ssoClients != nil && time.Since(m.ssoAt) < ssoCache {
		return m.ssoClients
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		m.Log.Error("listing services for single sign-on", "err", err)
		return nil
	}
	clients := []sso.Client{}
	for _, s := range services {
		p, in := s.Package, s.Instance
		if !generate.SSOOn(m.Lab, p, in) || in.Waiting || (in.Host != "" && in.Host != m.Lab.NodeID) {
			continue
		}
		route := p.SSORoute()
		r := p.Routes[route]
		if r == nil {
			continue
		}
		sub := r.Subdomain
		if o, ok := in.Routes[route]; ok && o.Subdomain != "" {
			sub = o.Subdomain
		}
		exposure := r.Exposure
		if o, ok := in.Routes[route]; ok && o.Exposure != "" {
			exposure = o.Exposure
		}
		host := sub + "." + m.Lab.Domain
		c := sso.Client{ID: in.ID(), Title: titleOf(p, in.Title), Public: exposure == pkgspec.ExposurePublic}
		if p.SSO.OIDC != nil {
			c.RedirectURIs = p.SSO.OIDC.SSORedirects("https://" + host)
		} else {
			c.Host = host
		}
		clients = append(clients, c)
	}
	m.ssoClients, m.ssoAt = clients, time.Now()
	return clients
}

func titleOf(p *pkgspec.Package, title string) string {
	if title != "" {
		return title
	}
	return p.Title
}
