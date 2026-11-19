// Package instance defines a user's installed service: their choices on top
// of a package (SPEC.md §8.1). Instances are desired state, stored as
// /var/lib/roostack/services/<name>.yaml.
package instance

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/fsutil"
	"roostack.cloud/roostack/internal/nodeid"
	"roostack.cloud/roostack/internal/pkgspec"
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
	// Name is the service's ID: its units, secrets, data folders, backups
	// and address in the UI are named after it, and it never changes. Empty
	// is the package's name (the first copy of a package). A second copy
	// has a name of its own, e.g. forgejo-work (SPEC.md §8.2).
	Name    string `yaml:"name,omitempty"`
	Package string `yaml:"package"`
	// Title is what people call it ("Forgejo (work)"); empty is the
	// package's title.
	Title string `yaml:"title,omitempty"`
	// Host is the node the service runs on (SPEC.md §10.4). Always set
	// explicitly (state.AssignHosts), so moving the controller can never
	// move services with it by accident.
	Host    string                   `yaml:"host,omitempty"`
	Version string                   `yaml:"version"`
	Routes  map[string]RouteOverride `yaml:"routes,omitempty"`
	// Subdomain, for a service with ports and no web address (RustDesk's
	// server), is the name its connections are at, under the lab's
	// address; empty is its ID (generate.PortHost).
	Subdomain string                  `yaml:"subdomain,omitempty"`
	Ports     map[string]PortOverride `yaml:"ports,omitempty"`
	Settings  map[string]any          `yaml:"settings,omitempty"`
	Updates   string                  `yaml:"updates"`
	// Waiting: its server (Host) is gone and it waits to be restored from
	// its backups, onto any server. It runs nowhere until then (SPEC.md
	// §10.10, "Restoring across the lab").
	Waiting bool `yaml:"waiting,omitempty"`
	// StartedAnyway is a version the registry withdrew as dangerous that
	// the Owner chose to run anyway (SPEC.md §8.3.4): Roostack doesn't
	// stop the service while it has this version. Another version (an
	// update) isn't covered.
	StartedAnyway string `yaml:"started_anyway,omitempty"`
	// Held keeps the service stopped after a restore (or a move through a
	// backup) that put its files in place but couldn't load its databases
	// (SPEC.md §6.9, "A restore that didn't finish"): only its databases
	// run, until the Owner chooses what to do. Nil when it isn't held.
	Held *Hold `yaml:"held,omitempty"`
	// DumpsPending: a lab restore put its files back from this backup, and
	// its databases' dumps among them still wait to be loaded (labrestore,
	// step 3). Only its databases run until they are, so the app never
	// starts on empty ones; while it's turned off, they wait until it's
	// turned on. Nil when nothing waits.
	DumpsPending *Restored `yaml:"dumps_pending,omitempty"`
	// Local: installed from a package the Owner added themselves, not one
	// from the service registry, so its pages say "Your own" (SPEC.md
	// §8.3.4). Recorded here because the lab's copy of the package is the
	// same whichever it came from, and the Owner may remove theirs.
	Local bool `yaml:"local,omitempty"`
	// NoWAF: the Owner turned off CrowdSec's AppSec (the web application
	// firewall) for this service's public addresses, after a warning
	// (SPEC.md §6.10 item 4). Only the Owner can: a package has no say.
	NoWAF bool `yaml:"no_waf,omitempty"`
	// PublicPause: the Owner or an Admin paused this service's public
	// access (SPEC.md §6.16): visitors from outside get a page saying it's
	// closed for a while, until someone lifts it. The lab's own pauses
	// (everything, or by what services are built with) are in lab.yaml.
	PublicPause *config.Pause `yaml:"public_pause,omitempty"`
	// Off: someone turned the service off for a while (SPEC.md §6.3.5):
	// nothing of it runs but its network, and its address answers with a
	// page saying it's turned off, until someone turns it on again. Its
	// data, settings, names and ports stay.
	Off *config.Pause `yaml:"off,omitempty"`
	// PublicAnyway are the security advisories the Owner chose to keep
	// this version public despite (SPEC.md §6.16): Roostack doesn't pause
	// it for them. Another version (an update) isn't covered.
	PublicAnyway *KeptPublic `yaml:"public_anyway,omitempty"`
	// SSO: single sign-on is on (SPEC.md §5.2), for a package that takes
	// it. Who may sign in is runtime data (store.Access).
	SSO bool `yaml:"sso,omitempty"`
	// Libraries are the media libraries of its server it may read, for a
	// package that reads them (SPEC.md §8.1, "Media libraries"): chosen at
	// install (all, by default) and in its settings.
	Libraries []string `yaml:"libraries,omitempty"`
	// LibraryPaths gives a library it reads a place of its own inside its
	// containers (SPEC.md §8.4, "Media libraries at their old paths"): the
	// path its old container had it at (films at /movies), so an app that
	// keeps every file's path in its database (Plex, Jellyfin) finds them
	// where it expects. Libraries without one are only in the package's
	// libraries folder (/media/<library>); every library is there too.
	LibraryPaths map[string]string `yaml:"library_paths,omitempty"`
	// AwaitingData: installed with "I'll bring its data" (SPEC.md §8.4),
	// so only its databases run, and its install hooks don't, until its
	// data is switched in.
	AwaitingData bool `yaml:"awaiting_data,omitempty"`
	// SettingUp: its install hooks haven't succeeded yet, so its addresses
	// and ports stay private whatever the Owner chose (SPEC.md §8.1,
	// "Public addresses wait for setup"): some apps belong to whoever
	// reaches them first until they're set up. Set by the controller when
	// it installs a package whose install hooks will run, and cleared when
	// the service's server says they've succeeded.
	SettingUp bool `yaml:"setting_up,omitempty"`
	// Env are the Owner's extra environment variables (SPEC.md §8.1,
	// "Extra environment variables"), for options the package doesn't
	// make settings: given to their containers after the package's own,
	// which they can't be.
	Env []EnvVar `yaml:"env,omitempty"`
}

