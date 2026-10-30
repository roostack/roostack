// Package pkgspec defines the Roostack package format (SPEC.md §8.1): the
// catalog file that describes how to run an app well inside Roostack.
package pkgspec

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// SchemaV1Alpha1 is the only supported schema version. Breaking changes are
// allowed before 1.0 (SPEC.md §8).
const SchemaV1Alpha1 = "roostack/v1alpha1"

// Package is one version of a package (catalog/<name>/<version>.yaml, as
// in the registry: SPEC.md §8.3.3).
type Package struct {
	Schema      string   `yaml:"schema"`
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	Version     string   `yaml:"version"`
	About       About    `yaml:"about"`
	Requires    Requires `yaml:"requires"`
	// BuiltWith names what the app is built on (PHP, Laravel, Log4j), as
	// short names from the list the service registry keeps (SPEC.md
	// §6.16): what pausing public access by what services are built with
	// matches, and what the service's page shows. Not a bill of materials:
	// the registry's CI finds exact versions with Syft. Its databases are
	// added from databases: (BuiltWithNames).
	BuiltWith []string `yaml:"built_with"`
	// UpgradeNotes are shown before a service updates to this version
	// (SPEC.md §8.3.5).
	UpgradeNotes []UpgradeNote         `yaml:"upgrade_notes"`
	Settings     []Setting             `yaml:"settings"`
	Containers   map[string]*Container `yaml:"containers"`
	Storage      map[string]*Storage   `yaml:"storage"`
	Databases    map[string]*Database  `yaml:"databases"`
	Routes       map[string]*Route     `yaml:"routes"`
	Ports        map[string]*Port      `yaml:"ports"`
	Hooks        []*Hook               `yaml:"hooks"`
	Links        []Link                `yaml:"links"`
	// SSO is how the app takes single sign-on (sso.go, SPEC.md §5.2).
	SSO *SSO `yaml:"sso"`
}

// Hook events.
const (
	// HookInstall runs once, after the service's first successful start
	// (e.g. creating the admin account). It is retried until the service
	// is ready, so it must be safe to run more than once.
	HookInstall = "install"
	// HookAction is a button on the service page (e.g. "Reset admin
	// password"), run by an Admin.
	HookAction = "action"
	// HookSSO runs when single sign-on is turned on, changed or turned
	// off, with ROOSTACK_SSO=on|off, for apps set by a command or their
	// own API (Forgejo, Immich). It must be safe to run again (SPEC.md
	// §5.2, "Connecting the remaining apps").
	HookSSO = "sso"
)

// Hook runs a command inside one of the service's containers.
//
// User-provided values (settings, inputs) reach the command only through
// environment variables, never templated into the command itself, so they
// can't change what a shell script does.
type Hook struct {
	On          string `yaml:"on"`   // install, action or sso
	Name        string `yaml:"name"` // actions: used in URLs
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Container   string `yaml:"container"`
	// Command is run as-is (no templating).
	Command []string `yaml:"command"`
	// Env values are templates: {{ .Settings.x }}, {{ .Inputs.x }}, ...
	Env map[string]string `yaml:"env"`
	// Secrets maps an environment variable to a secret setting, or for
	// actions "inputs.<key>" for a secret input.
	Secrets map[string]string `yaml:"secrets"`
	// Inputs are asked for when an action runs (string or secret).
	Inputs []Setting `yaml:"inputs"`
	// Saves names a from_hook secret setting: what the hook prints on
	// standard output becomes its value, and is never shown or logged
	// (install and action hooks only). Its standard error is its log.
	Saves string `yaml:"saves"`
}

// HooksOn returns the package's hooks for an event, in order.
func (p *Package) HooksOn(event string) []*Hook {
	var hooks []*Hook
	for _, h := range p.Hooks {
		if h.On == event {
			hooks = append(hooks, h)
		}
	}
	return hooks
}

// Action returns the action hook with this name, or nil.
func (p *Package) Action(name string) *Hook {
	for _, h := range p.HooksOn(HookAction) {
		if h.Name == name {
			return h
		}
	}
	return nil
}

