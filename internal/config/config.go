// Package config holds lab-wide settings and the central default paths.
//
// All default locations live in this file so they can be changed in one
// place (SPEC.md §6.7).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"
)

// Default paths. See SPEC.md §6.7 and docs/ASSUMPTIONS.md (A2).
const (
	DefaultStateDir    = "/var/lib/roostack"
	DefaultHomeDir     = "/var/lib/roostack/home"
	DefaultServicesDir = "/srv/roostack/services"
	DefaultMediaDir    = "/srv/roostack/media"
	// DefaultCatalogDir holds the packages that can be installed: the
	// controller's copy of the registry, fetched and checked against its
	// signed index (SPEC.md §8.3.3). It can always be fetched again, so
	// backups leave it out.
	DefaultCatalogDir = "/var/lib/roostack/catalog"
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
	// CrowdSecPort is where CrowdSec's local API listens on the host's
	// loopback while anything is public, for Caddy's bouncer (SPEC.md
	// §6.11).
	CrowdSecPort = 8473
	// AppSecPort is where CrowdSec's AppSec component (its web
	// application firewall) listens on the host's loopback, beside its
	// local API, for Caddy's bouncer (SPEC.md §6.10 item 4).
	AppSecPort = 8475
	// SetupPort is where the first-run setup pages are served to the home
	// network over HTTPS until setup is done (SPEC.md §6.3.2).
	SetupPort = 8443
)

// Paths are the filesystem locations Roostack reads and writes.
type Paths struct {
	StateDir    string `yaml:"state_dir"`    // desired state, SQLite, secrets metadata
	HomeDir     string `yaml:"home_dir"`     // home of the roostack user
	ServicesDir string `yaml:"services_dir"` // per-service data (bind mounts)
	MediaDir    string `yaml:"media_dir"`    // large data, e.g. a separate disk
	CatalogDir  string `yaml:"catalog_dir"`  // packages: <name>/<version>.yaml
}

// Media libraries' places (SPEC.md §8.1, "Media libraries"): fixed, and
// root's, not under the configurable paths (the roostack user owns those
// and could swap a mount point for a symlink). LibraryRoot holds a mount
// point per library; LibraryViews a folder per service with the libraries
// it may read, which its containers bind.
const (
	LibraryRoot  = "/mnt/roostack-library"
	LibraryViews = "/mnt/roostack-library-views"
)

// LibraryView is a service's folder of the media libraries it may read.
func LibraryView(service string) string { return filepath.Join(LibraryViews, service) }

// QuadletDir is where rootless Quadlet files for the roostack user live.
func (p Paths) QuadletDir() string {
	return filepath.Join(p.HomeDir, ".config", "containers", "systemd")
}

// LabFile is the lab-wide config file.
func (p Paths) LabFile() string { return filepath.Join(p.StateDir, "lab.yaml") }

// CatalogPackageDir is a package's folder in the catalog, laid out as in
// the registry: <version>.yaml and icon.png (SPEC.md §8.3.3). The catalog is only what can be installed; installed services
// use the lab's own copy (LabPackageFile).
func (p Paths) CatalogPackageDir(name string) string { return filepath.Join(p.CatalogDir, name) }

// CatalogPackageFile is one version of a package in the catalog.
func (p Paths) CatalogPackageFile(name, version string) string {
	return filepath.Join(p.CatalogPackageDir(name), version+".yaml")
}

// LocalPackagesDir holds the packages the Owner added themselves ("Your
// own", SPEC.md §8.3.4): <name>/<version>.yaml, one version each, and its
// logo. Under the state folder, so they're backed up with the lab: they
// can't be fetched again.
func (p Paths) LocalPackagesDir() string { return filepath.Join(p.StateDir, "local-packages") }

// LocalPackageDir is one of the Owner's own packages' folder.
func (p Paths) LocalPackageDir(name string) string { return filepath.Join(p.LocalPackagesDir(), name) }

// PackagesDir holds the lab's copy of the package file of every version
// its services use, so a restore needs nothing but the backup and a newer
// catalog never changes a running service (SPEC.md §8.3.1).
func (p Paths) PackagesDir() string { return filepath.Join(p.StateDir, "packages") }

// LabPackageFile is the lab's copy of one version of a package.
func (p Paths) LabPackageFile(name, version string) string {
	return filepath.Join(p.PackagesDir(), name, version+".yaml")
}

