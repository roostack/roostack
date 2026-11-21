// Package state loads Roostack's desired state from disk (SPEC.md §5.1): the
// lab config plus one instance file per installed service, combined with
// the lab's copy of their packages (SPEC.md §8.3.1).
package state

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/reconcile"
)

// Service is one installed service with its package.
type Service struct {
	Instance *instance.Instance
	Package  *pkgspec.Package
	// Stop, when set, is Roostack stopping the service because the
	// registry withdrew its version as dangerous (SPEC.md §8.3.4). Worked
	// out by the controller for each assignment, never saved.
	Stop *Stop
}

// Stop is how a stopped service runs (generate.Stop).
type Stop struct {
	// LastBackup: databases on the package's own images still run, until
	// a backup of the server has finished since the stop.
	LastBackup bool `json:"last_backup,omitempty"`
}

// Output is what runs of a service on its server: generate.Service's, but
// only its network and databases while Roostack has stopped it (SPEC.md
// §8.3.4), it waits for its data to be brought in (SPEC.md §8.4) or it's
// held after a restore that didn't finish (SPEC.md §6.9) or its databases
// wait to be loaded after a lab restore (labrestore), and only its
// network and its address's "turned off" page while someone has turned
// it off (SPEC.md §6.3.5).
func Output(lab config.Lab, s Service) (*generate.Output, error) {
	out, err := generate.Service(lab, s.Package, s.Instance)
	if err != nil {
		return nil, err
	}
	trim(out, s)
	return out, nil
}

// trim leaves out of a service's output what doesn't run now (Output).
func trim(out *generate.Output, s Service) {
	switch {
	case s.Instance != nil && s.Instance.Off != nil:
		generate.Off(out)
	case s.Stop != nil:
		generate.Stop(out, s.Package, s.Stop.LastBackup)
	case s.Instance != nil && s.Instance.AwaitingData:
		// Its databases run (empty until the switch loads them), so
		// what's brought can be checked against them.
		generate.Stop(out, s.Package, true)
	case s.Instance != nil && s.Instance.Held != nil:
		// Its databases run, so they can be loaded again (SPEC.md §6.9,
		// "A restore that didn't finish").
		generate.Stop(out, s.Package, true)
	case s.Instance != nil && s.Instance.DumpsPending != nil:
		// Its databases run, empty until a lab restore's last step loads
		// their dumps; then the app starts (labrestore).
		generate.Stop(out, s.Package, true)
	}
}

// Name is the service's ID (instance.Instance.ID): a second copy of a
// package has its own (SPEC.md §8.2).
func (s Service) Name() string {
	if s.Instance == nil {
		return s.Package.Name
	}
	return s.Instance.ID()
}

// Title is what people call it: its own title, or its package's.
func (s Service) Title() string {
	if s.Instance != nil && s.Instance.Title != "" {
		return s.Instance.Title
	}
	return s.Package.Title
}

// LoadServices reads every instance file and the lab's copy of its package
// version (LoadPackage).
func LoadServices(lab config.Lab) ([]Service, error) {
	dir := lab.Paths.InstancesDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var services []Service
	var errs []error
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".yaml")
		if e.IsDir() || !ok {
			continue
		}
		in, err := instance.Load(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if in.ID() != name {
			errs = append(errs, fmt.Errorf("%s: file name must match its name %q", e.Name(), in.ID()))
			continue
		}
		// Checked here, not only when generating: a service with a bad host
		// would otherwise belong to no server and silently never run.
		if in.Host != "" && !nodeid.ValidID(in.Host) {
			errs = append(errs, fmt.Errorf("%s: host %q is not a node ID", e.Name(), in.Host))
			continue
		}
		p, err := LoadPackage(lab.Paths, in.Package, in.Version)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		services = append(services, Service{Instance: in, Package: p})
	}
	return services, errors.Join(errs...)
}

// Proxied is a service running on another server whose routes this
// server's Caddy serves (SPEC.md §10.5), reached at Addr.
type Proxied struct {
	Service
	Addr string
}