// Input returns an action's input, or nil.
func (h *Hook) Input(key string) *Setting {
	for i := range h.Inputs {
		if h.Inputs[i].Key == key {
			return &h.Inputs[i]
		}
	}
	return nil
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
	// Generate makes Roostack create a random value (secret settings only).
	Generate bool `yaml:"generate"`
	// Required means a string can't be left empty.
	Required bool `yaml:"required"`
	// MinLength is the shortest value accepted (string and secret).
	MinLength int `yaml:"min_length"`
	// InstallOnly settings are asked for at install and used only by
	// install hooks. InstallOnly secrets are deleted once those succeed,
	// so no copy of e.g. an admin password is kept.
	InstallOnly bool `yaml:"install_only"`
	// Reveal lets Admins see a secret's value on the service page (e.g.
	// the token for the app's own admin page).
	Reveal bool `yaml:"reveal"`
	// InstallValue, if set, is the value used until the install hooks
	// succeed, whatever the Owner chose: e.g. sign-ups on so a hook can
	// make the first account by signing up, then the chosen value (SPEC.md
	// §8.1). Not for secrets.
	InstallValue any `yaml:"install_value"`
	// FromHook secrets are made by the app itself and handed to Roostack
	// by a hook that saves them (Hook.Saves): e.g. an API key Immich makes,
	// kept so a hook can change its sign-in settings later. Never shown,
	// and only hooks may use them (SPEC.md §5.2).
	FromHook bool `yaml:"from_hook"`
}

// Link points into the app's own pages, where most of its settings live
// (e.g. Forgejo's site administration).
type Link struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Route       string `yaml:"route"` // which route's address
	Path        string `yaml:"path"`  // e.g. /admin
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
	When string `yaml:"when"`
	// Memory is how much memory the container typically uses, e.g.
	// "300 MB" (ParseMemory), for the install summary (SPEC.md §8.3.4).
	// It's an estimate, not a limit.
	Memory  string  `yaml:"memory"`
	Quadlet Quadlet `yaml:"quadlet"`
}

// Mount binds a storage entry into a container, or with Libraries the
// media libraries the Owner chose for the service, each in a folder of
// its name under Path, read-only (SPEC.md §8.1, "Media libraries").
type Mount struct {
	Storage   string `yaml:"storage"`
	Libraries bool   `yaml:"libraries"`
	Path      string `yaml:"path"`
	ReadOnly  bool   `yaml:"readonly"`
}

// BuiltWithNames is what the service is built with (SPEC.md §6.16): the
// package's built_with:, then what its databases add, since Roostack runs
// them and knows what they are. A redis database is valkey on Roostack's
// own image, as it is on a package's image that names Valkey.
func (p *Package) BuiltWithNames() []string {
	out := slices.Clone(p.BuiltWith)
	for _, name := range slices.Sorted(maps.Keys(p.Databases)) {
		d := p.Databases[name]
		b := d.Type
		if b == DBRedis && (d.Image == "" || strings.Contains(d.Image, "valkey")) {
			b = "valkey"
		}
		if b != "" && !slices.Contains(out, b) {
			out = append(out, b)
		}
	}
	return out
}

// ReadsLibraries says whether any of the package's containers mounts the
// media libraries.
func (p *Package) ReadsLibraries() bool {
	for _, c := range p.Containers {
		for _, m := range c.Mounts {
			if m.Libraries {
				return true
			}
		}
	}
	return false
}

// Device is an abstract hardware request mapped to host devices by Roostack.
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
	// Title says what it holds, for people ("Your photos and videos"):
	// the summary shown before installing uses it (SPEC.md §8.3.4), and
	// shows the storage's name without one.
	Title    string `yaml:"title"`
	Class    string `yaml:"class"`
	Location string `yaml:"location"`
}

// Database types (SPEC.md §6.8).
const (
	DBSQLite   = "sqlite"
	DBPostgres = "postgres"
	DBRedis    = "redis"
	DBMySQL    = "mysql"
	DBMariaDB  = "mariadb"
)

// MySQLFamily says whether a database type is MySQL or MariaDB, which
// Roostack runs the same way (SPEC.md §8.1, "MySQL and MariaDB").
func MySQLFamily(kind string) bool { return kind == DBMySQL || kind == DBMariaDB }

// HasPassword says whether Roostack makes a password for the database
// (databases.<name>.password): the database servers, not SQLite or Redis.
func (d *Database) HasPassword() bool { return d.Type == DBPostgres || MySQLFamily(d.Type) }

