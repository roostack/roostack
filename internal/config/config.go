// Package config holds lab-wide settings and the central default paths.
//
// All default locations live in this file so they can be changed in one
// place (SPEC.md §6.7).
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Default paths. See SPEC.md §6.7 and docs/ASSUMPTIONS.md (A2).
const (
	DefaultStateDir    = "/var/lib/mylab"
	DefaultHomeDir     = "/var/lib/mylab/home"
	DefaultServicesDir = "/srv/mylab/services"
	DefaultMediaDir    = "/srv/mylab/media"
)

// Paths are the filesystem locations MyLab reads and writes.
type Paths struct {
	StateDir    string `yaml:"state_dir"`    // desired state, SQLite, secrets metadata
	HomeDir     string `yaml:"home_dir"`     // home of the mylab user
	ServicesDir string `yaml:"services_dir"` // per-service data (bind mounts)
	MediaDir    string `yaml:"media_dir"`    // large data, e.g. a separate disk
}

// QuadletDir is where rootless Quadlet files for the mylab user live.
func (p Paths) QuadletDir() string {
	return filepath.Join(p.HomeDir, ".config", "containers", "systemd")
}

// InstancesDir holds one instance YAML file per installed service.
func (p Paths) InstancesDir() string { return filepath.Join(p.StateDir, "services") }

// CaddyDir holds the generated Caddyfile and per-service site snippets.
func (p Paths) CaddyDir() string { return filepath.Join(p.StateDir, "caddy") }

// DNS configures the DNS-01 challenge used for the wildcard certificate.
type DNS struct {
	// Provider is a known provider name (e.g. "cloudflare").
	Provider string `yaml:"provider"`
	// Directive, if set, is used verbatim inside Caddy's tls block instead of
	// the built-in provider template (e.g. "dns porkbun {...}").
	Directive string `yaml:"directive,omitempty"`
}

// Lab is the lab-wide configuration (/var/lib/mylab/lab.yaml).
type Lab struct {
	// Domain is the base domain; services live on subdomains of it.
	Domain string `yaml:"domain"`
	// Email is used for the ACME (Let's Encrypt) account.
	Email string `yaml:"email"`
	DNS   DNS    `yaml:"dns"`
	Paths Paths  `yaml:"paths"`
	// HasGPU is detected at runtime, not configured. It resolves settings
	// with `default: auto` (docs/ASSUMPTIONS.md G1).
	HasGPU bool `yaml:"-"`
}

// Default returns a Lab with default paths and no domain.
func Default() Lab {
	return Lab{Paths: Paths{
		StateDir:    DefaultStateDir,
		HomeDir:     DefaultHomeDir,
		ServicesDir: DefaultServicesDir,
		MediaDir:    DefaultMediaDir,
	}}
}

// Load reads a lab config file. Unset paths keep their defaults.
func Load(path string) (Lab, error) {
	lab := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return lab, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&lab); err != nil {
		return lab, fmt.Errorf("%s: %w", path, err)
	}
	return lab, lab.Validate()
}

// Validate checks the config is usable.
func (l Lab) Validate() error {
	var errs []error
	if l.Domain == "" {
		errs = append(errs, errors.New("domain is required"))
	}
	for _, p := range []struct{ name, path string }{
		{"state_dir", l.Paths.StateDir}, {"home_dir", l.Paths.HomeDir},
		{"services_dir", l.Paths.ServicesDir}, {"media_dir", l.Paths.MediaDir},
	} {
		if !filepath.IsAbs(p.path) {
			errs = append(errs, fmt.Errorf("paths.%s must be absolute, got %q", p.name, p.path))
		}
	}
	return errors.Join(errs...)
}
