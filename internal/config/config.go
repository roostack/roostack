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
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/fsutil"
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
	// SignInPort is where the sign-in page for services listens on the
	// host's loopback, beside the UI (single sign-on, SPEC.md §5.2): a
	// listener of its own, so nothing reaching it can reach Roostack's own
	// pages. Caddy reaches it like the UI.
	SignInPort = 8482
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
	// ImportRoot holds a folder per service being brought data, where
	// the person's copy is mounted read-only (SPEC.md §8.4).
	ImportRoot = "/mnt/roostack-import"
)

// LibraryView is a service's folder of the media libraries it may read.
func LibraryView(service string) string { return filepath.Join(LibraryViews, service) }

// LibraryAtView is a service's second view folder, for libraries shown at
// their old paths' shared parent (SPEC.md §8.4), each under its old
// folder's name. A service's name has no ".", so it's never another's.
func LibraryAtView(service string) string { return filepath.Join(LibraryViews, service+".at") }

// UserUnitDir is where the roostack user's own systemd units live: the
// slices Roostack writes (generate/slices.go).
func (p Paths) UserUnitDir() string {
	return filepath.Join(p.HomeDir, ".config", "systemd", "user")
}

// QuadletDir is where rootless Quadlet files for the roostack user live.
func (p Paths) QuadletDir() string {
	return filepath.Join(p.HomeDir, ".config", "containers", "systemd")
}

// LabFile is the lab-wide config file.
func (p Paths) LabFile() string { return filepath.Join(p.StateDir, "lab.yaml") }

// DatabaseFile is Roostack's SQLite database.
func (p Paths) DatabaseFile() string { return filepath.Join(p.StateDir, "roostack.db") }

// LabDNSNames is the names file for Roostack's own DNS server (SPEC.md
// §6.5.3), on the server that runs it; roostack-dns.path starts the DNS
// server while it's there (labdns.NamesFile, the default).
func (p Paths) LabDNSNames() string { return filepath.Join(p.StateDir, "dns", "names.json") }

// SetupTokenFile holds the first-run setup code (0600), deleted once the
// Owner exists (docs/ASSUMPTIONS.md K5).
func (p Paths) SetupTokenFile() string { return filepath.Join(p.StateDir, "setup-token") }

// ServiceDataDirs are where a service keeps its data: its folder in
// ServicesDir, then its folder in MediaDir. The order matters: the lab API
// names a folder by its place in this list.
func (p Paths) ServiceDataDirs(name string) []string {
	return []string{filepath.Join(p.ServicesDir, name), filepath.Join(p.MediaDir, name)}
}

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

// SSODir holds single sign-on's files (SPEC.md §5.2): the signing key and
// the sign-in page's logo. Under the state folder, so they're backed up.
func (p Paths) SSODir() string { return filepath.Join(p.StateDir, "sso") }

// PhotosDir holds a folder per service photos are being migrated into
// (SPEC.md §8.5): immich-go's log of each run, which Roostack reads.
func (p Paths) PhotosDir() string { return filepath.Join(p.StateDir, "photos") }

// SigningKeyFile is the key ID tokens are signed with (0600).
func (p Paths) SigningKeyFile() string { return filepath.Join(p.SSODir(), "signing-key.pem") }

// MailPasswordFile holds the SMTP server's password (0600).
func (p Paths) MailPasswordFile() string { return filepath.Join(p.StateDir, "mail-password") }

// InstancesDir holds one instance YAML file per installed service.
func (p Paths) InstancesDir() string { return filepath.Join(p.StateDir, "services") }

// CaddyDir holds the generated Caddyfile and per-service site snippets
// (mounted read-only at /etc/caddy).
func (p Paths) CaddyDir() string { return filepath.Join(p.StateDir, "caddy") }

// CaddySitesDir holds one snippet per service.
func (p Paths) CaddySitesDir() string { return filepath.Join(p.CaddyDir(), "sites") }

// DomainPageDir holds the bare domain's simple page, which Roostack
// writes and Caddy serves (SPEC.md §5.3).
func (p Paths) DomainPageDir() string { return filepath.Join(p.CaddyDir(), "domain") }

// StatusPageDir holds the public status page Roostack writes every minute
// (SPEC.md §6.15), which Caddy serves at status.<domain>.
func (p Paths) StatusPageDir() string { return filepath.Join(p.CaddyDir(), "status") }

// CaddyDataDir holds certificates and the ACME account.
func (p Paths) CaddyDataDir() string { return filepath.Join(p.StateDir, "caddy-data") }

