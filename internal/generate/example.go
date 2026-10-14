package generate

import (
	"maps"
	"slices"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
)

// ExampleDomain is the example lab's domain.
const ExampleDomain = "lab.example.com"

// ExampleLab is a lab with Roostack's default folders and an example
// domain, for rendering a package without a lab of one's own (`roostack
// render -package`, which the registry's checks use to show what a
// package generates: SPEC.md §8.3.7).
func ExampleLab() config.Lab {
	lab := config.Default()
	lab.Domain = ExampleDomain
	return lab
}

// ExampleInstance is a service installed from p with its default
// settings, its routes given ports from the start of Roostack's range as
// installing would on an empty server.
func ExampleInstance(p *pkgspec.Package) *instance.Instance {
	in := &instance.Instance{Package: p.Name, Version: p.Version, Updates: instance.UpdatesNotify}
	port := config.DefaultRoutePortMin
	for _, name := range slices.Sorted(maps.Keys(p.Routes)) {
		if in.Routes == nil {
			in.Routes = map[string]instance.RouteOverride{}
		}
		in.Routes[name] = instance.RouteOverride{Port: port}
		port++
	}
	return in
}
