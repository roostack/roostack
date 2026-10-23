package manager

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
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

// Check says what's wrong with a request to pause, in a sentence.
func (r PauseRequest) Check() error {
	if r.empty() {
		return usererr.New("Choose what to pause: everything public, what services are built with, or some services.")
	}
	for _, b := range r.BuiltWith {
		if !pkgspec.ValidBuiltWith(b) {
			return usererr.New("%q isn't a name of something services are built with, such as php or log4j.", b)
		}
	}
	return nil
}

// PublicPauses is what's public in the lab and what's paused, for the
// pages: the lab's own pauses, and every service with a public address or
// port.
type PublicPauses struct {
	All       *config.Pause
	BuiltWith map[string]config.Pause
	Services  []PublicService
	// Titles are built-with names' titles from the registry's list (PHP
	// for php), where it has them.
	Titles map[string]string
}

// Title is a built-with name as people write it: the registry's title,
// or the name itself when the registry's list doesn't have it.
func (a PublicPauses) Title(name string) string {
	if t := a.Titles[name]; t != "" {
		return t
	}
	return name
}

// WithTitles adds the titles of the names in a's services and pauses
// from the registry's index (nil: none yet).
func (a PublicPauses) WithTitles(ix *registry.Index) PublicPauses {
	if ix == nil {
		return a
	}
	a.Titles = map[string]string{}
	add := func(names ...string) {
		for _, n := range names {
			if t := ix.BuiltWithTitle(n); t != "" {
				a.Titles[n] = t
			}
		}
	}
	add(slices.Collect(maps.Keys(a.BuiltWith))...)
	for _, s := range a.Services {
		add(s.BuiltWith...)
	}
	return a
}

// PublicService is a service with a public address or port.
type PublicService struct {
	Name, Title string
	// Hosts are its public addresses.
	Hosts []string
	// BuiltWith is what it's built with (pkgspec.Package.BuiltWithNames).
	BuiltWith []string
	// Own is its own pause; ByAll and ByBuiltWith are the lab's that
	// cover it; Advisory is the security advisory Roostack paused it for.
	Own         *config.Pause
	ByAll       bool
	ByBuiltWith []string
	Advisory    *ServiceAdvisory
}

// Paused says whether its public access is paused, whichever way.
func (s PublicService) Paused() bool {
	return s.Own != nil || s.ByAll || len(s.ByBuiltWith) > 0 || s.Advisory != nil
}

// PausedServices are the public services whose public access is paused.
func (a PublicPauses) PausedServices() []PublicService {
	var out []PublicService
	for _, s := range a.Services {
		if s.Paused() {
			out = append(out, s)
		}
	}
	return out
}

// PublicPauses lists the lab's public services and its pauses.
func (m *Manager) PublicPauses() (PublicPauses, error) {
	services, err := state.LoadServices(m.Lab)
	if err != nil && len(services) == 0 {
		return PublicPauses{}, err
	}
	pp := PublicPausesOf(m.labNow(), services).WithAdvisories(m.advisories(context.Background(), m.labNow(), services))
	if m.RegistryIndex != nil {
		if ix, err := m.RegistryIndex(); err == nil {
			pp = pp.WithTitles(ix)
		}
	}
	return pp, nil
}

// WithAdvisories marks the services an advisory pauses, from advs (by
// service name, as AdvisoriesOf).
func (a PublicPauses) WithAdvisories(advs map[string][]*ServiceAdvisory) PublicPauses {
	for i := range a.Services {
		for _, adv := range advs[a.Services[i].Name] {
			if adv.Paused {
				a.Services[i].Advisory = adv
				break
			}
		}
	}
	return a
}

// builtWithTitle is a built-with name as people write it (the registry's
// title, else the name).
func (m *Manager) builtWithTitle(name string) string {
	if m.RegistryIndex != nil {
		if ix, err := m.RegistryIndex(); err == nil {
			if t := ix.BuiltWithTitle(name); t != "" {
				return t
			}
		}
	}
	return name
}

// BuiltWith is what a service is built with, as people write it (the
// registry's titles), for its page.
func (m *Manager) BuiltWith(name string) []string {
	p, err := m.ServicePackage(name)
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range p.BuiltWithNames() {
		out = append(out, m.builtWithTitle(n))
	}
	return out
}

