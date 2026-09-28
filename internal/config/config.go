// Package config holds lab-wide settings and the central default paths.
//
// All default locations live in this file so they can be changed in one
// place (SPEC.md §6.7).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Default paths. See SPEC.md §6.7 and docs/ASSUMPTIONS.md (A2).
const (
	DefaultStateDir    = "/var/lib/roostack"
	DefaultHomeDir     = "/var/lib/roostack/home"
	DefaultServicesDir = "/srv/roostack/services"
	DefaultMediaDir    = "/srv/roostack/media"
	// DefaultCatalogDir holds official packages shipped with Roostack.
	DefaultCatalogDir = "/usr/share/roostack/catalog"
	// DefaultUIPort is where the roostack web UI listens on the host.
	DefaultUIPort = 8480
	// DefaultUIBind keeps the UI on the host's loopback; Caddy reaches it
	// through pasta's loopback mapping (SPEC.md §6.5).
	DefaultUIBind = "127.0.0.1"
	// Route ports: each routed container is published on
	// 127.0.0.1:<port>, allocated from this range and stored in its
	// instance file so it never changes (SPEC.md §6.5).
	DefaultRoutePortMin = 20000
	DefaultRoutePortMax = 29999
)

// Paths are the filesystem locations Roostack reads and writes.
type Paths struct {
	StateDir    string `yaml:"state_dir"`    // desired state, SQLite, secrets metadata
	HomeDir     string `yaml:"home_dir"`     // home of the roostack user
	ServicesDir string `yaml:"services_dir"` // per-service data (bind mounts)
	MediaDir    string `yaml:"media_dir"`    // large data, e.g. a separate disk
	CatalogDir  string `yaml:"catalog_dir"`  // packages: <name>/package.yaml
}

// QuadletDir is where rootless Quadlet files for the roostack user live.
func (p Paths) QuadletDir() string {
	return filepath.Join(p.HomeDir, ".config", "containers", "systemd")
}

// LabFile is the lab-wide config file.
func (p Paths) LabFile() string { return filepath.Join(p.StateDir, "lab.yaml") }

// PackageFile is where a catalog package is defined.
func (p Paths) PackageFile(name string) string {
	return filepath.Join(p.CatalogDir, name, "package.yaml")
}

// BackupStoreDir is where this server keeps backups it stores for other
// servers (SPEC.md §10.10), next to the media folder on the big disk.
func (p Paths) BackupStoreDir() string { return filepath.Join(filepath.Dir(p.MediaDir), "backups") }

// InstancesDir holds one instance YAML file per installed service.
func (p Paths) InstancesDir() string { return filepath.Join(p.StateDir, "services") }

// CaddyDir holds the generated Caddyfile and per-service site snippets
// (mounted read-only at /etc/caddy).
func (p Paths) CaddyDir() string { return filepath.Join(p.StateDir, "caddy") }

// CaddySitesDir holds one snippet per service.
func (p Paths) CaddySitesDir() string { return filepath.Join(p.CaddyDir(), "sites") }

// CaddyDataDir holds certificates and the ACME account.
func (p Paths) CaddyDataDir() string { return filepath.Join(p.StateDir, "caddy-data") }

// DNS configures the DNS-01 challenge used for the wildcard certificate.
type DNS struct {
	// Provider is a provider ID from internal/dns (e.g. "cloudflare").
	Provider string `yaml:"provider"`
	// Fields lists which of the provider's credential fields are set. Their
	// values are Podman secrets, never stored here. Empty means the
	// provider's required fields.
	Fields []string `yaml:"fields,omitempty"`
	// Directive, if set, is used verbatim inside Caddy's tls block instead of
	// the provider's generated one (advanced use).
	Directive string `yaml:"directive,omitempty"`
	// Zone is the provider's DNS zone for the domain (e.g. example.com for
	// lab.example.com), found by the setup wizard's check.
	Zone string `yaml:"zone,omitempty"`
	// ManageRecords lets Roostack create the DNS records services need
	// (SPEC.md §6.5.1).
	ManageRecords bool `yaml:"manage_records,omitempty"`
	// AllowRiskyRecords is the Owner's opt-in for providers whose API
	// rewrites every record to change one (Namecheap).
	AllowRiskyRecords bool `yaml:"allow_risky_records,omitempty"`
	// LANIP overrides the detected LAN address used for private services.
	LANIP string `yaml:"lan_ip,omitempty"`
}