// CaddyLocalRoot is the root certificate of Caddy's own certificate
// authority, which signs a test address's certificates (SPEC.md §6.6): in
// Caddy's data, where the Caddy image keeps it (/data/caddy/pki).
func (p Paths) CaddyLocalRoot() string {
	return filepath.Join(p.CaddyDataDir(), "caddy", "pki", "authorities", "local", "root.crt")
}

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
	// Pause is public access the Owner or an Admin paused for the whole
	// lab, or for every service built with something (SPEC.md §6.16).
	// One service's own pause is in its instance (PublicPause).
	Pause PublicPause `yaml:"pause,omitempty"`
	// Advisories is what Roostack does when the service registry warns
	// of a security problem in a public service (SPEC.md §6.16): tell,
	// exploited (the default, when empty) or critical (AdvisoriesTell…).
	Advisories string `yaml:"advisories,omitempty"`
	// Refused are the services whose public addresses Roostack Cloud
	// won't carry through the lab's roost.ac name or its relay, by name,
	// each with why (a cloudapi name state, or RefusedChecking): they
	// stay private, as if the Owner hadn't made them public (SPEC.md
	// §6.11, "Roostack Cloud's names and relay"). Worked out by the
	// controller from Cloud's last answer for each assignment, and sent
	// to every server, never saved.
	Refused map[string]string `yaml:"-" json:"refused,omitempty"`
}

// RefusedChecking is a service in Public.Refused that waits for Roostack
// Cloud's answer: one it may refuse, kept private until it's said.
const RefusedChecking = "checking"

// What Roostack does about the service registry's security advisories
// (Public.Advisories, SPEC.md §6.16). Only critical ones (usable by anyone
// on the internet without signing in) ever pause a service by themselves.
const (
	// AdvisoriesTell: tell the Owner, and pause nothing by itself.
	AdvisoriesTell = "tell"
	// AdvisoriesExploited: pause a public service a critical advisory
	// affects when it's being used in attacks (the default).
	AdvisoriesExploited = "exploited"
	// AdvisoriesCritical: pause it for any critical advisory.
	AdvisoriesCritical = "critical"
)

// AdvisoryPolicy is Advisories, AdvisoriesExploited when unset.
func (p Public) AdvisoryPolicy() string {
	if p.Advisories == "" {
		return AdvisoriesExploited
	}
	return p.Advisories
}

// PublicPause is public access paused for the whole lab (All), or for
// every service built with a name in BuiltWith, now and installed later
// (SPEC.md §6.16). A paused service's public addresses answer visitors
// from outside with a page saying it's closed for a while, and its public
// ports take connections from home only; everything else stays as it is.
// It lasts until someone lifts it: a pause lifting by itself could reopen
// a vulnerable service while nobody's looking.
type PublicPause struct {
	All       *Pause           `yaml:"all,omitempty" json:"all,omitempty"`
	BuiltWith map[string]Pause `yaml:"built_with,omitempty" json:"built_with,omitempty"`
	// Closed: public access is closed completely, for a security problem
	// in Caddy or Roostack itself, which a pause's page relies on (SPEC.md
	// §6.16): every server's firewall refuses connections to its web ports
	// from outside the home network and the lab's VPN, the lab asks
	// Roostack Cloud's relay to close its edges, and everything public is
	// paused too.
	Closed *Closed `yaml:"closed,omitempty" json:"closed,omitempty"`
	// Advised are the services Roostack paused because of a security
	// advisory, by name, each with the advisory's ID: worked out by the
	// controller from the registry's index for each assignment, and
	// sent to every server, never saved (SPEC.md §6.16).
	Advised map[string]string `yaml:"-" json:"advised,omitempty"`
}

// Pause is who paused public access, and when.
type Pause struct {
	Since time.Time `yaml:"since" json:"since"`
	// By is the person's username; empty from the command line.
	By string `yaml:"by,omitempty" json:"by,omitempty"`
}

// Closed is who closed public access completely, and when. Advisory is
// the security advisory about Roostack it was closed for, if any: it
// then opens again by itself once the advisory no longer affects the
// Roostack the lab runs (updated, or the advisory changed).
type Closed struct {
	Pause    `yaml:",inline"`
	Advisory string `yaml:"advisory,omitempty" json:"advisory,omitempty"`
}

// Covers says whether a service built with builtWith
// (pkgspec.Package.BuiltWithNames) is paused.
func (p PublicPause) Covers(builtWith []string) bool {
	if p.All != nil || p.Closed != nil {
		return true
	}
	for _, b := range builtWith {
		if _, ok := p.BuiltWith[b]; ok {
			return true
		}
	}
	return false
}

