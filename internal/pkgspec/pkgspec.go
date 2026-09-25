// Package pkgspec defines the MyLab package format (SPEC.md §8.1): the
// catalog file that describes how to run an app well inside MyLab.
package pkgspec

import (
	"bytes"
	"fmt"
	"os"

	"go.yaml.in/yaml/v3"
)

// SchemaV1Alpha1 is the only supported schema version. Breaking changes are
// allowed before 1.0 (SPEC.md §8).
const SchemaV1Alpha1 = "mylab/v1alpha1"

// Package is a catalog entry (catalog/<name>/package.yaml).
type Package struct {
	Schema      string                `yaml:"schema"`
	Name        string                `yaml:"name"`
	Title       string                `yaml:"title"`
	Description string                `yaml:"description"`
	Version     string                `yaml:"version"`
	Settings    []Setting             `yaml:"settings"`
	Containers  map[string]*Container `yaml:"containers"`
	Storage     map[string]*Storage   `yaml:"storage"`
	Databases   map[string]*Database  `yaml:"databases"`
	Routes      map[string]*Route     `yaml:"routes"`
}

// Setting types.
const (
	SettingBool   = "bool"
	SettingString = "string"
	SettingInt    = "int"
	SettingSecret = "secret"
)

// DefaultAuto marks a bool setting resolved from host facts at install time.
const DefaultAuto = "auto"

// Setting is a user-facing option shown at install and in service settings.
type Setting struct {
	Key         string `yaml:"key"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
	Default     any    `yaml:"default"`
	// Generate makes MyLab create a random value (secret settings only).
	Generate bool `yaml:"generate"`
}

// Container is one container in the service.
type Container struct {
	Image   string            `yaml:"image"`
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`
	// Secrets maps an environment variable to a secret reference: either a
	// secret setting key, or "databases.<name>.password".
	Secrets   map[string]string `yaml:"secrets"`
	Mounts    []Mount           `yaml:"mounts"`
	Devices   []Device          `yaml:"devices"`
	Health    *Health           `yaml:"health"`
	DependsOn []string          `yaml:"depends_on"`
	// When is a template; the container is omitted unless it renders "true".
	When    string  `yaml:"when"`
	Quadlet Quadlet `yaml:"quadlet"`
}

// Mount binds a storage entry into a container.
type Mount struct {
	Storage  string `yaml:"storage"`
	Path     string `yaml:"path"`
	ReadOnly bool   `yaml:"readonly"`
}

// Device is an abstract hardware request mapped to host devices by MyLab.
type Device struct {
	GPU  string `yaml:"gpu"`
	When string `yaml:"when"`
}

// Health describes how to check a container is healthy.
type Health struct {
	HTTP *HTTPCheck `yaml:"http"`
	// Cmd runs inside the container (becomes a Podman HealthCmd).
	Cmd string `yaml:"cmd"`
}

// HTTPCheck is an HTTP health endpoint inside the container.
type HTTPCheck struct {
	Port int    `yaml:"port"`
	Path string `yaml:"path"`
}

// Storage classes (SPEC.md §6.9).
const (
	ClassCritical    = "critical"
	ClassReplaceable = "replaceable"
)

// Storage locations (SPEC.md §6.7).
const (
	LocationService = "service"
	LocationMedia   = "media"
)

// Storage is a directory owned by the service.
type Storage struct {
	Class    string `yaml:"class"`
	Location string `yaml:"location"`
}

// Database types (SPEC.md §6.8).
const (
	DBSQLite   = "sqlite"
	DBPostgres = "postgres"
	DBRedis    = "redis"
)

// Database is a MyLab-managed database following the standard pattern.
type Database struct {
	Type string `yaml:"type"`
	// Path is "<storage>/<relative path>" for sqlite.
	Path string `yaml:"path"`
	// Image overrides the default image (e.g. Postgres with extensions).
	Image string `yaml:"image"`
	// Backup defaults to true.
	Backup  *bool   `yaml:"backup"`
	Quadlet Quadlet `yaml:"quadlet"`
}

// BackedUp reports whether the database is included in backups.
func (d *Database) BackedUp() bool { return d.Backup == nil || *d.Backup }

// Exposure values for routes.
const (
	ExposurePrivate = "private"
	ExposurePublic  = "public"
)

// Route publishes a container port through Caddy on a subdomain.
type Route struct {
	Container   string `yaml:"container"`
	Port        int    `yaml:"port"`
	Subdomain   string `yaml:"subdomain"`
	Exposure    string `yaml:"exposure"`
	MaxBodySize string `yaml:"max_body_size"`
}

// Quadlet is a raw passthrough: section -> key -> scalar or list of scalars.
type Quadlet map[string]map[string]any

// Load reads and validates a package file.
func Load(path string) (*Package, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Parse decodes and validates a package. Unknown fields are errors, so typos
// are caught early.
func Parse(data []byte) (*Package, error) {
	var p Package
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, err
	}
	p.applyDefaults()
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Package) applyDefaults() {
	for _, s := range p.Storage {
		if s != nil && s.Location == "" {
			s.Location = LocationService
		}
	}
	for _, r := range p.Routes {
		if r != nil && r.Exposure == "" {
			r.Exposure = ExposurePrivate
		}
	}
}

// Setting returns the setting with the given key, or nil.
func (p *Package) Setting(key string) *Setting {
	for i := range p.Settings {
		if p.Settings[i].Key == key {
			return &p.Settings[i]
		}
	}
	return nil
}