// PublicPausesOf is PublicPauses for a lab's settings and services (the
// command line has no manager).
func PublicPausesOf(lab config.Lab, services []state.Service) PublicPauses {
	pause := lab.Public.Pause
	out := PublicPauses{All: pause.All, BuiltWith: pause.BuiltWith}
	for _, s := range services {
		ps, ok := PublicServiceOf(lab, s)
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
	return out
}

// PublicServiceOf is s as a PublicService, if any of its addresses or
// ports is public.
func PublicServiceOf(lab config.Lab, s state.Service) (PublicService, bool) {
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
	if err := r.Check(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	services, err := m.publicServices(r.Services)
	if err != nil {
		return err
	}
	p := config.Pause{Since: time.Now().UTC().Truncate(time.Second), By: by}
	if r.All || len(r.BuiltWith) > 0 {
		if err := m.saveLab(func(l *config.Lab) { l.Public.Pause = r.Paused(l.Public.Pause, p) }); err != nil {
			return err
		}
	}
	if err := SaveServicePauses(m.Lab, services, &p); err != nil {
		return err
	}
	m.startApplyNowLocked("Pausing public access to " + PauseWhat(r, services, m.builtWithTitle))
	return nil
}

// Paused is the lab's pauses with r's added (its services aside), each
// from p; what's paused already keeps its first pause. The lab's own isn't
// changed: its map is copied, since a runtime copy may share it.
func (r PauseRequest) Paused(pause config.PublicPause, p config.Pause) config.PublicPause {
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
	return pause
}

// Lifted is the lab's pauses without r's (its services aside).
func (r PauseRequest) Lifted(pause config.PublicPause) config.PublicPause {
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
	return pause
}

// SaveServicePauses sets each service's own pause to p, or lifts it when
// p is nil, in its instance file; a service already paused keeps its
// first pause.
func SaveServicePauses(lab config.Lab, services []state.Service, p *config.Pause) error {
	for _, s := range services {
		if (s.Instance.PublicPause != nil) == (p != nil) {
			continue
		}
		s.Instance.PublicPause = p
		if err := s.Instance.Save(state.InstanceFile(lab, s.Name())); err != nil {
			return err
		}
	}
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
		if err := m.saveLab(func(l *config.Lab) { l.Public.Pause = r.Lifted(l.Public.Pause) }); err != nil {
			return err
		}
	}
	if err := SaveServicePauses(m.Lab, services, nil); err != nil {
		return err
	}
	m.startApplyNowLocked("Opening public access again to " + PauseWhat(r, services, m.builtWithTitle))
	return nil
}

// ReloadPauses takes the lab's pauses as lab.yaml has them now and applies
// them at once: `roostack public` changed them from the command line and
// asked this running Roostack to read them (SIGHUP) rather than restart
// it (SPEC.md §6.16). Services' own pauses are in their instance files,
// which every apply reads.
func (m *Manager) ReloadPauses(pause config.PublicPause) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.labNow()
	l.Public.Pause = pause
	m.saved.Store(&l)
	m.startApplyNowLocked("Pausing or opening public access from the server's command line")
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
	return PublicServicesNamed(m.labNow(), all, names)
}

// PublicServicesNamed are the named services of all, each of which must
// have a public address or port.
func PublicServicesNamed(lab config.Lab, all []state.Service, names []string) ([]state.Service, error) {
	var out []state.Service
	for _, name := range names {
		i := slices.IndexFunc(all, func(s state.Service) bool { return s.Name() == name })
		if i < 0 {
			return nil, ErrNotFound
		}
		if _, public := PublicServiceOf(lab, all[i]); !public {
			return nil, usererr.New("%s isn't open to the internet, so there's nothing to pause.", all[i].Title())
		}
		out = append(out, all[i])
	}
	return out, nil
}

// PauseWhat says what a pause or lift is about ("everything", "services
// built with PHP", "Lychee"), for the apply's reason and the audit log;
// title is how a built-with name is written (PublicPauses.Title).
func PauseWhat(r PauseRequest, services []state.Service, title func(string) string) string {
	var parts []string
	if r.All {
		parts = append(parts, "everything")
	}
	if len(r.BuiltWith) > 0 {
		var bw []string
		for _, b := range r.BuiltWith {
			bw = append(bw, title(b))
		}
		parts = append(parts, "services built with "+words.And(bw))
	}
	for _, s := range services {
		parts = append(parts, s.Title())
	}
	return words.And(parts)
}