// Backup configures the backup destination (SPEC.md §6.9).
type Backup struct {
	// Repository is a restic repository: a local path (e.g. a second disk)
	// or a remote such as s3:... or sftp:... Empty disables backups.
	Repository string `yaml:"repository"`
	// Env holds backend credentials as KEY=value (e.g. for S3).
	Env []string `yaml:"env,omitempty"`
	// LimitUploadKiB caps upload speed; 0 is unlimited.
	LimitUploadKiB int `yaml:"limit_upload_kib,omitempty"`
	// PrunedByController: the controller removes old backups (SPEC.md
	// §10.10); this server may only add them, so it only checks.
	PrunedByController bool `yaml:"pruned_by_controller,omitempty"`
	// Copies are extra copies somewhere else (SPEC.md §6.9, 3-2-1).
	Copies []BackupCopy `yaml:"copies,omitempty"`
	// Drive is the drive the repository is on, when it's one (a USB drive
	// or second disk): backups check it's connected before writing.
	Drive BackupDrive `yaml:"drive,omitempty"`
}

// BackupDrive is a drive backups go on.
type BackupDrive struct {
	// Mount is where it's mounted; UUID its filesystem's (empty for this
	// server's own disk or a network share); Name how people know it.
	Mount string `yaml:"mount,omitempty"`
	UUID  string `yaml:"uuid,omitempty"`
	Name  string `yaml:"name,omitempty"`
}

// BackupCopy is an extra copy of this server's backups: each nightly
// backup also goes there.
type BackupCopy struct {
	// Name identifies it (the controller's reference to the destination);
	// Title is for people.
	Name  string `yaml:"name"`
	Title string `yaml:"title"`
	// Repository and Env reach it, like Backup's.
	Repository string   `yaml:"repository"`
	Env        []string `yaml:"env,omitempty"`
	// KeyFile holds this server's key to it; empty uses the main key file
	// (the controller's recovery key).
	KeyFile string `yaml:"key_file,omitempty"`
}

// SSH access choices (Firewall.SSH).
const (
	SSHFromLAN = "lan" // home network (and VPN) only
	SSHFromAny = "any" // anywhere, e.g. with port 22 forwarded
	SSHOff     = "off" // blocked
)

// Firewall configures the host firewall that roostack-agent applies.
type Firewall struct {
	// SSH is who may reach this server's SSH server. Empty means the
	// Owner hasn't chosen yet, and Roostack leaves the firewall alone.
	SSH string `yaml:"ssh,omitempty"`
}

// Restart choices (Maintenance.Restart).
const (
	RestartAuto   = "auto"   // restart in the window when updates need it (default)
	RestartNotify = "notify" // only tell the Owner
)

// Maintenance configures automatic restarts after OS updates.
type Maintenance struct {
	// Restart is RestartAuto (default when empty) or RestartNotify.
	Restart string `yaml:"restart,omitempty"`
	// RestartHour is when the restart window opens (0-23, local time).
	// Nil means the default, 4 (04:00).
	RestartHour *int `yaml:"restart_hour,omitempty"`
}

// Hour returns the restart window's start hour.
func (m Maintenance) Hour() int {
	if m.RestartHour == nil {
		return DefaultRestartHour
	}
	return *m.RestartHour
}

// DefaultRestartHour opens the restart window at 04:00, after the 02:00
// backup and OS updates (SPEC.md §3.2).
const DefaultRestartHour = 4

// Lab is the lab-wide configuration (/var/lib/roostack/lab.yaml).
type Lab struct {
	// Name is what people call the lab (e.g. "The Smiths' lab"): shown in
	// the UI, and to other labs it shares storage with.
	Name string `yaml:"name,omitempty"`
	// Domain is the base domain; services live on subdomains of it.
	Domain string `yaml:"domain"`
	// Email is used for the ACME (Let's Encrypt) account.
	Email string `yaml:"email"`
	DNS   DNS    `yaml:"dns"`
	Paths Paths  `yaml:"paths"`
	// UIPort is the host port of the roostack web UI.
	UIPort int `yaml:"ui_port"`
	// UIBind is the address the UI listens on. Only change it for
	// development (e.g. 0.0.0.0 to reach it directly in a VM).
	UIBind string `yaml:"ui_bind"`
	Backup Backup `yaml:"backup"`
	// Firewall is the host firewall (SPEC.md §6.10).
	Firewall Firewall `yaml:"firewall,omitempty"`
	// Maintenance is when the host may restart (SPEC.md §3.2).
	Maintenance Maintenance `yaml:"maintenance,omitempty"`
	// NodeID is this server's node ID (internal/node), set at runtime.
	// Services are placed on nodes (SPEC.md §10.4).
	NodeID string `yaml:"-"`
	// Timezone is the host's time zone (e.g. "Europe/Berlin"), detected at
	// runtime so containers use it too. Empty leaves them on UTC.
	Timezone string `yaml:"-"`
	// HasGPU is detected at runtime, not configured. It resolves settings
	// with `default: auto` (docs/ASSUMPTIONS.md G1).
	HasGPU bool `yaml:"-"`
	// Role is this server's part in the lab (SPEC.md §10.1), set at
	// runtime from its membership (internal/labnet).
	Role string `yaml:"-"`
	// OverlayAddr is this server's address on Roostack's network, "" until
	// the lab has a network. A node publishes its web services there, for
	// the controller's Caddy.
	OverlayAddr string `yaml:"-"`
}