// Desired generates the full desired state for the reconciler: this
// server's services, and Caddy sites for services it proxies.
func Desired(lab config.Lab, services []Service, proxied ...Proxied) (reconcile.Desired, error) {
	var d reconcile.Desired
	// Every hostname must be unique across the lab, including the admin UI.
	hosts := map[string]string{lab.UIHost(): "the Roostack admin UI",
		caddy.LinkSubdomain + "." + lab.Domain:   "Roostack's address for other servers and labs",
		caddy.StatusSubdomain + "." + lab.Domain: "the lab's public status page",
		lab.SignInHost():                         "the sign-in page for services"}
	for _, g := range lab.GlancesSites {
		hosts[g.Host] = "a server's live view"
	}
	ports := map[int]string{lab.UIPort: "the Roostack admin UI", config.SignInPort: "the sign-in page for services"}
	hostPorts := map[string]string{} // "2222/tcp" -> service
	var errs []error
	// Each service's first address, where the domain itself may go
	// (SPEC.md §5.3).
	first := map[string]string{}
	lab.SharedLibraries = SharedLibraries(lab, services)
	tight := MemoryTight(lab, services)
	for _, s := range services {
		out, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		limit, err := generate.MemoryLimit(lab, s.Package, s.Instance, tight)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: memory limit: %w", s.Name(), err))
		}
		slice := generate.ServiceSliceFile(s.Name(), limit)
		out.Slice = &slice
		// A stopped service keeps its names and ports, so nothing else takes
		// them while it's stopped.
		for _, h := range out.Hosts {
			if other, dup := hosts[h]; dup {
				errs = append(errs, &HostInUseError{Service: s.Name(), Host: h, UsedBy: other})
			}
			hosts[h] = s.Name()
		}
		if len(out.Hosts) > 0 {
			first[s.Name()] = out.Hosts[0]
		}
		// A service with ports and no web address has a name of its own
		// for them (generate.PortHost), kept for it as an address is.
		if h := generate.PortHost(lab, s.Package, s.Instance); h != "" && len(s.Package.Routes) == 0 {
			if other, dup := hosts[h]; dup {
				errs = append(errs, &HostInUseError{Service: s.Name(), Host: h, UsedBy: other})
			}
			hosts[h] = s.Name()
		}
		for _, p := range out.Ports {
			if other, dup := ports[p]; dup {
				errs = append(errs, &PortInUseError{Service: s.Name(), Port: p, UsedBy: other})
			}
			ports[p] = s.Name()
		}
		for _, hp := range out.HostPorts {
			if err := checkHostPort(hp, s.Name(), ports, hostPorts); err != nil {
				errs = append(errs, err)
			}
		}
		trim(out, s)
		d.Services = append(d.Services, out)
	}
	for _, s := range proxied {
		if s.Stop != nil || (s.Instance != nil && (s.Instance.AwaitingData || s.Instance.Held != nil || s.Instance.DumpsPending != nil)) {
			continue // its server has stopped it: nothing to pass on
		}
		out, err := generate.ProxySite(lab, s.Package, s.Instance, s.Addr)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, h := range out.Hosts {
			if other, dup := hosts[h]; dup {
				errs = append(errs, &HostInUseError{Service: s.Name(), Host: h, UsedBy: other})
			}
			hosts[h] = s.Name()
		}
		if len(out.Hosts) > 0 {
			first[s.Name()] = out.Hosts[0]
		}
		d.Services = append(d.Services, out)
	}
	slices.SortFunc(d.Services, func(a, b *generate.Output) int { return strings.Compare(a.Service, b.Service) })
	// Generated after the services: CrowdSec runs while any is public, or
	// Roostack's own pages are (a lab on the internet, SPEC.md §6.3.2).
	public := lab.PublicUI() || slices.ContainsFunc(d.Services, func(o *generate.Output) bool { return o.Public })
	// The sign-in page and Your account, my.<domain>, always (SPEC.md
	// §5.3): public while a service using single sign-on is, or Roostack's
	// own pages are, which sign in there too (SPEC.md §5.2).
	sys, err := generate.SystemWith(lab, generate.SystemOptions{Public: public, SignIn: true,
		SignInPublic: lab.PublicUI() || slices.ContainsFunc(d.Services, func(o *generate.Output) bool { return o.SSOPublic }),
		FirstHosts:   first})
	if err != nil {
		return reconcile.Desired{}, errors.Join(append(errs, err)...)
	}
	d.System = sys
	return d, errors.Join(errs...)
}

// MemoryTight says whether the services on this server together expect
// more memory than they may use (generate.ServicesMemory), which is when
// each is held to its own automatic limit (generate.MemoryLimit). A
// service that's turned off uses none.
func MemoryTight(lab config.Lab, services []Service) bool {
	can := generate.ServicesMemory(lab.MemoryTotal)
	if can == 0 {
		return false
	}
	var expected int64
	for _, s := range services {
		if s.Instance.Off != nil {
			continue
		}
		if mem, err := generate.ExpectedMemory(lab, s.Package, s.Instance); err == nil {
			expected += mem
		}
	}
	return expected > can
}

// HostInUseError is a web address (hostname) that another service, or
// Roostack itself, already has: every hostname is unique across the lab.
type HostInUseError struct {
	Service, Host string
	// UsedBy is what already has the hostname.
	UsedBy string
}

func (e *HostInUseError) Error() string {
	return fmt.Sprintf("%s: hostname %s is already used by %s", e.Service, e.Host, e.UsedBy)
}

