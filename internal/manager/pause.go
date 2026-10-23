package manager

import (
	"maps"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/state"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/words"
)

// PauseRequest is what to pause, or to open again (SPEC.md §6.16):
// everything public, every service built with something, or chosen
// services. Any of them may be given at once.
type PauseRequest struct {
	All       bool
	BuiltWith []string
	Services  []string
}

func (r PauseRequest) empty() bool { return !r.All && len(r.BuiltWith) == 0 && len(r.Services) == 0 }

// PublicAccess is what's public in the lab and what's paused, for the
// pages: the lab's own pauses, and every service with a public address or
// port.
type PublicAccess struct {
	All       *config.Pause
	BuiltWith map[string]config.Pause
	Services  []PublicService
}

// PublicService is a service with a public address or port.
type PublicService struct {
	Name, Title string
	// Hosts are its public addresses.
	Hosts []string
	// BuiltWith is what it's built with (pkgspec.Package.BuiltWithNames).
	BuiltWith []string
	// Own is its own pause; ByAll and ByBuiltWith are the lab's that
	// cover it.
	Own         *config.Pause
	ByAll       bool
	ByBuiltWith []string
}

// Paused says whether its public access is paused, whichever way.
func (s PublicService) Paused() bool { return s.Own != nil || s.ByAll || len(s.ByBuiltWith) > 0 }

// PausedServices are the public services whose public access is paused.
func (a PublicAccess) PausedServices() []PublicService {
	var out []PublicService
	for _, s := range a.Services {
		if s.Paused() {
			out = append(out, s)
		}
	}
	return out
}

// PublicAccess lists the lab's public services and its pauses.
func (m *Manager) PublicAccess() (PublicAccess, error) {
	lab := m.labNow()
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		return PublicAccess{}, err
	}
	pause := lab.Public.Pause
	out := PublicAccess{All: pause.All, BuiltWith: pause.BuiltWith}
	for _, s := range services {
		ps, ok := publicService(lab, s)
		if !ok {
			continue
		}
		ps.Own, ps.ByAll = s.Instance.PublicPause, pause.All != nil
		for _, b := range ps.BuiltWith {
			if _, ok := pause.BuiltWith[b]; ok {
				ps.ByBuiltWith = append(ps.ByBuiltWith, b)
			}
		}
		out.Services = append(out.Services, ps)
	}
	slices.SortFunc(out.Services, func(a, b PublicService) int { return strings.Compare(a.Title, b.Title) })
	return out, nil
}

// publicService is s as a PublicService, if any of its addresses or ports
// is public.
func publicService(lab config.Lab, s state.Service) (PublicService, bool) {
	ps := PublicService{Name: s.Name(), Title: s.Title(), BuiltWith: s.Package.BuiltWithNames()}
	_, routes := generate.Values(lab, s.Package, s.Instance)
	public := false
	for _, name := range slices.Sorted(maps.Keys(routes)) {
		if rc := routes[name]; rc.Exposure == pkgspec.ExposurePublic {
			public = true
			if lab.HasAddress() {
				ps.Hosts = append(ps.Hosts, rc.Host)
			}
		}
	}
	for _, pc := range generate.ResolvePorts(s.Package, s.Instance) {
		public = public || pc.Enabled && pc.Exposure == pkgspec.ExposurePublic
	}
	return ps, public
}

// PausePublic pauses public access (SPEC.md §6.16) for what r says, by
// the person by ("" from the command line), and applies it: visitors from
// outside will get the paused page within seconds, whichever way they come
// in, and the services' public ports will take connections from home only.
// Pausing something already paused keeps its first pause.
func (m *Manager) PausePublic(by string, r PauseRequest) error {
	if r.empty() {
		return usererr.New("Choose what to pause: everything public, what services are built with, or some services.")
	}
	for _, b := range r.BuiltWith {
		if !pkgspec.ValidBuiltWith(b) {
			return usererr.New("%q isn't a name of something services are built with, such as php or log4j.", b)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := m.publicServices(r.Services)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	p := config.Pause{Since: now, By: by}
	if r.All || len(r.BuiltWith) > 0 {
		err := m.saveLab(func(l *config.Lab) {
			pause := &l.Public.Pause
			if r.All && pause.All == nil {
				pause.All = &p
			}
			bw := maps.Clone(pause.BuiltWith)
			for _, b := range r.BuiltWith {
				if _, ok := bw[b]; !ok {
					if bw == nil {
						bw = map[string]config.Pause{}
					}
					bw[b] = p
				}
			}
			pause.BuiltWith = bw
		})
		if err != nil {
			return err
		}
	}
	for _, s := range services {
		if s.Instance.PublicPause != nil {
			continue
		}
		s.Instance.PublicPause = &p
		if err := s.Instance.Save(state.InstanceFile(m.Lab, s.Name())); err != nil {
			return err
		}
	}
	m.startApplyLocked("Pausing public access to " + pauseWhat(r, services))
	return nil
}

// LiftPause opens public access again for what r says (SPEC.md §6.16):
// the lab's pause of everything, its pauses by what services are built
// with, and services' own pauses. A service another pause still covers
// stays paused.
func (m *Manager) LiftPause(r PauseRequest) error {
	if r.empty() {
		return usererr.New("Choose what to open to the internet again.")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, err := state.LoadServices(m.Lab)
	if err != nil && len(all) == 0 {
		return err
	}
	var services []state.Service
	for _, name := range r.Services {
		i := slices.IndexFunc(all, func(s state.Service) bool { return s.Name() == name })
		if i < 0 {
			return ErrNotFound
		}
		services = append(services, all[i])
	}
	if r.All || len(r.BuiltWith) > 0 {
		err := m.saveLab(func(l *config.Lab) {
			pause := &l.Public.Pause
			if r.All {
				pause.All = nil
			}
			bw := maps.Clone(pause.BuiltWith)
			for _, b := range r.BuiltWith {
				delete(bw, b)
			}
			if len(bw) == 0 {
				bw = nil
			}
			pause.BuiltWith = bw
		})
		if err != nil {
			return err
		}
	}
	for _, s := range services {
		if s.Instance.PublicPause == nil {
			continue
		}
		s.Instance.PublicPause = nil
		if err := s.Instance.Save(state.InstanceFile(m.Lab, s.Name())); err != nil {
			return err
		}
	}
	m.startApplyLocked("Opening public access again to " + pauseWhat(r, services))
	return nil
}

// publicServices are the named services, each of which must have a
// public address or port: there's nothing to pause otherwise.
func (m *Manager) publicServices(names []string) ([]state.Service, error) {
	if len(names) == 0 {
		return nil, nil
	}
	all, err := state.LoadServices(m.Lab)
	if err != nil && len(all) == 0 {
		return nil, err
	}
	lab := m.labNow()
	var out []state.Service
	for _, name := range names {
		i := slices.IndexFunc(all, func(s state.Service) bool { return s.Name() == name })
		if i < 0 {
			return nil, ErrNotFound
		}
		if _, public := publicService(lab, all[i]); !public {
			return nil, usererr.New("%s isn't open to the internet, so there's nothing to pause.", all[i].Title())
		}
		out = append(out, all[i])
	}
	return out, nil
}

// pauseWhat says what a pause or lift is about, for the apply's reason.
func pauseWhat(r PauseRequest, services []state.Service) string {
	var parts []string
	if r.All {
		parts = append(parts, "everything")
	}
	if len(r.BuiltWith) > 0 {
		parts = append(parts, "services built with "+words.And(r.BuiltWith))
	}
	for _, s := range services {
		parts = append(parts, s.Title())
	}
	return words.And(parts)
}
