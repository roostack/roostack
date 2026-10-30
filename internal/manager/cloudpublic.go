package manager

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
	"roostack.cloud/roostack/internal/state"
)

// Roostack Cloud's names and relay (SPEC.md §6.11): a free roost.ac name
// carries only services from the registry's packages, and a package the
// registry flags as needing approval goes public through a roost.ac name
// or Cloud's relay only for an account Cloud has approved. The lab tells
// Cloud what serves each public address that comes through Cloud
// (CloudPublicHosts), and keeps private what Cloud says it won't carry,
// and what it may refuse until it has said. The Owner's own domain without
// the relay is never limited: it's their lab.

// CloudPublic is Roostack Cloud's say on the lab's public addresses, from
// the cloud package (nil funcs: no Cloud, and nothing is limited).
type CloudPublic struct {
	// Send tells Cloud what serves the lab's public addresses, if that's
	// changed since it last answered (CloudPublicHosts).
	Send func(ctx context.Context) error
	// Verdict is how Cloud stands on one of them: refused is why it won't
	// carry it (nil: it will); known says Cloud has answered for it as it
	// is (cloud.Service.PublicVerdict).
	Verdict func(h cloudapi.PublicHost) (refused *cloudapi.NameStatus, known bool)
}

// cloudHost is a public address as Cloud is told it, and the service it's
// one of ("" for Roostack's own pages).
type cloudHost struct {
	cloudapi.PublicHost
	service string
}

// throughCloud says whether the lab's public addresses come through
// Roostack Cloud: its address is a roost.ac name, or the relay is on.
func throughCloud(lab config.Lab) bool { return lab.RoostAc() || lab.Public.Relay }

// cloudHostsOf are the lab's public addresses that come through Roostack
// Cloud, with what serves each, whether Cloud carries them or not: as
// publicHostsOf, but every address the Owner made public (old ones too),
// whatever Cloud said. None when nothing comes through Cloud. ix is the
// registry's index, for its flags (nil: none known).
func cloudHostsOf(lab config.Lab, services []state.Service, ix *registry.Index) []cloudHost {
	if !throughCloud(lab) || !lab.HasAddress() {
		return nil
	}
	var out []cloudHost
	add := func(host string, h cloudapi.PublicHost, service string) {
		h.Host = host
		out = append(out, cloudHost{PublicHost: h, service: service})
		if o := generate.OldHost(lab, host); o != "" {
			h.Host = o
			out = append(out, cloudHost{PublicHost: h, service: service})
		}
	}
	own := cloudapi.PublicHost{Kind: cloudapi.HostRoostack}
	if lab.Monitoring.StatusPage {
		add(caddy.StatusSubdomain+"."+lab.Domain, own, "")
	}
	for _, s := range services {
		h := cloudapi.PublicHost{Kind: cloudapi.HostOwn}
		if s.Instance == nil || !s.Instance.Local {
			// The Owner's own packages are never named.
			h = cloudapi.PublicHost{Kind: cloudapi.HostRegistry, Package: s.Package.Name}
			if ix != nil {
				h.Approval = ix.Packages[s.Package.Name].NeedsApproval() != ""
			}
		}
		_, routes := generate.Values(lab, s.Package, s.Instance)
		if rc, ok := routes[s.Package.SSORoute()]; ok && rc.Exposure == pkgspec.ExposurePublic && generate.SSOOn(lab, s.Package, s.Instance) {
			add(lab.SignInHost(), own, "")
		}
		for _, rc := range routes {
			if rc.Exposure == pkgspec.ExposurePublic {
				add(rc.Host, h, s.Name())
			}
		}
	}
	slices.SortStableFunc(out, func(a, b cloudHost) int { return strings.Compare(a.Host, b.Host) })
	return slices.CompactFunc(out, func(a, b cloudHost) bool { return a.Host == b.Host })
}

// CloudPublicHosts are the lab's public addresses that come through
// Roostack Cloud, with what serves each, for telling Cloud (the cloud
// package's Public).
func (m *Manager) CloudPublicHosts() []cloudapi.PublicHost {
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		m.Log.Error("listing public addresses for Roostack Cloud", "err", err)
		return nil
	}
	lab := m.withStatusPage()
	lab.Public.Relay = m.labNow().Public.Relay
	var out []cloudapi.PublicHost
	for _, h := range cloudHostsOf(lab, services, m.index()) {
		out = append(out, h.PublicHost)
	}
	return out
}

// index is the registry's index, or nil while there's none.
func (m *Manager) index() *registry.Index {
	if m.RegistryIndex == nil {
		return nil
	}
	ix, err := m.RegistryIndex()
	if err != nil {
		m.Log.Warn("reading the registry's index", "err", err)
		return nil
	}
	return ix
}

