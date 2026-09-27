// Package instance defines a user's installed service: their choices on top
// of a package (SPEC.md §8.1). Instances are desired state, stored as
// /var/lib/mylab/services/<name>.yaml.
package instance

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"

	"github.com/mylab/mylab/internal/nodeid"
	"github.com/mylab/mylab/internal/pkgspec"
	"go.yaml.in/yaml/v3"
)

// Update policies (SPEC.md §7).
const (
	UpdatesNotify = "notify"
	UpdatesPatch  = "patch"
	UpdatesMinor  = "minor"
	UpdatesAll    = "all"
)

var subdomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Instance is the user's desired state for one installed service.
type Instance struct {
	Package string `yaml:"package"`
	// Host is the node the service runs on (SPEC.md §10.4). Always set
	// explicitly (state.AssignHosts), so moving the controller can never
	// move services with it by accident.
	Host     string                   `yaml:"host,omitempty"`
	Version  string                   `yaml:"version"`
	Routes   map[string]RouteOverride `yaml:"routes,omitempty"`
	Ports    map[string]PortOverride  `yaml:"ports,omitempty"`
	Settings map[string]any           `yaml:"settings,omitempty"`
	Updates  string                   `yaml:"updates"`
}

// RouteOverride holds user changes to a package route. Empty fields keep the
// package default.
type RouteOverride struct {
	Subdomain string `yaml:"subdomain,omitempty"`
	Exposure  string `yaml:"exposure,omitempty"`
	// Port is the host loopback port the route's container is published
	// on. MyLab allocates it once (state.AllocatePorts); it isn't meant
	// to be edited.
	Port int `yaml:"port,omitempty"`
}

// Save writes an instance file atomically. Comments in a hand-edited file
// are not preserved.
// PortOverride changes a package port (pkgspec.Port). Unset fields keep
// the package's defaults.
type PortOverride struct {
	Enabled  *bool  `yaml:"enabled,omitempty"`
	HostPort int    `yaml:"host_port,omitempty"`
	Exposure string `yaml:"exposure,omitempty"`
}

func (in *Instance) Save(path string) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(in); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads an instance file.
func Load(path string) (*Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var in Instance
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if in.Updates == "" {
		in.Updates = UpdatesNotify
	}
	return &in, nil
}

// Validate checks the instance against its package.
func (in *Instance) Validate(p *pkgspec.Package) error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if in.Package != p.Name {
		add("package: instance is for %q but package is %q", in.Package, p.Name)
	}
	if in.Version != p.Version {
		add("version: instance wants %q but package is %q", in.Version, p.Version)
	}
	if in.Host != "" && !nodeid.ValidID(in.Host) {
		add("host: %q is not a node ID", in.Host)
	}
	if !slices.Contains([]string{UpdatesNotify, UpdatesPatch, UpdatesMinor, UpdatesAll}, in.Updates) {
		add("updates: must be notify, patch, minor or all; got %q", in.Updates)
	}
	for _, name := range slices.Sorted(maps.Keys(in.Routes)) {
		r := in.Routes[name]
		if p.Routes[name] == nil {
			add("routes.%s: package has no such route", name)
		}
		if r.Subdomain != "" && !subdomainRe.MatchString(r.Subdomain) {
			add("routes.%s.subdomain: %q is not a valid DNS label", name, r.Subdomain)
		}
		if r.Exposure != "" && r.Exposure != pkgspec.ExposurePrivate && r.Exposure != pkgspec.ExposurePublic {
			add("routes.%s.exposure: must be private or public", name)
		}
		if r.Port != 0 && (r.Port < 1024 || r.Port > 65535) {
			add("routes.%s.port: %d is outside 1024-65535", name, r.Port)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(in.Ports)) {
		o := in.Ports[name]
		if p.Ports[name] == nil {
			add("ports.%s: package has no such port", name)
		}
		if o.HostPort != 0 && (o.HostPort < pkgspec.MinHostPort || o.HostPort > 65535) {
			add("ports.%s.host_port: %d is outside %d-65535", name, o.HostPort, pkgspec.MinHostPort)
		}
		if o.Exposure != "" && o.Exposure != pkgspec.ExposurePrivate && o.Exposure != pkgspec.ExposurePublic {
			add("ports.%s.exposure: must be private or public", name)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(in.Settings)) {
		s := p.Setting(key)
		switch {
		case s == nil:
			add("settings.%s: package has no such setting", key)
		case s.Type == pkgspec.SettingSecret:
			add("settings.%s: secrets are stored as Podman secrets, not in the instance file", key)
		case !typeMatches(s.Type, in.Settings[key]):
			add("settings.%s: expected a %s", key, s.Type)
		}
	}
	return errors.Join(errs...)
}

func typeMatches(typ string, v any) bool {
	switch typ {
	case pkgspec.SettingBool:
		_, ok := v.(bool)
		return ok
	case pkgspec.SettingString:
		_, ok := v.(string)
		return ok
	case pkgspec.SettingInt:
		_, ok := v.(int)
		return ok
	}
	return false
}