// PortInUseError is a package port that clashes with another port Roostack
// uses.
type PortInUseError struct {
	Service string
	Port    int
	// UsedBy is what already has the port, or "" if the port is in the
	// range Roostack keeps for its web services.
	UsedBy string
}

func (e *PortInUseError) Error() string {
	if e.UsedBy == "" {
		return fmt.Sprintf("%s: port %d is reserved for Roostack's web services (%d-%d)",
			e.Service, e.Port, config.DefaultRoutePortMin, config.DefaultRoutePortMax)
	}
	return fmt.Sprintf("%s: port %d is already used by %s", e.Service, e.Port, e.UsedBy)
}

// checkHostPort checks an enabled package port against the loopback ports
// (which are TCP) and the other package ports.
func checkHostPort(hp generate.HostPort, service string, ports map[int]string, hostPorts map[string]string) error {
	if hp.Port >= config.DefaultRoutePortMin && hp.Port <= config.DefaultRoutePortMax {
		return &PortInUseError{Service: service, Port: hp.Port}
	}
	key := fmt.Sprintf("%d/%s", hp.Port, hp.Protocol)
	other, dup := hostPorts[key]
	if !dup && hp.Protocol == "tcp" {
		other, dup = ports[hp.Port]
	}
	if dup {
		return &PortInUseError{Service: service, Port: hp.Port, UsedBy: other}
	}
	hostPorts[key] = service
	// A well-known port the firewall sends on is one service's too.
	if hp.Outside != 0 {
		key := fmt.Sprintf("outside %d/%s", hp.Outside, hp.Protocol)
		if other, dup := hostPorts[key]; dup {
			return &PortInUseError{Service: service, Port: hp.Outside, UsedBy: other}
		}
		hostPorts[key] = service
	}
	return nil
}

// AssignHosts places services that have no host yet on nodeID (services
// installed before a lab could span several servers, SPEC.md §10.4).
// It returns the ones it changed, to be saved.
func AssignHosts(services []Service, nodeID string) []Service {
	var changed []Service
	for _, s := range services {
		if s.Instance.Host == "" && nodeID != "" {
			s.Instance.Host = nodeID
			changed = append(changed, s)
		}
	}
	return changed
}

// OnNode returns the services placed on nodeID. A service with no host
// yet counts as this node's (see AssignHosts).
func OnNode(services []Service, nodeID string) []Service {
	var out []Service
	for _, s := range services {
		// One waiting to be restored runs nowhere (SPEC.md §10.10).
		if !s.Instance.Waiting && (s.Instance.Host == nodeID || s.Instance.Host == "") {
			out = append(out, s)
		}
	}
	return out
}

// AllocatePorts gives every route without a host port the lowest free port
// in the lab's range (SPEC.md §6.5). It returns the services whose instance
// changed, so the caller can save them. Once stored, a port never changes.
func AllocatePorts(services []Service) ([]Service, error) {
	used := map[int]bool{}
	for _, s := range services {
		for _, r := range s.Instance.Routes {
			if r.Port != 0 {
				used[r.Port] = true
			}
		}
	}
	next := config.DefaultRoutePortMin
	var changed []Service
	sorted := slices.Clone(services)
	slices.SortFunc(sorted, func(a, b Service) int { return strings.Compare(a.Name(), b.Name()) })
	for _, s := range sorted {
		dirty := false
		for _, name := range slices.Sorted(maps.Keys(s.Package.Routes)) {
			r := s.Instance.Routes[name]
			if r.Port != 0 {
				continue
			}
			for used[next] {
				next++
			}
			if next > config.DefaultRoutePortMax {
				return changed, errors.New("no free route ports left")
			}
			r.Port = next
			used[next] = true
			if s.Instance.Routes == nil {
				s.Instance.Routes = map[string]instance.RouteOverride{}
			}
			s.Instance.Routes[name] = r
			dirty = true
		}
		if dirty {
			changed = append(changed, s)
		}
	}
	return changed, nil
}

// InstanceFile is where a service's instance is stored.
func InstanceFile(lab config.Lab, name string) string {
	return filepath.Join(lab.Paths.InstancesDir(), name+".yaml")
}

// SharedLibraries are the folders the services offer as media libraries
// (pkgspec.Storage.Shared), by library name: those of the server the
// services run on, for the services there that read libraries (SPEC.md
// §8.1, "A service's folder as a library"). A service waiting to be
// restored has none.
func SharedLibraries(lab config.Lab, services []Service) map[string]string {
	out := map[string]string{}
	for _, s := range services {
		if s.Instance.Waiting {
			continue
		}
		for _, st := range s.Package.SharedStorage() {
			if l := pkgspec.SharedLibrary(s.Name(), st); l != "" {
				out[l] = generate.StoragePath(lab, s.Package, s.Instance, st)
			}
		}
	}
	return out
}
