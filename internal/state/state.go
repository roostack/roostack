// Package state loads Roostack's desired state from disk (SPEC.md §5.1): the
// lab config plus one instance file per installed service, combined with
// their catalog packages.
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
}

// LoadServices reads every instance file and its package from the catalog.
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
		if in.Package != name {
			errs = append(errs, fmt.Errorf("%s: file name must match package %q", e.Name(), in.Package))
			continue
		}
		// Checked here, not only when generating: a service with a bad host
		// would otherwise belong to no server and silently never run.
		if in.Host != "" && !nodeid.ValidID(in.Host) {
			errs = append(errs, fmt.Errorf("%s: host %q is not a node ID", e.Name(), in.Host))
			continue
		}
		p, err := pkgspec.Load(lab.Paths.PackageFile(in.Package))
		if err != nil {
			errs = append(errs, err)
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
	hosts := map[string]string{caddy.AdminSubdomain + "." + lab.Domain: "the Roostack admin UI"}
	ports := map[int]string{lab.UIPort: "the Roostack admin UI"}
	hostPorts := map[string]string{} // "2222/tcp" -> service
	var errs []error
	for _, s := range services {
		out, err := generate.Service(lab, s.Package, s.Instance)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, h := range out.Hosts {
			if other, dup := hosts[h]; dup {
				errs = append(errs, fmt.Errorf("%s: hostname %s is already used by %s", s.Package.Name, h, other))
			}
			hosts[h] = s.Package.Name
		}
		for _, p := range out.Ports {
			if other, dup := ports[p]; dup {
				errs = append(errs, fmt.Errorf("%s: port %d is already used by %s", s.Package.Name, p, other))
			}
			ports[p] = s.Package.Name
		}
		for _, hp := range out.HostPorts {
			if err := checkHostPort(hp, s.Package.Name, ports, hostPorts); err != nil {
				errs = append(errs, err)
			}
		}
		d.Services = append(d.Services, out)
	}
	for _, s := range proxied {
		out, err := generate.ProxySite(lab, s.Package, s.Instance, s.Addr)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, h := range out.Hosts {
			if other, dup := hosts[h]; dup {
				errs = append(errs, fmt.Errorf("%s: hostname %s is already used by %s", s.Package.Name, h, other))
			}
			hosts[h] = s.Package.Name
		}
		d.Services = append(d.Services, out)
	}
	slices.SortFunc(d.Services, func(a, b *generate.Output) int { return strings.Compare(a.Service, b.Service) })
	// Generated after the services: CrowdSec runs while any is public, or
	// Roostack's own pages are (a lab on the internet, SPEC.md §6.3.2).
	public := lab.PublicUI() || slices.ContainsFunc(d.Services, func(o *generate.Output) bool { return o.Public })
	sys, err := generate.System(lab, public)
	if err != nil {
		return reconcile.Desired{}, errors.Join(append(errs, err)...)
	}
	d.System = sys
	return d, errors.Join(errs...)
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
		if s.Instance.Host == nodeID || s.Instance.Host == "" {
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
	slices.SortFunc(sorted, func(a, b Service) int { return strings.Compare(a.Package.Name, b.Package.Name) })
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
