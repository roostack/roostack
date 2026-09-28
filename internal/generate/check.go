package generate

import (
	"fmt"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
)

// Check generates a package against a sample lab, so template mistakes
// (e.g. comparing a bool setting to a string) show when the package is
// validated instead of when someone installs it. It tries the default
// settings and every bool flipped, with and without a GPU, so templates
// behind when: conditions are rendered too. Install hooks and actions are
// rendered as well.
func Check(p *pkgspec.Package) error {
	lab := config.Default()
	lab.Domain = "lab.example.com"
	lab.Timezone = "Europe/London"

	defaults, flipped := map[string]any{}, map[string]any{}
	for _, s := range p.Settings {
		switch s.Type {
		case pkgspec.SettingBool:
			on := s.Default == true
			defaults[s.Key], flipped[s.Key] = on, !on
		case pkgspec.SettingString:
			defaults[s.Key], flipped[s.Key] = "example", "example"
		case pkgspec.SettingInt:
			defaults[s.Key], flipped[s.Key] = 1, 1
		}
	}
	routes := map[string]instance.RouteOverride{}
	port := config.DefaultRoutePortMin
	for name := range p.Routes {
		routes[name] = instance.RouteOverride{Port: port}
		port++
	}
	// Ports turned on, so templates see them enabled.
	on := true
	ports := map[string]instance.PortOverride{}
	for name := range p.Ports {
		ports[name] = instance.PortOverride{Enabled: &on}
	}

	for _, settings := range []map[string]any{defaults, flipped} {
		for _, gpu := range []bool{false, true} {
			lab.HasGPU = gpu
			in := &instance.Instance{Package: p.Name, Version: p.Version, Updates: instance.UpdatesNotify,
				Settings: settings, Routes: routes, Ports: ports}
			if _, err := Service(lab, p, in); err != nil {
				return fmt.Errorf("with settings %v, GPU %v: %w", settings, gpu, err)
			}
			for _, h := range p.Hooks {
				inputs := map[string]string{}
				for _, i := range h.Inputs {
					inputs[i.Key] = "example"
				}
				if _, err := Hook(lab, p, in, h, inputs); err != nil {
					return fmt.Errorf("hook %q: %w", h.Name, err)
				}
			}
		}
	}
	return nil
}