// BackupStoreDir is where this server keeps backups it stores for other
// servers (SPEC.md §10.10), next to the media folder on the big disk.
func (p Paths) BackupStoreDir() string { return filepath.Join(filepath.Dir(p.MediaDir), "backups") }

// MailPasswordFile holds the SMTP server's password (0600).
func (p Paths) MailPasswordFile() string { return filepath.Join(p.StateDir, "mail-password") }

// InstancesDir holds one instance YAML file per installed service.
func (p Paths) InstancesDir() string { return filepath.Join(p.StateDir, "services") }

// CaddyDir holds the generated Caddyfile and per-service site snippets
// (mounted read-only at /etc/caddy).
func (p Paths) CaddyDir() string { return filepath.Join(p.StateDir, "caddy") }

// CaddySitesDir holds one snippet per service.
func (p Paths) CaddySitesDir() string { return filepath.Join(p.CaddyDir(), "sites") }

// CaddyDataDir holds certificates and the ACME account.
func (p Paths) CaddyDataDir() string { return filepath.Join(p.StateDir, "caddy-data") }

// CaddyLogDir holds Caddy's access log while anything is public, which
// CrowdSec reads (SPEC.md §6.11).
func (p Paths) CaddyLogDir() string { return filepath.Join(p.StateDir, "caddy-logs") }

// CaddyBuildLog is the output of the latest build of Roostack's Caddy
// image. The roostack user's units log to the system journal (it's a
// system user), which Roostack can't read, so the build writes here and
// the UI shows it while it runs.
func (p Paths) CaddyBuildLog() string { return filepath.Join(p.StateDir, "caddy-build.log") }

// SetupCertFile and SetupKeyFile are the setup pages' self-signed
// certificate and its key (0600), made at Roostack's first start and
// deleted when setup is done (SPEC.md §6.3.2).
func (p Paths) SetupCertFile() string { return filepath.Join(p.StateDir, "setup-tls.crt") }
func (p Paths) SetupKeyFile() string  { return filepath.Join(p.StateDir, "setup-tls.key") }

// CrowdSecDir holds CrowdSec's configuration and database.
func (p Paths) CrowdSecDir() string { return filepath.Join(p.StateDir, "crowdsec") }