// Dumped says whether the database is backed up through a dump its own
// container makes before each backup (Postgres, MySQL and MariaDB), rather
// than from its files.
func (d *Database) Dumped() bool { return d.HasPassword() && d.BackedUp() }

// Database is a Roostack-managed database following the standard pattern.
type Database struct {
	Type string `yaml:"type"`
	// Path is "<storage>/<relative path>" for sqlite.
	Path string `yaml:"path"`
	// Image overrides the default image (e.g. Postgres with extensions).
	Image string `yaml:"image"`
	// Backup defaults to true.
	Backup *bool `yaml:"backup"`
	// Memory is the database's typical memory use, if not Roostack's
	// default for its type (ExpectedMemory).
	Memory string `yaml:"memory"`
	// Schemas are more schemas on a mysql or mariadb database's server,
	// for the same user, for an app that wants several (Seafile's three):
	// each is <the database's name>_<schema>, backed up and restored with
	// the database (SPEC.md §8.1, "MySQL and MariaDB").
	Schemas []string `yaml:"schemas"`
	Quadlet Quadlet  `yaml:"quadlet"`
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
	// ClientIPHeader names a header the app reads the visitor's address
	// from, for apps that don't read X-Forwarded-For (Vaultwarden reads
	// X-Real-IP): Roostack's proxy sets it to the visitor's address,
	// replacing any the visitor sent (SPEC.md §8.1).
	ClientIPHeader string `yaml:"client_ip_header"`
	// Monitor changes how Roostack's monitoring checks the route, for an
	// app whose usual answer doesn't say it works (SPEC.md §6.15); most
	// packages leave it out.
	Monitor *RouteMonitor `yaml:"monitor"`
	// Public says what's safe when the Owner makes the address public
	// (SPEC.md §6.11).
	Public PublicRules `yaml:"public"`
}

// RouteMonitor is how monitoring checks a route (SPEC.md §6.15). Without
// it, Roostack asks the container's health.http path when the route leads
// to that port, or /, and any answer below 500 is up.
type RouteMonitor struct {
	// Path is asked instead, e.g. /api/health.
	Path string `yaml:"path"`
	// Statuses are the HTTP statuses that count as up, instead of any
	// below 500.
	Statuses []int `yaml:"statuses"`
}

// PublicRules mark what isn't safe to make public, and the login pages
// that get a strict rate limit (SPEC.md §6.11). The Owner can still make
// an unsafe address or path public, after a warning giving the reason.
type PublicRules struct {
	// Unsafe, if set, is why the whole address shouldn't be public.
	Unsafe string `yaml:"unsafe"`
	// PrivatePaths stay home-only when the address is public, e.g. an
	// admin page.
	PrivatePaths []PrivatePath `yaml:"private_paths"`
	// LoginPaths are where people sign in: public visitors get a strict
	// rate limit there.
	LoginPaths []string `yaml:"login_paths"`
}

// PrivatePath is a path, and everything under it, kept home-only on a
// public address.
type PrivatePath struct {
	Path   string `yaml:"path"`   // e.g. /admin
	Reason string `yaml:"reason"` // shown when the Owner makes it public anyway
}

// Port publishes a container port directly on the host, for protocols
// Caddy can't proxy (SSH, DNS, WireGuard, ...). Ports are off unless the
// package enables one by default; an Admin turns them on per service.
type Port struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Container   string `yaml:"container"`
	Port        int    `yaml:"port"`     // inside the container
	Protocol    string `yaml:"protocol"` // tcp (default) or udp
	// HostPort is the suggested port on the host; the Admin can change it.
	HostPort int  `yaml:"host_port"`
	Enabled  bool `yaml:"enabled"` // on by default
	// Exposure is private (home network only, the default) or public.
	Exposure string `yaml:"exposure"`
}

// Proto returns the port's protocol, defaulting to tcp.
func (p *Port) Proto() string {
	if p.Protocol == "" {
		return "tcp"
	}
	return p.Protocol
}

// DefaultExposure returns the port's exposure, defaulting to private.
func (p *Port) DefaultExposure() string {
	if p.Exposure == "" {
		return ExposurePrivate
	}
	return p.Exposure
}

// MinHostPort is the lowest host port a package port may use: lower ports
// are system services (and 80/443 belong to Caddy).
const MinHostPort = 1024

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