// cloudRefused works out which services Roostack Cloud won't carry, by ID
// (config.Public.Refused): those Cloud said it won't, and, until it's
// said, those it may refuse (one of the Owner's own packages, or one the
// registry flags), kept private meanwhile. Nil without Cloud. Callers
// hold mu.
func (m *Manager) cloudRefused(services []state.Service) map[string]string {
	if m.CloudPublic.Verdict == nil {
		return nil
	}
	lab := m.withStatusPage()
	lab.Public.Relay = m.labNow().Public.Relay
	var out map[string]string
	for _, h := range cloudHostsOf(lab, services, m.index()) {
		if h.service == "" || h.Kind == cloudapi.HostRegistry && !h.Approval {
			continue // Cloud carries Roostack's own pages and the registry's
		}
		why := ""
		switch refused, known := m.CloudPublic.Verdict(h.PublicHost); {
		case !known:
			why = config.RefusedChecking
		case refused != nil:
			why = refused.State
		default:
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		if cur := out[h.service]; cur == "" || cur == config.RefusedChecking {
			out[h.service] = why
		}
	}
	return out
}

// sendCloudPublic tells Cloud what serves the lab's public addresses, in
// the background, after an assignment (Cloud's answer applies again if it
// changes what's refused). Callers hold mu.
func (m *Manager) sendCloudPublic() {
	if m.CloudPublic.Send == nil {
		return
	}
	m.relayWG.Add(1)
	go func() {
		defer m.relayWG.Done()
		if err := m.CloudPublic.Send(context.Background()); err != nil {
			m.Log.Warn("telling Roostack Cloud what serves the lab's public addresses (told again at its next check-in)", "err", err)
		}
	}()
}

// CloudRefusal is how Roostack Cloud limits a service's public access
// (SPEC.md §6.11), for its pages: Own says its package is one of the
// Owner's own, and Registry is the registry's reason for one it flags.
// State is why it's kept private now (a cloudapi name state,
// NameOwnPackage or NameNeedsApproval, or config.RefusedChecking), "" when
// it isn't; Hosts are its public addresses kept private, and Route the
// first of their routes.
type CloudRefusal struct {
	Name, Title string
	Own         bool
	Registry    string
	State       string
	Hosts       []string
	Route       string
}

// CloudLimit is how Roostack Cloud limits the service's public access:
// nil when it doesn't (its addresses don't come through Cloud, or it's
// from a package of the registry's it doesn't flag), with State set when
// it's kept private now.
func (m *Manager) CloudLimit(name string) *CloudRefusal {
	for _, c := range m.cloudLimits() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// CloudRefusals are the services kept private for Roostack Cloud as the
// last assignment worked them out, by name.
func (m *Manager) CloudRefusals() []*CloudRefusal {
	var out []*CloudRefusal
	for _, c := range m.cloudLimits() {
		if c.State != "" {
			out = append(out, c)
		}
	}
	return out
}

// cloudLimits are the installed services Roostack Cloud limits, by name.
func (m *Manager) cloudLimits() []*CloudRefusal {
	if m.CloudPublic.Verdict == nil {
		return nil
	}
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		m.Log.Error("listing what Roostack Cloud limits", "err", err)
		return nil
	}
	lab := m.withStatusPage()
	lab.Public.Relay = m.labNow().Public.Relay
	if !throughCloud(lab) || !lab.HasAddress() {
		return nil
	}
	refused := lab.Public.Refused
	ix := m.index()
	var out []*CloudRefusal
	for _, s := range services {
		c := &CloudRefusal{Name: s.Name(), Title: s.Title(), Own: s.Instance != nil && s.Instance.Local, State: refused[s.Name()]}
		if !c.Own && ix != nil {
			c.Registry = ix.Packages[s.Package.Name].NeedsApproval()
		}
		// Free roost.ac names only carry the registry's packages; the
		// relay on the Owner's own domain carries their own too.
		if c.State == "" && c.Registry == "" && !(c.Own && lab.RoostAc()) {
			continue
		}
		if c.State != "" {
			_, routes := generate.Values(lab, s.Package, s.Instance)
			for _, rn := range slices.Sorted(maps.Keys(routes)) {
				if rc := routes[rn]; rc.Exposure == pkgspec.ExposurePublic {
					c.Hosts = append(c.Hosts, rc.Host)
					c.Route = cmp.Or(c.Route, rn)
				}
			}
		}
		out = append(out, c)
	}
	return out
}