// DNS configures the DNS-01 challenge used for the wildcard certificate.
type DNS struct {
	// Provider is a provider ID from internal/dns (e.g. "cloudflare").
	Provider string `yaml:"provider"`
	// Fields lists which of the provider's credential fields are set. Their
	// values are Podman secrets, never stored here. Empty means the
	// provider's required fields.
	Fields []string `yaml:"fields,omitempty"`
	// Values are the provider's fields that aren't secret (dns.Field's
	// InConfig: Roostack Cloud's challenge server, username, subdomain).
	Values map[string]string `yaml:"values,omitempty"`
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

// Public configures public access (SPEC.md §6.11).
type Public struct {
	// SharingAsked: the Owner has answered whether CrowdSec shares attack
	// reports with its community (asked when something first goes public).
	SharingAsked bool `yaml:"sharing_asked,omitempty"`
	// NoSharing: the Owner chose not to share them (SPEC.md §6.10). Local
	// detection and blocking still work; the community blocklist doesn't.
	NoSharing bool `yaml:"no_sharing,omitempty"`
	// UITunnelOnly keeps Roostack's own pages off roost.<domain> on a
	// lab on the internet, where they're public by default (SPEC.md
	// §6.3.2): they're then only reachable through an SSH tunnel.
	UITunnelOnly bool `yaml:"ui_tunnel_only,omitempty"`
	// Edge is the server (its ID) that public visitors come in through
	// (SPEC.md §10.5): a cloud server in the lab, passing their connections
	// on still encrypted. Empty is port forwarding at home (§6.11). One for
	// the whole lab for now; each public address will be able to choose
	// its own later (Route's entry, SPEC.md §10.5).
	Edge string `yaml:"edge,omitempty"`
	// Relay brings public visitors through Roostack Cloud's relay instead
	// (its DESIGN §5): for a lab whose address is a roost.ac name. Never
	// with Edge.
	Relay bool `yaml:"relay,omitempty"`
	// Countries limits public visitors by the country their address is in
	// (SPEC.md §10.5). Only Roostack Cloud's relay enforces it, at its
	// edges, before visitors reach the lab.
	Countries Countries `yaml:"countries,omitempty"`
}

// Countries lets in only visitors from Allow, or everyone but those from
// Block (ISO 3166 codes, such as AU; never both; neither is anyone).
// OpenUntil lets everyone in until then, for travel.
type Countries struct {
	Allow     []string  `yaml:"allow,omitempty"`
	Block     []string  `yaml:"block,omitempty"`
	OpenUntil time.Time `yaml:"open_until,omitempty"`
}

// Limited says whether any countries are kept out.
func (c Countries) Limited() bool { return len(c.Allow) > 0 || len(c.Block) > 0 }

// Open says whether everyone is let in for now, for travel.
func (c Countries) Open(now time.Time) bool { return c.Limited() && c.OpenUntil.After(now) }

// Where a lab is (Lab.Network, SPEC.md §6.12).
const (
	// NetworkHome: behind a home router. "Private" means the home network;
	// public services need port forwarding.
	NetworkHome = "home"
	// NetworkInternet: the server has a public address of its own (a
	// cloud server). "Private" means only through an SSH tunnel.
	NetworkInternet = "internet"
)

// Backup configures the backup destination (SPEC.md §6.9).
type Backup struct {
	// Repository is a restic repository: a local path (e.g. a second disk)
	// or a remote such as s3:... or sftp:... Empty disables backups.
	Repository string `yaml:"repository"`
	// Env holds backend credentials as KEY=value (e.g. for S3).
	Env []string `yaml:"env,omitempty"`
	// Cloud: the repository is in Roostack Cloud backup, reached with
	// short-lived keys fetched before each backup (internal/cloudkeys),
	// not Env.
	Cloud bool `yaml:"cloud,omitempty"`
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
	// Repository and Env (or Cloud) reach it, like Backup's.
	Repository string   `yaml:"repository"`
	Env        []string `yaml:"env,omitempty"`
	Cloud      bool     `yaml:"cloud,omitempty"`
	// KeyFile holds this server's key to it; empty uses the main key file
	// (the controller's recovery key).
	KeyFile string `yaml:"key_file,omitempty"`
	// Drive is the drive it's on, if it's one (backups check it's there).
	Drive BackupDrive `yaml:"drive,omitempty"`
}

// SSH access choices (Firewall.SSH).
const (
	SSHFromLAN = "lan" // home network (and VPN) only
	SSHFromAny = "any" // anywhere, e.g. with port 22 forwarded
	SSHOff     = "off" // blocked
)

// How people log in over SSH (Firewall.SSHLogin; SPEC.md §6.10 item 3).
const (
	SSHLoginKey      = "key"      // keys only, no root logins
	SSHLoginPassword = "password" // sshd's own settings (passwords allowed)
)

// Firewall configures the host firewall that roostack-agent applies.
type Firewall struct {
	// SSH is who may reach this server's SSH server. Empty means the
	// Owner hasn't chosen yet, and Roostack leaves the firewall alone.
	SSH string `yaml:"ssh,omitempty"`
	// SSHLogin is how people log in over SSH: SSHLoginKey writes Roostack's
	// drop-in for sshd. Empty means not chosen yet (or no SSH server).
	SSHLogin string `yaml:"ssh_login,omitempty"`
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
	// Domain is the lab's address: services live on subdomains of it. Empty
	// until the lab has one: a roost.ac name or its own domain (SPEC.md
	// §6.6).
	Domain string `yaml:"domain,omitempty"`
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
	// Public is how services are made public (SPEC.md §6.11).
	Public Public `yaml:"public,omitempty"`
	// Mail is the SMTP server Roostack sends email through (SPEC.md §6.13).
	Mail Mail `yaml:"mail,omitempty"`
	// Cloud is Roostack Cloud (SPEC.md §6.6).
	Cloud Cloud `yaml:"cloud,omitempty"`
	// Registry is where the controller fetches packages from (SPEC.md
	// §8.3.3).
	Registry Registry `yaml:"registry,omitempty"`
	// OldAddress is the lab's previous address, still served for a while
	// after switching so bookmarks and apps can be updated (SPEC.md §6.6).
	OldAddress *OldAddress `yaml:"old_address,omitempty"`
	// Network is where the lab is: NetworkHome (the default when empty)
	// or NetworkInternet (SPEC.md §6.12). It's about the controller, whose
	// Caddy serves the lab's addresses.
	Network string `yaml:"network,omitempty"`
	// HostIP is the address this server reaches the internet from (its
	// LAN address at home, its own public address on a cloud server),
	// detected at runtime.
	HostIP string `yaml:"-"`
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
	// EdgeAddrs are the WireGuard addresses (as /32 ranges) of the lab's
	// edges, for the Caddy that serves the public names: they alone may
	// say who a visitor is (a PROXY header), and they count as outside.
	// Set at run time from the lab's servers.
	EdgeAddrs []string `yaml:"-"`
}

// Cloud is the lab's use of Roostack Cloud.
type Cloud struct {
	// Disabled turns off every Roostack Cloud feature: nothing is offered
	// and Roostack never contacts Cloud. Only in lab.yaml, not in Settings,
	// for people who want Roostack fully self-contained (SPEC.md §6.6).
	Disabled bool `yaml:"disabled,omitempty"`
	// URL is Cloud's address for labs (DefaultCloudURL when empty); a
	// test Cloud's otherwise.
	URL string `yaml:"url,omitempty"`
	// CAFile, for a test Cloud, is the certificate authority its HTTPS
	// certificate comes from (a real Cloud's is publicly trusted).
	CAFile string `yaml:"ca_file,omitempty"`
}

// Registry is the service registry the controller fetches its catalog
// from (SPEC.md §8.3.3).
type Registry struct {
	// URL is the registry's address (registry.DefaultURL when empty); a
	// mirror's otherwise. Anyone may mirror it: what's fetched is checked
	// against signatures, not trusted for where it came from.
	URL string `yaml:"url,omitempty"`
	// Disabled stops fetching (Settings → Updates): nothing new can be
	// installed from the registry, and the lab doesn't hear of
	// withdrawals.
	Disabled bool `yaml:"disabled,omitempty"`
	// NoStops is the Owner's opt-out from Roostack stopping a service
	// whose version the registry withdrew as dangerous (Settings →
	// Updates, SPEC.md §8.3.4): such a withdrawal is then shown as a
	// warning, and the service keeps running.
	NoStops bool `yaml:"no_stops,omitempty"`
}

// OldAddressDays is how long a lab's previous address keeps working after
// it switches to another (SPEC.md §6.6).
const OldAddressDays = 30

// OldAddress is a lab's previous address.
type OldAddress struct {
	Domain string `yaml:"domain"`
	// Until is when it stops working.
	Until time.Time `yaml:"until"`
	// DNS is its DNS provider, whose credentials are kept (as the Podman
	// secrets dns.OldSecretName) so its certificate can still renew.
	DNS DNS `yaml:"dns"`
}

// Old is the previous address while it still works, or nil.
func (l Lab) Old(now time.Time) *OldAddress {
	if l.OldAddress == nil || l.OldAddress.Domain == "" || l.OldAddress.Domain == l.Domain || !now.Before(l.OldAddress.Until) {
		return nil
	}
	return l.OldAddress
}

// DefaultCloudURL is Roostack Cloud's API, which labs talk to (its
// website, where people confirm links, is cloud.roostack.cloud; roost.ac
// holds only labs' names).
const DefaultCloudURL = "https://api.roostack.cloud"

// Address is Cloud's address for labs.
func (c Cloud) Address() string {
	if c.URL != "" {
		return c.URL
	}
	return DefaultCloudURL
}

// ProviderCloud is the DNS provider of a lab whose address is a roost.ac
// name: Roostack Cloud (SPEC.md §6.6).
const ProviderCloud = "roostack-cloud"

// RoostAc reports whether the lab's address is a roost.ac name from
// Roostack Cloud.
func (l Lab) RoostAc() bool { return l.DNS.Provider == ProviderCloud }

// UIHost is where Roostack's own pages are: the roost.ac name itself, or
// roost.<domain> on an own domain (SPEC.md §6.6).
func (l Lab) UIHost() string {
	if l.RoostAc() {
		return l.Domain
	}
	return "roost." + l.Domain
}

// HasAddress reports whether the lab has an address (a roost.ac name or
// its own domain): until then its services have no web addresses and
// Caddy doesn't run (SPEC.md §6.6).
func (l Lab) HasAddress() bool { return l.Domain != "" }

// Roles (SPEC.md §10.1). A lab with one server has a controller only.
const (
	RoleController = "controller"
	RoleNode       = "node"
)

// OnInternet reports whether the lab is on the internet (a cloud server)
// rather than behind a home router (SPEC.md §6.12).
func (l Lab) OnInternet() bool { return l.Network == NetworkInternet }

// PublicUI reports whether Roostack's own pages are public at
// roost.<domain>: on a lab on the internet, unless the Owner keeps them
// to SSH tunnels (SPEC.md §6.3.2).
func (l Lab) PublicUI() bool { return l.OnInternet() && !l.Public.UITunnelOnly }

// IsNode reports whether this server is a node run by another server
// (the controller): it runs services but no UI, Caddy or DNS.
func (l Lab) IsNode() bool { return l.Role == RoleNode }

// DisplayName is the lab's name, or its domain for a lab that has none
// (named before names existed).
func (l Lab) DisplayName() string {
	if l.Name != "" {
		return l.Name
	}
	if l.Domain != "" {
		return l.Domain
	}
	return "My lab"
}

// NameAt is the lab's name (DisplayName) as the config at path says when
// it's called: the setup wizard and Settings → Lab name change it while
// Roostack runs, the wizard without restarting. If the file can't be
// read, it's fallback's.
func NameAt(path string, fallback Lab) func() string {
	return func() string {
		lab, err := Load(path)
		if err != nil {
			return fallback.DisplayName()
		}
		return lab.DisplayName()
	}
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
	// A file of only comments (a new install's, SPEC.md §6.6) is all
	// defaults.
	if err := dec.Decode(&lab); err != nil && !errors.Is(err, io.EOF) {
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

// Mail security: how the connection to the SMTP server is encrypted.
const (
	MailSTARTTLS = "starttls" // port 587: plain, then upgraded (the default)
	MailTLS      = "tls"      // port 465: encrypted from the start
)

// Mail is the SMTP server Roostack sends email through (SPEC.md §6.13).
// Its password is kept apart, in Paths.MailPasswordFile (0600).
type Mail struct {
	Host     string `yaml:"host,omitempty"`
	Port     int    `yaml:"port,omitempty"`
	Security string `yaml:"security,omitempty"` // MailSTARTTLS or MailTLS
	Username string `yaml:"username,omitempty"`
	// From is the sender address, e.g. roostack@example.com.
	From string `yaml:"from,omitempty"`
}

// Configured reports whether email can be sent.
func (m Mail) Configured() bool { return m.Host != "" && m.From != "" }

// Validate checks the config is usable.
func (l Lab) Validate() error {
	var errs []error
	if r := l.Maintenance.Restart; r != "" && r != RestartAuto && r != RestartNotify {
		errs = append(errs, fmt.Errorf("maintenance.restart must be %q or %q", RestartAuto, RestartNotify))
	}
	if h := l.Maintenance.Hour(); h < 0 || h > 23 {
		errs = append(errs, errors.New("maintenance.restart_hour must be 0-23"))
	}
	switch l.Network {
	case "", NetworkHome, NetworkInternet:
	default:
		errs = append(errs, fmt.Errorf("network must be %q or %q", NetworkHome, NetworkInternet))
	}
	switch l.Mail.Security {
	case "", MailSTARTTLS, MailTLS:
	default:
		errs = append(errs, fmt.Errorf("mail.security must be %q or %q", MailSTARTTLS, MailTLS))
	}
	switch l.Firewall.SSH {
	case "", SSHFromLAN, SSHFromAny, SSHOff:
	default:
		errs = append(errs, fmt.Errorf("firewall.ssh must be %q, %q or %q", SSHFromLAN, SSHFromAny, SSHOff))
	}
	switch l.Firewall.SSHLogin {
	case "", SSHLoginKey, SSHLoginPassword:
	default:
		errs = append(errs, fmt.Errorf("firewall.ssh_login must be %q or %q", SSHLoginKey, SSHLoginPassword))
	}
	if u := l.Registry.URL; u != "" {
		if parsed, err := url.Parse(u); err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			errs = append(errs, fmt.Errorf("registry.url must be an http or https address, got %q", u))
		}
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