// EnvVar is an extra environment variable (Instance.Env).
type EnvVar struct {
	Container string `yaml:"container"`
	Name      string `yaml:"name"`
	// Value is a plain variable's value. A secret's is a Podman secret
	// instead (EnvSecretRef), never in the instance file.
	Value  string `yaml:"value,omitempty"`
	Secret bool   `yaml:"secret,omitempty"`
	// Rev counts a secret's values: each new one is a Podman secret of
	// its own, so its container file changes and the container restarts
	// with it, and the one before is deleted.
	Rev int `yaml:"rev,omitempty"`
}

// The limits on a service's extra environment variables.
const (
	MaxEnv      = 100
	MaxEnvValue = 4096
)

// envNameRe is an environment variable's name, as apps document them.
var envNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,199}$`)

// ValidEnvName says whether name can be an environment variable's name.
func ValidEnvName(name string) bool { return envNameRe.MatchString(name) }

// EnvSecretRef is the secret reference (generate's secretName) of an extra
// variable that's a secret: env-<container>-<name>-<rev>.
func (e EnvVar) EnvSecretRef() string {
	return fmt.Sprintf("env-%s-%s-%d", e.Container, e.Name, e.Rev)
}

// EnvSecretPrefix starts the secret references of a service's extra
// variables, for finding the ones no longer used.
const EnvSecretPrefix = "env-"

// CheckEnvVar is why an extra variable can't be given to the package's
// container, or nil.
func CheckEnvVar(p *pkgspec.Package, e EnvVar) error {
	switch {
	case p.Containers[e.Container] == nil:
		return fmt.Errorf("%s has no container %q", p.Title, e.Container)
	case !ValidEnvName(e.Name):
		return fmt.Errorf("%q isn't a name an environment variable can have: letters, digits, and underscores, not starting with a digit", e.Name)
	case slices.Contains(p.ManagedEnv(e.Container), e.Name):
		return fmt.Errorf("%s's package sets %s itself, so it can't be added here", p.Title, e.Name)
	case e.Secret && e.Value != "":
		return fmt.Errorf("%s: a secret's value is a Podman secret, not in the instance file", e.Name)
	case strings.ContainsAny(e.Value, "\n\r\x00"):
		return fmt.Errorf("%s: a value can't have a line break in it", e.Name)
	case len(e.Value) > MaxEnvValue:
		return fmt.Errorf("%s: a value can be at most 4 KB", e.Name)
	case e.Rev < 0:
		return fmt.Errorf("%s: rev can't be below 0", e.Name)
	}
	return nil
}

// Hold is why a service is held stopped (Instance.Held).
type Hold struct {
	// Backup is the backup it was restored from, as the restore page
	// names it ("<repository>@<snapshot>"; "" for a move), and Taken when
	// that backup was made.
	Backup string    `yaml:"backup,omitempty"`
	Taken  time.Time `yaml:"taken"`
	// Error is why its databases didn't load.
	Error string    `yaml:"error"`
	At    time.Time `yaml:"at"`
	// Kept is the data from before, set aside on the server it runs on,
	// for putting back ("" if there's none). KeptByRestore: the server's
	// own restore kept it (backup.Restores.PutBack puts it back);
	// otherwise the controller's (node.Mover.PutBackKept).
	Kept          string `yaml:"kept,omitempty"`
	KeptByRestore bool   `yaml:"kept_by_restore,omitempty"`
	// MovedFrom is the server a move through a backup came from, when its
	// databases didn't load here and moving it back failed too.
	MovedFrom string `yaml:"moved_from,omitempty"`
	// NoBackup: a lab restore found no backup of it, so it came back with
	// neither its files nor its databases (Error says why).
	NoBackup bool `yaml:"no_backup,omitempty"`
}

// Restored is the backup a lab restore put a service back from
// (Instance.DumpsPending), as a hold names it (Hold.Backup, Hold.Taken).
type Restored struct {
	Backup string    `yaml:"backup"`
	Taken  time.Time `yaml:"taken"`
}

// RouteOverride holds user changes to a package route. Empty fields keep the
// package default.
type RouteOverride struct {
	Subdomain string `yaml:"subdomain,omitempty"`
	Exposure  string `yaml:"exposure,omitempty"`
	// Port is the host loopback port the route's container is published
	// on. Roostack allocates it once (state.AllocatePorts); it isn't meant
	// to be edited.
	Port int `yaml:"port,omitempty"`
	// PublicPaths are the package's private paths (pkgspec.PublicRules)
	// the Owner made public anyway, after the warning (SPEC.md §6.11).
	PublicPaths []string `yaml:"public_paths,omitempty"`
}

// PortOverride changes a package port (pkgspec.Port). Unset fields keep
// the package's defaults.
type PortOverride struct {
	Enabled  *bool  `yaml:"enabled,omitempty"`
	HostPort int    `yaml:"host_port,omitempty"`
	Exposure string `yaml:"exposure,omitempty"`
}

// KeptPublic is a version kept public despite security advisories.
type KeptPublic struct {
	Version    string   `yaml:"version"`
	Advisories []string `yaml:"advisories"`
}

// KeptPublicFor says whether the Owner kept this version public despite
// the advisory with id.
func (in *Instance) KeptPublicFor(id string) bool {
	k := in.PublicAnyway
	return k != nil && k.Version == in.Version && slices.Contains(k.Advisories, id)
}

// HostOr is the server the service runs on: Host, or controller if Host
// is empty (a service installed before labs had several servers, until
// state.AssignHosts sets it).
func (in *Instance) HostOr(controller string) string { return cmp.Or(in.Host, controller) }

// ID is the service's name: Name, or the package's for its first copy.
func (in *Instance) ID() string {
	if in.Name != "" {
		return in.Name
	}
	return in.Package
}

// MaxName is the longest a service's ID can be.
const MaxName = 40

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ValidName reports whether name can be a service's ID: it ends up in
// unit, secret and folder names, and URLs. An ID is also its package's
// name, or that and a dash and more (Validate), so names say what they
// are and a package's own name is always free for its first copy.
func ValidName(name string) bool {
	return len(name) <= MaxName && nameRe.MatchString(name) && !strings.HasSuffix(name, "-") && !strings.Contains(name, "--")
}

// Save writes an instance file atomically. Comments in a hand-edited file
// are not preserved.
func (in *Instance) Save(path string) error {
	data, err := in.Marshal()
	if err != nil {
		return err
	}
	return fsutil.WriteFile(path, data, 0o640)
}

// Marshal encodes an instance as its file's YAML.
func (in *Instance) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(in); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Load reads an instance file.
func Load(path string) (*Instance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	in, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return in, nil
}

// Parse decodes an instance file's YAML.
func Parse(data []byte) (*Instance, error) {
	var in Instance
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&in); err != nil {
		return nil, err
	}
	if in.Updates == "" {
		in.Updates = UpdatesNotify
	}
	return &in, nil
}

// Validate checks the instance against its package.
// libraryNameRe is a media library's name (agent.ValidLibraryName).
var libraryNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func (in *Instance) Validate(p *pkgspec.Package) error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if in.Subdomain != "" {
		switch {
		case len(p.Routes) > 0 || len(p.Ports) == 0:
			add("subdomain: only for a package with ports and no web address (its routes have their own)")
		case !subdomainRe.MatchString(in.Subdomain):
			add("subdomain: %q is not a valid DNS label", in.Subdomain)
		}
	}

	if in.Package != p.Name {
		add("package: instance is for %q but package is %q", in.Package, p.Name)
	}
	if in.Name != "" && (!ValidName(in.Name) || (in.Name != p.Name && !strings.HasPrefix(in.Name, p.Name+"-"))) {
		add("name: %q must be %q, or start with %q and go on with lowercase letters, digits, and dashes (up to %d in all)",
			in.Name, p.Name, p.Name+"-", MaxName)
	}
	if in.Version != p.Version {
		add("version: instance wants %q but package is %q", in.Version, p.Version)
	}
	if in.Host != "" && !nodeid.ValidID(in.Host) {
		add("host: %q is not a node ID", in.Host)
	}
	for _, l := range in.Libraries {
		if !libraryNameRe.MatchString(l) {
			add("libraries: %q isn't a media library's name", l)
		}
	}
	if len(in.Libraries) > 0 && !p.ReadsLibraries() {
		add("libraries: the package reads no media libraries")
	}
	for _, l := range slices.Sorted(maps.Keys(in.LibraryPaths)) {
		if err := CheckLibraryPath(p, in.LibraryPaths, l); err != nil {
			add("library_paths.%s: %q: %v", l, in.LibraryPaths[l], err)
		} else if !slices.Contains(in.Libraries, l) {
			add("library_paths.%s: it doesn't read that library", l)
		}
	}
	if !slices.Contains([]string{UpdatesNotify, UpdatesPatch, UpdatesMinor, UpdatesAll}, in.Updates) {
		add("updates: must be notify, patch, minor, or all; got %q", in.Updates)
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
		for _, pp := range r.PublicPaths {
			if pr := p.Routes[name]; pr != nil && !slices.ContainsFunc(pr.Public.PrivatePaths,
				func(x pkgspec.PrivatePath) bool { return x.Path == pp }) {
				add("routes.%s.public_paths: %q isn't one of the package's private paths", name, pp)
			}
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
		// A port on a well-known one such as DNS's 53 stays at home: a DNS
		// server anyone on the internet can ask is used against others.
		if pp := p.Ports[name]; pp != nil && pp.OutsidePort != 0 && o.Exposure == pkgspec.ExposurePublic {
			add("ports.%s.exposure: %s (port %d) stays on the home network and the lab's VPN", name, pkgspec.OutsidePorts[pp.OutsidePort], pp.OutsidePort)
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
	if len(in.Env) > MaxEnv {
		add("env: at most %d variables", MaxEnv)
	}
	seenEnv := map[string]bool{}
	for i, e := range in.Env {
		if err := CheckEnvVar(p, e); err != nil {
			add("env[%d]: %v", i, err)
		}
		if seenEnv[e.Container+"/"+e.Name] {
			add("env[%d]: %s is in %s twice", i, e.Name, e.Container)
		}
		seenEnv[e.Container+"/"+e.Name] = true
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

// AllowsUpdate says whether the update policy applies an update of this
// kind (pkgspec.UpdateKind) automatically (SPEC.md §7): notify never;
// patch takes new definitions and patch releases; minor adds minor
// releases; all takes everything.
func (in *Instance) AllowsUpdate(kind string) bool {
	switch in.Updates {
	case UpdatesAll:
		return true
	case UpdatesMinor:
		return kind == pkgspec.UpdateRevision || kind == pkgspec.UpdatePatch || kind == pkgspec.UpdateMinor
	case UpdatesPatch:
		return kind == pkgspec.UpdateRevision || kind == pkgspec.UpdatePatch
	}
	return false
}

// ValidUpdates says whether s is an update policy.
func ValidUpdates(s string) bool {
	return s == UpdatesNotify || s == UpdatesPatch || s == UpdatesMinor || s == UpdatesAll
}

// libraryPathRe is a container path a library can be shown at: plain
// folder names, so it goes into a Quadlet Volume= line as it is.
var libraryPathRe = regexp.MustCompile(`^(/[A-Za-z0-9_][A-Za-z0-9._-]*)+$`)

// systemPaths are a container's own folders, which a library mustn't hide
// (as the package editor's Compose import refuses them).
var systemPaths = []string{"/bin", "/boot", "/dev", "/etc", "/lib", "/lib32", "/lib64", "/libx32", "/proc",
	"/run", "/sbin", "/sys", "/usr", "/var/lib", "/var/run"}

// CheckLibraryPath checks the path library l is given in paths: absolute
// and plain, not a system folder, not overlapping any of the package's
// mounts or another library's path (SPEC.md §8.4). The error says why, for
// people ("it overlaps …").
func CheckLibraryPath(p *pkgspec.Package, paths map[string]string, l string) error {
	at := paths[l]
	if !libraryNameRe.MatchString(l) {
		return fmt.Errorf("%q isn't a media library's name", l)
	}
	if !libraryPathRe.MatchString(at) || strings.Contains(at+"/", "/../") || strings.Contains(at+"/", "/./") {
		return errors.New("it must be a folder's full path inside the container, with only letters, digits, and - _ .")
	}
	for _, sys := range systemPaths {
		if fsutil.Within(at, sys) || fsutil.Within(sys, at) {
			return fmt.Errorf("it's one of the container's own folders (%s)", sys)
		}
	}
	for _, cn := range slices.Sorted(maps.Keys(p.Containers)) {
		for _, m := range p.Containers[cn].Mounts {
			if fsutil.Within(at, m.Path) || fsutil.Within(m.Path, at) {
				return fmt.Errorf("it overlaps %s, where %s already has a folder", m.Path, p.Title)
			}
		}
	}
	for _, other := range slices.Sorted(maps.Keys(paths)) {
		if other != l && (fsutil.Within(at, paths[other]) || fsutil.Within(paths[other], at)) {
			return fmt.Errorf("it overlaps %s, where the library %s is", paths[other], other)
		}
	}
	return nil
}

// LibraryPlacement is where a service's libraries with paths of their own
// go (SPEC.md §8.4). A library bound straight at its own path stays broken
// once its drive is unplugged and plugged back in, until the service
// restarts (docs/ASSUMPTIONS.md W5), while a folder of libraries comes back
// by itself. So when every such path has the same parent folder, that
// folder is a second view folder with each library in it under its old
// name; otherwise each library is bound at its path, and its server
// restarts the service when the library's drive comes back.
type LibraryPlacement struct {
	// Parent is the view folder's path inside the containers, and Names
	// what's in it: the old folder's name → the library.
	Parent string
	Names  map[string]string
	// Binds are libraries bound one by one: library → its path.
	Binds map[string]string
}

// noViewAt are folders a view folder never goes at, though a library may
// be inside one: images keep their own files there, which the view would
// hide (systemPaths are refused outright).
var noViewAt = []string{"/", "/app", "/opt", "/srv", "/var"}

// PlaceLibraries works out where the instance's libraries with paths go,
// for its package p: a view folder at their shared parent, unless the
// parent is one of noViewAt or holds one of the package's own mounts
// (the view is read-only, so nothing could be mounted inside it).
func (in *Instance) PlaceLibraries(p *pkgspec.Package) LibraryPlacement {
	var pl LibraryPlacement
	if len(in.LibraryPaths) == 0 {
		return pl
	}
	var parents []string
	for _, at := range in.LibraryPaths {
		parents = append(parents, cmp.Or(at[:strings.LastIndex(at, "/")], "/"))
	}
	slices.Sort(parents)
	parent := parents[0]
	shared := parents[len(parents)-1] == parent && !slices.Contains(noViewAt, parent)
	for _, c := range p.Containers {
		for _, m := range c.Mounts {
			shared = shared && !fsutil.Within(m.Path, parent)
		}
	}
	if shared {
		pl.Parent, pl.Names = parent, map[string]string{}
		for l, at := range in.LibraryPaths {
			pl.Names[at[len(parent)+1:]] = l
		}
		return pl
	}
	pl.Binds = maps.Clone(in.LibraryPaths)
	return pl
}