// IsZero: nothing is paused (and yaml's omitempty leaves it out).
func (p PublicPause) IsZero() bool {
	return p.All == nil && len(p.BuiltWith) == 0 && len(p.Advised) == 0 && p.Closed == nil
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
	// Monitoring is how the lab's monitoring is shown (SPEC.md §6.15).
	Monitoring Monitoring `yaml:"monitoring,omitempty"`
	// VPN is the lab's VPN for people's own devices (SPEC.md §10.11).
	VPN VPN `yaml:"vpn,omitempty"`
	// DomainItself is what the bare domain shows (SPEC.md §5.3).
	DomainItself DomainItself `yaml:"domain_itself,omitempty"`
	// Resolver is the DNS server the lab's devices, and the servers that
	// choose it, look names up with (SPEC.md §6.5.2).
	Resolver Resolver `yaml:"resolver,omitempty"`
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
	// HomeAddress is where the lab's names point for the home network's
	// devices (the controller's home address), for a DNS server among
	// the lab's services to answer them with (SPEC.md §6.5.2); "" on a
	// lab on the internet. Set at run time by the controller.
	HomeAddress string `yaml:"-"`
	// LabDNSHere: Roostack's own DNS server runs on this server (SPEC.md
	// §6.5.3), so the firewall sends the home network's and the VPN
	// devices' port 53 to it. Set at run time from the assignment.
	LabDNSHere bool `yaml:"-"`
	// Devices are the lab's VPN devices as the controller works them out
	// for each apply (SPEC.md §10.11): never in lab.yaml.
	Devices Devices `yaml:"-"`
	// GlancesHere: this server runs Glances for its live view, listening
	// at GlancesBind; GlancesSites are every server's live views, which
	// the controller's Caddy serves (SPEC.md §6.15.1). From the
	// assignment, never in lab.yaml.
	GlancesHere bool `yaml:"-"`
	// SharedLibraries are the folders services on this server offer as
	// media libraries (pkgspec.Storage.Shared): the library's name → its
	// folder. Worked out for each apply from the services here (state).
	SharedLibraries map[string]string `yaml:"-"`
	GlancesBind     string            `yaml:"-"`
	GlancesSites    []GlancesSite     `yaml:"-"`
	// MemoryTotal is this server's memory in bytes, for the services'
	// memory limits (generate/slices.go); 0 where it isn't known, for no
	// limits. Read by the server for each apply, never in lab.yaml.
	MemoryTotal int64 `yaml:"-"`
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

// Monitoring holds monitoring's lab settings (SPEC.md §6.15); the rest
// (switching it off, what the status page shows) is runtime data.
type Monitoring struct {
	// StatusPage serves the public status page at status.<domain>.
	StatusPage bool `yaml:"status_page,omitempty"`
	// Glances are the servers (by ID) where an Owner or Admin turned on a
	// live view with Glances (SPEC.md §6.15.1).
	Glances []string `yaml:"glances,omitempty"`
}

// GlancesSite is a server's live view, as the controller's Caddy serves
// it: at Host, from Upstream (as Caddy reaches it), behind single sign-on
// as Client, for Owners and Admins.
type GlancesSite struct {
	Server   string `json:"server"`
	Host     string `json:"host"`
	Upstream string `json:"upstream"`
	Client   string `json:"client"`
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

// ProviderTestAddress is the DNS provider of a test address (SPEC.md
// §6.6, "A test address for throwaway labs"): a name under .localhost
// that only `roostack test-address` sets, served with Caddy's own
// certificates. It has no credentials and no records.
const ProviderTestAddress = "test-address"

// TestAddressSuffix ends every test address. Names under .localhost always
// mean the computer itself (RFC 6761), so no DNS server on the internet
// answers for them and no public certificate authority signs them: a lab
// on one can only be reached from its own server.
const TestAddressSuffix = ".localhost"

// TestAddress reports whether the lab's address is a test address.
func (l Lab) TestAddress() bool { return l.DNS.Provider == ProviderTestAddress }

// LocalhostName reports whether name is localhost or a name under it.
func LocalhostName(name string) bool {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	return name == "localhost" || strings.HasSuffix(name, TestAddressSuffix)
}

// UIHost is where Roostack's own pages are: roost.<domain>, for a
// roost.ac name too since 2026-11-11 (SPEC.md §5.3).
func (l Lab) UIHost() string { return "roost." + l.Domain }

// DefaultMaxDevices is how many devices each person may add to the lab's
// VPN unless the Owner chooses otherwise (SPEC.md §10.11).
const DefaultMaxDevices = 5

// VPN is the lab's VPN: people's own devices joining the lab's network
// from anywhere, each reaching only what its person may use (SPEC.md
// §10.11).
type VPN struct {
	On bool `yaml:"on,omitempty"`
	// Server is the server devices connect to (its ID); empty lets
	// Roostack choose (Lab.VPNServer).
	Server string `yaml:"server,omitempty"`
	// MaxDevices is each person's limit (0: DefaultMaxDevices), unless
	// NoLimit.
	MaxDevices int  `yaml:"max_devices,omitempty"`
	NoLimit    bool `yaml:"no_limit,omitempty"`
	// HomeNetwork lets devices reach the home network's other devices,
	// and Internet lets them send all their internet traffic through the
	// lab, if their person chooses either or both.
	HomeNetwork bool `yaml:"home_network,omitempty"`
	Internet    bool `yaml:"internet,omitempty"`
}

// Limit is how many devices each person may add (0: no limit).
func (v VPN) Limit() int {
	switch {
	case v.NoLimit:
		return 0
	case v.MaxDevices > 0:
		return v.MaxDevices
	}
	return DefaultMaxDevices
}

// Devices are the VPN's devices' addresses for what Caddy and each
// server's firewall let them reach (SPEC.md §10.11).
type Devices struct {
	// Services are, by service name, the devices whose person may use it.
	Services map[string][]string `json:"services,omitempty"`
	// Pages reach Roostack's pages, SSH reaches SSH, and All is every
	// device (Your account, my.<domain>, is open to all).
	Pages []string `json:"pages,omitempty"`
	SSH   []string `json:"ssh,omitempty"`
	All   []string `json:"all,omitempty"`
	// Home and Everything are devices whose traffic goes on to the home
	// network, or anywhere, from the server they connect to.
	Home     []string `json:"home,omitempty"`
	Internet []string `json:"internet,omitempty"`
	// Elsewhere: devices connect to a server that isn't the controller,
	// which passes their traffic on to it over Roostack's network to
	// PassOn: the lab's address and the home's networks (SPEC.md §10.11).
	Elsewhere bool     `json:"elsewhere,omitempty"`
	PassOn    []string `json:"pass_on,omitempty"`
}

// Resolver is which DNS server the lab's devices use, through its VPN,
// and the servers that choose it use for their own lookups (SPEC.md
// §6.5.2).
type Resolver struct {
	// Use is one of the Resolver constants; "" is ResolverAuto.
	Use string `yaml:"use,omitempty"`
	// Address is the server's address (ResolverAddress).
	Address string `yaml:"address,omitempty"`
	// Servers are the lab's servers, by ID, whose own lookups use it too.
	Servers []string `yaml:"servers,omitempty"`
	// Server is the server at home that runs Roostack's own DNS server
	// on a lab whose controller is in the cloud (SPEC.md §6.5.3), by ID;
	// "" is the first at home by name.
	Server string `yaml:"server,omitempty"`
	// Fallback is the DNS server Roostack's own passes questions to when
	// the server's network gives none it can use; "" is Quad9's.
	Fallback string `yaml:"fallback,omitempty"`
}

// Which DNS server the lab uses (Resolver.Use).
const (
	// ResolverAuto: a DNS server among the lab's services (Pi-hole), once
	// there is one, else Roostack's own.
	ResolverAuto = "auto"
	// ResolverRoostack: Roostack's own (SPEC.md §6.5.3), even with a DNS
	// service installed.
	ResolverRoostack = "roostack"
	// ResolverProvider: whatever each device and server has already,
	// usually the internet provider's, through the router.
	ResolverProvider = "provider"
	// ResolverAddress: one at an address the Owner typed.
	ResolverAddress = "address"
)

// What the bare domain shows (SPEC.md §5.3, DomainItself.Show).
const (
	ShowNothing = "nothing" // Roostack leaves it alone
	ShowAccount = "account" // a redirect to my.<domain>
	ShowService = "service" // a redirect to one of the lab's services
	ShowAddress = "address" // a redirect to any web address
	ShowPage    = "page"    // a simple page Roostack writes
)

// DomainItself is what someone gets at the bare domain (smith.family),
// the Owner's choice (SPEC.md §5.3).
type DomainItself struct {
	// Show is one of the Show constants; empty is the default for the
	// address (Lab.Shows).
	Show string `yaml:"show,omitempty"`
	// Service is the service it redirects to (ShowService), Address the
	// web address (ShowAddress).
	Service string `yaml:"service,omitempty"`
	Address string `yaml:"address,omitempty"`
	// Public: anyone reaches it, not only the home network.
	Public bool `yaml:"public,omitempty"`
	// Page is the simple page's words and links (ShowPage).
	Page SimplePage `yaml:"page,omitempty"`
}

// SimplePage is the bare domain's simple page: a heading, a few lines
// of text, and links to the services the Owner picked, in the lab's look.
type SimplePage struct {
	Heading  string   `yaml:"heading,omitempty"`
	Text     string   `yaml:"text,omitempty"`
	Services []string `yaml:"services,omitempty"`
}

// Limits on the simple page's words.
const (
	PageHeadingMax = 100
	PageTextMax    = 2000
)

// Shows is what the bare domain shows: the Owner's choice, or by default
// nothing on an own domain (its website stays where it is) and Your
// account on a roost.ac name; nothing without an address.
func (l Lab) Shows() string {
	switch {
	case !l.HasAddress():
		return ShowNothing
	case l.DomainItself.Show != "":
		return l.DomainItself.Show
	case l.RoostAc():
		return ShowAccount
	}
	return ShowNothing
}

// ServesDomain reports whether Caddy serves the bare domain, which then
// needs a DNS record pointing at the lab.
func (l Lab) ServesDomain() bool { return l.Shows() != ShowNothing }

// DomainPublic reports whether the bare domain is served to anyone.
func (l Lab) DomainPublic() bool { return l.ServesDomain() && l.DomainItself.Public }

// SignInHost is the sign-in page for services, my.<domain> (single
// sign-on, SPEC.md §5.2).
func (l Lab) SignInHost() string { return "my." + l.Domain }

// Validate checks the choice is whole: a service or an address to go
// to, and words that fit.
func (d DomainItself) Validate() error {
	switch d.Show {
	case "", ShowNothing, ShowAccount, ShowPage:
	case ShowService:
		if d.Service == "" {
			return errors.New("show: service needs a service")
		}
	case ShowAddress:
		if u, err := url.Parse(d.Address); err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" ||
			strings.ContainsAny(d.Address, " \t\n\"{}") {
			return fmt.Errorf("address must be an http or https address, got %q", d.Address)
		}
	default:
		return fmt.Errorf("show must be %q, %q, %q, %q, or %q, got %q", ShowNothing, ShowAccount, ShowService, ShowAddress, ShowPage, d.Show)
	}
	if utf8.RuneCountInString(d.Page.Heading) > PageHeadingMax || utf8.RuneCountInString(d.Page.Text) > PageTextMax {
		return errors.New("the page's heading or text is too long")
	}
	return nil
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
// it's called: the setup wizard and Settings → Name and look change it while
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
	return fsutil.WriteFile(path, buf.Bytes(), 0o640)
}

// updateMu serialises Update, so two settings changed at the same moment
// can't each read the old file and save over the other's change.
var updateMu sync.Mutex

// Update re-reads the config at path, applies change, and saves it, holding
// a lock for the whole read-change-save. Callers change only their own
// fields, so with the lock concurrent edits of different settings don't
// overwrite each other (everything that edits lab.yaml runs in the one
// roostack process).
func Update(path string, change func(*Lab)) error {
	updateMu.Lock()
	defer updateMu.Unlock()
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
		errs = append(errs, fmt.Errorf("firewall.ssh must be %q, %q, or %q", SSHFromLAN, SSHFromAny, SSHOff))
	}
	switch l.Firewall.SSHLogin {
	case "", SSHLoginKey, SSHLoginPassword:
	default:
		errs = append(errs, fmt.Errorf("firewall.ssh_login must be %q or %q", SSHLoginKey, SSHLoginPassword))
	}
	if err := l.DomainItself.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("domain_itself: %w", err))
	}
	// A test address is only ever a name under .localhost, and such a
	// name only ever a test address, so Caddy's own certificates can't
	// take the place of real ones on a real address (SPEC.md §6.6).
	switch local := LocalhostName(l.Domain); {
	case l.TestAddress() && !local:
		errs = append(errs, fmt.Errorf("dns.provider %q is only for a domain under %s, got %q", ProviderTestAddress, TestAddressSuffix, l.Domain))
	case local && !l.TestAddress():
		errs = append(errs, fmt.Errorf("a domain under %s is a test address, set with `roostack test-address`, got %q with dns.provider %q",
			TestAddressSuffix, l.Domain, l.DNS.Provider))
	}
	if o := l.OldAddress; o != nil && (LocalhostName(o.Domain) || o.DNS.Provider == ProviderTestAddress) {
		errs = append(errs, errors.New("old_address can't be a test address"))
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