// Roles (SPEC.md §10.1). A lab with one server has a controller only.
const (
	RoleController = "controller"
	RoleNode       = "node"
)

// IsNode reports whether this server is a node run by another server
// (the controller): it runs services but no UI, Caddy or DNS.
func (l Lab) IsNode() bool { return l.Role == RoleNode }

// DisplayName is the lab's name, or its domain for a lab that has none
// (named before names existed).
func (l Lab) DisplayName() string {
	if l.Name != "" {
		return l.Name
	}
	if l.Domain != "" && l.Domain != "lab.example.com" {
		return l.Domain
	}
	return "My lab"
}

// Default returns a Lab with default paths and no domain.
func Default() Lab {
	return Lab{UIPort: DefaultUIPort, UIBind: DefaultUIBind, Paths: Paths{
		StateDir:    DefaultStateDir,
		HomeDir:     DefaultHomeDir,
		ServicesDir: DefaultServicesDir,
		MediaDir:    DefaultMediaDir,
		CatalogDir:  DefaultCatalogDir,
	}}
}

// Load reads a lab config file. Unset paths keep their defaults.
func Load(path string) (Lab, error) {
	lab := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return lab, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&lab); err != nil {
		return lab, fmt.Errorf("%s: %w", path, err)
	}
	return lab, lab.Validate()
}

// Save writes the lab config atomically (mode 0640: it can hold backup
// credentials).
func (l Lab) Save(path string) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(l); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Update re-reads the config at path, applies change, and saves it. Callers
// change only their own fields, so concurrent edits of different settings
// don't overwrite each other.
func Update(path string, change func(*Lab)) error {
	lab, err := Load(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// An incomplete config (e.g. no domain yet) is still updatable.
		var perr *os.PathError
		if errors.As(err, &perr) {
			return err
		}
	}
	change(&lab)
	return lab.Save(path)
}

// Validate checks the config is usable.
func (l Lab) Validate() error {
	var errs []error
	if r := l.Maintenance.Restart; r != "" && r != RestartAuto && r != RestartNotify {
		errs = append(errs, fmt.Errorf("maintenance.restart must be %q or %q", RestartAuto, RestartNotify))
	}
	if h := l.Maintenance.Hour(); h < 0 || h > 23 {
		errs = append(errs, errors.New("maintenance.restart_hour must be 0-23"))
	}
	switch l.Firewall.SSH {
	case "", SSHFromLAN, SSHFromAny, SSHOff:
	default:
		errs = append(errs, fmt.Errorf("firewall.ssh must be %q, %q or %q", SSHFromLAN, SSHFromAny, SSHOff))
	}
	if l.Domain == "" {
		errs = append(errs, errors.New("domain is required"))
	}
	for _, p := range []struct{ name, path string }{
		{"state_dir", l.Paths.StateDir}, {"home_dir", l.Paths.HomeDir},
		{"services_dir", l.Paths.ServicesDir}, {"media_dir", l.Paths.MediaDir},
		{"catalog_dir", l.Paths.CatalogDir},
	} {
		if !filepath.IsAbs(p.path) {
			errs = append(errs, fmt.Errorf("paths.%s must be absolute, got %q", p.name, p.path))
		}
	}
	return errors.Join(errs...)
}

// DetectGPU reports whether the host has a GPU render device (Intel iGPU
// on the reference hardware). It resolves settings with `default: auto`.
func DetectGPU() bool {
	matches, _ := filepath.Glob("/dev/dri/renderD*")
	return len(matches) > 0
}
