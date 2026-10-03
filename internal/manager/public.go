package manager

import (
	"context"
	"errors"
	"maps"
	"slices"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
)

// Making services public (SPEC.md §6.11). The Owner makes one of a
// service's addresses public; packages mark what isn't safe, which the
// Owner can still open after a warning.

// RouteAccess is who can reach one of a service's addresses.
type RouteAccess struct {
	Route  string // the package's route name
	Host   string // e.g. photos.lab.example.com
	Public bool
	// Unsafe is the package's reason the whole address shouldn't be
	// public, if it gave one.
	Unsafe string
	// PrivatePaths stay home-only when it's public, unless opened.
	PrivatePaths []PathAccess
}

// PathAccess is a path the package keeps home-only on a public address.
type PathAccess struct {
	Path   string
	Reason string
	// Open: the Owner made it public anyway.
	Open bool
}

// Access lists who can reach each of a service's addresses.
func (m *Manager) Access(name string) ([]RouteAccess, error) {
	p, in, err := m.installedService(name)
	if err != nil {
		return nil, err
	}
	_, routes := generate.Values(m.Lab, p, in)
	var out []RouteAccess
	for _, rn := range slices.Sorted(maps.Keys(p.Routes)) {
		r, rc := p.Routes[rn], routes[rn]
		ra := RouteAccess{Route: rn, Host: rc.Host, Public: rc.Exposure == pkgspec.ExposurePublic, Unsafe: r.Public.Unsafe}
		for _, pp := range r.Public.PrivatePaths {
			ra.PrivatePaths = append(ra.PrivatePaths, PathAccess{Path: pp.Path, Reason: pp.Reason,
				Open: slices.Contains(in.Routes[rn].PublicPaths, pp.Path)})
		}
		out = append(out, ra)
	}
	return out, nil
}

// PublicRequest makes one of a service's addresses public or private.
type PublicRequest struct {
	Route  string
	Public bool
	// AcceptUnsafe confirms the Owner read why the package says the
	// address isn't safe to make public.
	AcceptUnsafe bool
	// OpenPaths are private paths to make public too (each after its
	// warning); the rest stay home-only.
	OpenPaths []string
}

// SetPublic changes who can reach an address, then applies the change:
// making something public starts CrowdSec, and its record is set once
// CrowdSec is up (internal/dnsrecords).
func (m *Manager) SetPublic(ctx context.Context, name string, req PublicRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := state.LoadServices(m.Lab)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(services, func(s state.Service) bool { return s.Package.Name == name })
	if i < 0 {
		return ErrNotFound
	}
	p, in := services[i].Package, services[i].Instance
	r := p.Routes[req.Route]
	if r == nil {
		return ErrNotFound
	}
	if req.Public && r.Public.Unsafe != "" && !req.AcceptUnsafe {
		return formErrorf("Tick the box to confirm you understand why this address isn't safe to make public.")
	}
	if in.Routes == nil {
		in.Routes = map[string]instance.RouteOverride{}
	}
	o := in.Routes[req.Route]
	o.Exposure, o.PublicPaths = pkgspec.ExposurePrivate, nil
	if req.Public {
		o.Exposure = pkgspec.ExposurePublic
		for _, pp := range r.Public.PrivatePaths {
			if slices.Contains(req.OpenPaths, pp.Path) {
				o.PublicPaths = append(o.PublicPaths, pp.Path)
			}
		}
	}
	in.Routes[req.Route] = o
	if err := in.Validate(p); err != nil {
		return formErrorf("%s", err)
	}
	if err := in.Save(state.InstanceFile(m.Lab, name)); err != nil {
		return err
	}
	m.applyReason = "Changing who can reach " + name
	m.startApplyLocked()
	return nil
}

// PublicHosts lists the lab's public addresses, for their DNS records.
func (m *Manager) PublicHosts() []string {
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		m.Log.Error("listing public addresses", "err", err)
		return nil
	}
	var hosts []string
	for _, s := range services {
		_, routes := generate.Values(m.Lab, s.Package, s.Instance)
		for _, rc := range routes {
			if rc.Exposure == pkgspec.ExposurePublic {
				hosts = append(hosts, rc.Host)
			}
		}
	}
	slices.Sort(hosts)
	return hosts
}

// SignalSharing says whether the Owner has been asked about CrowdSec
// sharing attack reports, and the answer (SPEC.md §6.10).
func (m *Manager) SignalSharing() (asked, share bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.publicLocked()
	return p.SharingAsked, !p.NoSharing
}

// publicLocked returns the public access settings; m.mu is held.
func (m *Manager) publicLocked() *config.Public {
	if m.public == nil {
		p := m.Lab.Public
		m.public = &p
	}
	return m.public
}

// SetSignalSharing saves the Owner's answer and applies it (CrowdSec
// restarts with or without its online API while anything is public).
func (m *Manager) SetSignalSharing(share bool) error {
	if m.UpdateLab == nil {
		return errors.New("manager: UpdateLab is not set")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.UpdateLab(func(l *config.Lab) { l.Public.SharingAsked, l.Public.NoSharing = true, !share }); err != nil {
		return err
	}
	p := m.publicLocked()
	p.SharingAsked, p.NoSharing = true, !share
	m.applyReason = "Changing whether CrowdSec shares attack reports"
	m.startApplyLocked()
	return nil
}
