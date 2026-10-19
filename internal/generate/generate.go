// Package generate turns desired state (package + instance + lab config)
// into the files Roostack writes: Quadlet units, a Caddy site snippet, plus the
// secrets, directories and backup plan the service needs.
//
// Generation is pure: it reads nothing from disk and has no side effects, so
// its output can be tested with golden files and inspected with
// `roostack render`.
package generate

import (
	"cmp"
	"fmt"
	"maps"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/quadlet"
)

// File is a generated file, named relative to its target directory.
type File struct {
	Name    string
	Content string
	// Restarts is a container (its Quadlet file's name) that reads the
	// file only when it starts, so a change to it restarts the container
	// (CrowdSec's files).
	Restarts string
}

// Secret is a Podman secret the service needs.
type Secret struct {
	Name string // Podman secret name, e.g. roostack-vaultwarden-admin_token
	// Generate means Roostack creates a random value. Otherwise the value
	// comes from the user (a secret setting without generate).
	Generate bool
	// Setting is the secret setting key, if any.
	Setting string
	// InstallOnly secrets are only needed by install hooks and are deleted
	// once those succeed: create them if a value is at hand, but a missing
	// one is not an error.
	InstallOnly bool
	// Holds names a Quadlet file that waits while this user secret hasn't
	// been given (e.g. Caddy until the domain step): the rest of the lab
	// is applied without it.
	Holds string
	// DBURL, if set, makes this secret a database's whole URL, password
	// included, from the database's password secret: for apps that take
	// their database only as a URL (databases.<name>.url; SPEC.md §8.1).
	DBURL *DBURL
}

// DBURL is what a database URL secret is made of. The password comes from
// the PasswordSecret when the secret is created.
type DBURL struct {
	PasswordSecret string
	Scheme         string // postgresql
	User, Host     string
	Port           int
	Name           string
}

// URL is the database's URL with password (url.UserPassword escapes it).
func (d DBURL) URL(password string) string {
	u := url.URL{Scheme: d.Scheme, User: url.UserPassword(d.User, password),
		Host: net.JoinHostPort(d.Host, strconv.Itoa(d.Port)), Path: "/" + d.Name}
	return u.String()
}

// Output is everything generated for one service.
type Output struct {
	Service string
	// ReadsLibraries: a container mounts its view of the media libraries,
	// which the server makes before starting it (SPEC.md §8.1).
	ReadsLibraries bool
	// Quadlets go in config.Paths.QuadletDir().
	Quadlets []File
	// CaddySite goes in <CaddyDir>/sites/. Nil when the service has no routes.
	CaddySite *File
	Secrets   []Secret
	// Dirs are host directories that must exist before starting.
	Dirs []string
	// Units are the systemd services to start. systemd orders them using
	// the generated Requires=/After= dependencies.
	Units []string
	// Hosts are the hostnames this service's routes use.
	Hosts []string
	// Ports are the host loopback ports its routes are published on.
	Ports []int
	// HostPorts are the package ports published on every host address,
	// which the firewall must let in.
	HostPorts []HostPort
	// Public: one of its addresses is public (SPEC.md §6.11), so the lab
	// needs CrowdSec running.
	Public bool
	Backup BackupPlan
	// Images are the registry images its containers run, for downloading
	// an update's before switching to it (SPEC.md §7).
	Images []string
	// Checks are how to tell its containers work, after an update.
	Checks []HealthCheck
	// Monitors are how monitoring checks its routes from outside, one per
	// route with an address (SPEC.md §6.15).
	Monitors []RouteMonitor
}

// RouteMonitor is how monitoring checks one route: an HTTPS request for
// Path at Host, through the lab's Caddy.
type RouteMonitor struct {
	Route string // the package's route name
	Host  string
	// Path is the package's monitor.path, else its container's health.http
	// path when the route leads to that port, else /.
	Path string
	// Statuses count as up (the package's monitor.statuses); empty: any
	// below 500.
	Statuses []int
	// Public: the route is open to the internet.
	Public bool
}

// HealthCheck checks one container (pkgspec.Health). Exactly one of HTTP
// and Cmd is set.
type HealthCheck struct {
	// Container is its container (and unit) name.
	Container string
	// HTTP is a URL the server running it can reach (a route's published
	// port), answering below 500 when it works.
	HTTP string
	// Cmd runs inside the container (with sh -c) and exits 0 when it works.
	Cmd string
}

// HostPort is an enabled package port (pkgspec.Port).
type HostPort struct {
	Name     string // the package's port name
	Title    string
	Port     int // on the host
	Protocol string
	Exposure string
}

// BackupPlan says what to back up for a service (SPEC.md §6.9).
type BackupPlan struct {
	// Include are host paths to snapshot (critical storage, database dumps).
	Include []string
	// Exclude are paths inside Include that must not be snapshotted, e.g.
	// live SQLite files (their consistent copy is in the dump directory).
	Exclude []string
	// Prepare are steps that make data consistent before the snapshot.
	Prepare []PrepareStep
}

// PrepareStep is one backup prepare action.
type PrepareStep struct {
	Type string // pkgspec.DBSQLite, DBPostgres, DBMySQL or DBMariaDB
	// Container is the unit/container to run a dump in (the servers).
	Container string
	User      string
	Database  string
	// Source is the host path of the live file (sqlite).
	Source string
	// Target is the host path the consistent copy is written to.
	Target string
}

// Service generates all output for one installed service.
func Service(lab config.Lab, p *pkgspec.Package, in *instance.Instance) (*Output, error) {
	if err := in.Validate(p); err != nil {
		return nil, err
	}
	g := newGen(lab, p, in, CaddyHostLoopback)
	if err := g.run(); err != nil {
		return nil, fmt.Errorf("%s: %w", in.ID(), err)
	}
	if lab.IsNode() {
		// The controller's Caddy serves a node's routes (ProxySite).
		g.out.CaddySite = nil
	}
	return g.out, nil
}

// ProxySite generates only the Caddy site for a service running on
// another server (SPEC.md §10.5): the controller's Caddy forwards its
// routes over Roostack's network to addr, where that server publishes them.
func ProxySite(lab config.Lab, p *pkgspec.Package, in *instance.Instance, addr string) (*Output, error) {
	if err := in.Validate(p); err != nil {
		return nil, err
	}
	g := newGen(lab, p, in, addr)
	for _, name := range slices.Sorted(maps.Keys(p.Containers)) {
		ok, err := g.ctx.enabled("containers."+name+".when", p.Containers[name].When)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", in.ID(), err)
		}
		g.included[name] = ok
	}
	if err := g.caddySite(); err != nil {
		return nil, fmt.Errorf("%s: %w", in.ID(), err)
	}
	// The ports are the other server's, not this one's.
	g.out.Ports = nil
	return g.out, nil
}

func newGen(lab config.Lab, p *pkgspec.Package, in *instance.Instance, upstream string) *gen {
	return &gen{
		lab: lab, pkg: p, ctx: buildContext(lab, p, in), upstream: upstream,
		name: in.ID(), title: cmp.Or(in.Title, p.Title), out: &Output{Service: in.ID()},
		included: map[string]bool{}, waf: !in.NoWAF,
	}
}

type gen struct {
	lab config.Lab
	pkg *pkgspec.Package
	// name is the service's ID (instance.ID): everything is named after it.
	// title is what people call it (its error pages say it).
	name, title string
	ctx         *Context
	out         *Output
	// upstream is the host Caddy reaches the routes' ports on.
	upstream string
	// included records which containers passed their `when:` condition.
	included map[string]bool
	// waf: CrowdSec's AppSec checks its public routes (SPEC.md §6.10).
	waf bool
}

func (g *gen) run() error {
	p := g.pkg
	for _, name := range slices.Sorted(maps.Keys(p.Containers)) {
		ok, err := g.ctx.enabled("containers."+name+".when", p.Containers[name].When)
		if err != nil {
			return err
		}
		g.included[name] = ok
	}

	if err := g.network(); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(p.Databases)) {
		if err := g.database(name, p.Databases[name]); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Containers)) {
		if !g.included[name] {
			continue
		}
		if err := g.container(name, p.Containers[name]); err != nil {
			return err
		}
	}
	if err := g.caddySite(); err != nil {
		return err
	}
	g.secrets()
	g.storage()
	return nil
}

func (g *gen) network() error {
	var u quadlet.Unit
	u.Section("Unit").Add("Description", "Roostack "+g.name+": network")
	u.Section("Network").Add("NetworkName", "roostack-"+g.name)
	return g.addQuadlet("roostack-"+g.name+".network", &u)
}

func (g *gen) container(name string, c *pkgspec.Container) error {
	svc := g.name
	unit := containerUnitName(svc, name)
	at := "containers." + name
	var u quadlet.Unit

	us := u.Section("Unit")
	us.Add("Description", fmt.Sprintf("Roostack %s: %s", svc, name))
	for _, dep := range c.DependsOn {
		kind, target, _ := strings.Cut(dep, ".")
		depUnit := dbUnitName(svc, target)
		if kind == "containers" {
			if !g.included[target] {
				return fmt.Errorf("%s.depends_on: %q is disabled by its when: condition", at, dep)
			}
			depUnit = containerUnitName(svc, target)
		}
		us.Add("Requires", depUnit+".service")
		us.Add("After", depUnit+".service")
	}

	cs := u.Section("Container")
	cs.Add("ContainerName", unit)
	cs.Add("Image", c.Image)
	g.out.Images = append(g.out.Images, c.Image)
	addTimezone(cs, g.lab)
	if len(c.Command) > 0 {
		args := make([]string, len(c.Command))
		for i, a := range c.Command {
			r, err := g.ctx.render(fmt.Sprintf("%s.command[%d]", at, i), a)
			if err != nil {
				return err
			}
			args[i] = r
		}
		cs.Exec(args)
	}
	cs.Add("Network", "roostack-"+svc+".network")
	// Routed containers are published on the host's loopback only; Caddy
	// reaches them through pasta's loopback mapping (SPEC.md §6.5). A node
	// publishes them on its address on Roostack's network instead, for the
	// controller's Caddy (the firewall lets only that network in).
	publish := "127.0.0.1"
	if g.lab.IsNode() {
		publish = g.lab.OverlayAddr
	}
	for _, rn := range slices.Sorted(maps.Keys(g.pkg.Routes)) {
		r := g.pkg.Routes[rn]
		if r.Container != name {
			continue
		}
		port := g.ctx.Routes[rn].Port
		if port == 0 {
			return fmt.Errorf("routes.%s: no host port allocated (run roostack apply)", rn)
		}
		cs.Add("PublishPort", fmt.Sprintf("%s:%d:%d", publish, port, r.Port))
	}
	for _, pn := range slices.Sorted(maps.Keys(g.pkg.Ports)) {
		pp, pc := g.pkg.Ports[pn], g.ctx.Ports[pn]
		if pp.Container != name || !pc.Enabled {
			continue
		}
		// All host addresses: the firewall limits who can reach it.
		cs.Add("PublishPort", fmt.Sprintf("%d:%d/%s", pc.HostPort, pp.Port, pc.Protocol))
		g.out.HostPorts = append(g.out.HostPorts, HostPort{Name: pn, Title: pp.Title, Port: pc.HostPort,
			Protocol: pc.Protocol, Exposure: pc.Exposure})
	}
	for _, k := range slices.Sorted(maps.Keys(c.Env)) {
		v, err := g.ctx.render(at+".env."+k, c.Env[k])
		if err != nil {
			return err
		}
		cs.Env(k, v)
	}
	for _, k := range slices.Sorted(maps.Keys(c.Secrets)) {
		cs.Add("Secret", fmt.Sprintf("%s,type=env,target=%s", secretName(svc, c.Secrets[k]), k))
	}

	mounts := slices.Clone(c.Mounts)
	// Parent directories must be mounted before nested ones (ASSUMPTIONS B4).
	slices.SortStableFunc(mounts, func(a, b pkgspec.Mount) int { return strings.Compare(a.Path, b.Path) })
	for _, m := range mounts {
		if m.Libraries {
			// The service's view folder: a mount of each library it may
			// read appears in it as it comes and goes (rslave), read-only
			// (SPEC.md §8.1, docs/ASSUMPTIONS.md M3). The server makes the
			// folder before starting it.
			cs.Add("Volume", config.LibraryView(svc)+":"+m.Path+":ro,rslave")
			g.out.ReadsLibraries = true
			continue
		}
		vol := g.storagePath(m.Storage) + ":" + m.Path
		if m.ReadOnly {
			vol += ":ro"
		}
		cs.Add("Volume", vol)
	}

	gpu := false
	for i, d := range c.Devices {
		ok, err := g.ctx.enabled(fmt.Sprintf("%s.devices[%d].when", at, i), d.When)
		if err != nil {
			return err
		}
		gpu = gpu || ok
	}
	if gpu {
		// Intel iGPU (ASSUMPTIONS A7). Only "intel" is accepted by validation.
		cs.Add("AddDevice", "/dev/dri")
		cs.Add("GroupAdd", "keep-groups")
	}

	if c.Health != nil && c.Health.Cmd != "" {
		// HTTP checks are done by Roostack itself (ASSUMPTIONS F1).
		cs.Add("HealthCmd", c.Health.Cmd)
		g.out.Checks = append(g.out.Checks, HealthCheck{Container: unit, Cmd: c.Health.Cmd})
	}
	if c.Health != nil && c.Health.HTTP != nil {
		// Through a route's published port, when one leads to the checked
		// port: Roostack checks it after an update (ASSUMPTIONS F1).
		for _, rn := range slices.Sorted(maps.Keys(g.pkg.Routes)) {
			r := g.pkg.Routes[rn]
			if r.Container == name && r.Port == c.Health.HTTP.Port {
				g.out.Checks = append(g.out.Checks, HealthCheck{Container: unit,
					HTTP: fmt.Sprintf("http://%s%s", net.JoinHostPort(publish, strconv.Itoa(g.ctx.Routes[rn].Port)), c.Health.HTTP.Path)})
				break
			}
		}
	}
	cs.Add("Label", "io.roostack.service="+svc)
	cs.Add("Label", "io.roostack.component="+name)
	addPassthrough(&u, c.Quadlet)
	addServiceSections(&u)

	if err := g.addQuadlet(unit+".container", &u); err != nil {
		return err
	}
	g.out.Units = append(g.out.Units, unit+".service")
	return nil
}

func (g *gen) database(name string, d *pkgspec.Database) error {
	switch d.Type {
	case pkgspec.DBSQLite:
		if d.BackedUp() {
			storage, rel, _ := strings.Cut(d.Path, "/")
			live := filepath.Join(g.storagePath(storage), rel)
			g.out.Backup.Prepare = append(g.out.Backup.Prepare, PrepareStep{
				Type:   pkgspec.DBSQLite,
				Source: live,
				Target: filepath.Join(g.dumpDir(), name+".sqlite3"),
			})
			g.out.Backup.Exclude = append(g.out.Backup.Exclude, live, live+"-wal", live+"-shm")
		}
		return nil
	case pkgspec.DBPostgres:
		return g.postgres(name, d)
	case pkgspec.DBRedis:
		return g.redis(name, d)
	case pkgspec.DBMySQL, pkgspec.DBMariaDB:
		return g.mysql(name, d)
	}
	return fmt.Errorf("databases.%s: unsupported type %q", name, d.Type)
}

func (g *gen) postgres(name string, d *pkgspec.Database) error {
	svc := g.name
	unit := dbUnitName(svc, name)
	db := g.ctx.Databases[name]
	dataDir := filepath.Join(g.serviceDir(), "db", name)

	var u quadlet.Unit
	u.Section("Unit").Add("Description", fmt.Sprintf("Roostack %s: database %s (postgres)", svc, name))
	cs := u.Section("Container")
	cs.Add("ContainerName", unit)
	cs.Add("Image", cmp.Or(d.Image, DefaultPostgresImage))
	g.out.Images = append(g.out.Images, cmp.Or(d.Image, DefaultPostgresImage))
	addTimezone(cs, g.lab)
	cs.Add("Network", "roostack-"+svc+".network")
	cs.Env("POSTGRES_USER", db.User)
	cs.Env("POSTGRES_DB", db.Name)
	cs.Env("POSTGRES_PASSWORD_FILE", "/run/secrets/db-password")
	cs.Add("Secret", secretName(svc, "databases."+name+".password")+",type=mount,target=db-password")
	// PGDATA location differs in some images (e.g. Postgres 18). ASSUMPTIONS I1.
	cs.Add("Volume", dataDir+":/var/lib/postgresql/data")
	cs.Add("HealthCmd", fmt.Sprintf("pg_isready -U %s -d %s", db.User, db.Name))
	addDBHealthTiming(cs)
	cs.Add("Label", "io.roostack.service="+svc)
	cs.Add("Label", "io.roostack.component=db-"+name)
	addPassthrough(&u, d.Quadlet)
	addServiceSections(&u)

	if err := g.addQuadlet(unit+".container", &u); err != nil {
		return err
	}
	g.out.Units = append(g.out.Units, unit+".service")
	g.out.Dirs = append(g.out.Dirs, dataDir)
	g.out.Secrets = append(g.out.Secrets, Secret{Name: secretName(svc, "databases."+name+".password"), Generate: true})
	if d.BackedUp() {
		g.out.Backup.Prepare = append(g.out.Backup.Prepare, PrepareStep{
			Type: pkgspec.DBPostgres, Container: unit, User: db.User, Database: db.Name,
			Target: filepath.Join(g.dumpDir(), name+".sql"),
		})
	}
	return nil
}

// mysql generates a MySQL or MariaDB container (SPEC.md §8.1, "MySQL and
// MariaDB"): the app's user and database, its password from a Podman
// secret, and a random root password nobody keeps (Roostack needs only
// the app's user, which can drop and recreate its own database).
func (g *gen) mysql(name string, d *pkgspec.Database) error {
	svc := g.name
	unit := dbUnitName(svc, name)
	db := g.ctx.Databases[name]
	dataDir := filepath.Join(g.serviceDir(), "db", name)
	image, prefix, admin := DefaultMySQLImage, "MYSQL", "mysqladmin"
	if d.Type == pkgspec.DBMariaDB {
		image, prefix, admin = DefaultMariaDBImage, "MARIADB", "mariadb-admin"
	}
	image = cmp.Or(d.Image, image)

	var u quadlet.Unit
	u.Section("Unit").Add("Description", fmt.Sprintf("Roostack %s: database %s (%s)", svc, name, d.Type))
	cs := u.Section("Container")
	cs.Add("ContainerName", unit)
	cs.Add("Image", image)
	g.out.Images = append(g.out.Images, image)
	addTimezone(cs, g.lab)
	cs.Add("Network", "roostack-"+svc+".network")
	cs.Env(prefix+"_USER", db.User)
	cs.Env(prefix+"_DATABASE", db.Name)
	cs.Env(prefix+"_PASSWORD_FILE", "/run/secrets/db-password")
	cs.Env(prefix+"_RANDOM_ROOT_PASSWORD", "yes")
	if d.Type == pkgspec.DBMariaDB {
		// A newer major version upgrades the data as it starts (ASSUMPTIONS
		// I3); MySQL does so by itself.
		cs.Env("MARIADB_AUTO_UPGRADE", "1")
	}
	cs.Add("Secret", secretName(svc, "databases."+name+".password")+",type=mount,target=db-password")
	cs.Add("Volume", dataDir+":/var/lib/mysql")
	// Over TCP: on first start the image's entrypoint runs a temporary
	// server on its socket only, which mustn't count as ready.
	cs.Add("HealthCmd", admin+" ping -h 127.0.0.1 --silent")
	addDBHealthTiming(cs)
	cs.Add("Label", "io.roostack.service="+svc)
	cs.Add("Label", "io.roostack.component=db-"+name)
	addPassthrough(&u, d.Quadlet)
	addServiceSections(&u)

	if err := g.addQuadlet(unit+".container", &u); err != nil {
		return err
	}
	g.out.Units = append(g.out.Units, unit+".service")
	g.out.Dirs = append(g.out.Dirs, dataDir)
	g.out.Secrets = append(g.out.Secrets, Secret{Name: secretName(svc, "databases."+name+".password"), Generate: true})
	if d.BackedUp() {
		g.out.Backup.Prepare = append(g.out.Backup.Prepare, PrepareStep{
			Type: d.Type, Container: unit, User: db.User, Database: db.Name,
			Target: filepath.Join(g.dumpDir(), name+".sql"),
		})
	}
	return nil
}

func (g *gen) redis(name string, d *pkgspec.Database) error {
	svc := g.name
	unit := dbUnitName(svc, name)

	var u quadlet.Unit
	u.Section("Unit").Add("Description", fmt.Sprintf("Roostack %s: database %s (redis)", svc, name))
	cs := u.Section("Container")
	cs.Add("ContainerName", unit)
	cs.Add("Image", cmp.Or(d.Image, DefaultRedisImage))
	g.out.Images = append(g.out.Images, cmp.Or(d.Image, DefaultRedisImage))
	addTimezone(cs, g.lab)
	cs.Add("Network", "roostack-"+svc+".network")
	if d.BackedUp() {
		// Persisted snapshots are consistent on disk, so the data directory is
		// backed up directly.
		dataDir := filepath.Join(g.serviceDir(), "db", name)
		cs.Add("Volume", dataDir+":/data")
		g.out.Dirs = append(g.out.Dirs, dataDir)
		g.out.Backup.Include = append(g.out.Backup.Include, dataDir)
	}
	cs.Add("HealthCmd", "valkey-cli ping || redis-cli ping")
	addDBHealthTiming(cs)
	cs.Add("Label", "io.roostack.service="+svc)
	cs.Add("Label", "io.roostack.component=db-"+name)
	addPassthrough(&u, d.Quadlet)
	addServiceSections(&u)

	if err := g.addQuadlet(unit+".container", &u); err != nil {
		return err
	}
	g.out.Units = append(g.out.Units, unit+".service")
	return nil
}

func (g *gen) caddySite() error {
	var routes []caddy.Route
	for _, name := range slices.Sorted(maps.Keys(g.pkg.Routes)) {
		r := g.pkg.Routes[name]
		if !g.included[r.Container] {
			return fmt.Errorf("routes.%s: container %q is disabled by its when: condition", name, r.Container)
		}
		rc := g.ctx.Routes[name]
		if rc.Port == 0 {
			return fmt.Errorf("routes.%s: no host port allocated (run roostack apply)", name)
		}
		g.out.Ports = append(g.out.Ports, rc.Port)
		if !g.lab.HasAddress() {
			// No web address yet (SPEC.md §6.6): the service runs on its
			// local port, and gets its site once the lab has an address.
			continue
		}
		g.out.Hosts = append(g.out.Hosts, rc.Host)
		g.out.Monitors = append(g.out.Monitors, g.routeMonitor(name, r, rc.Host, rc.Exposure == pkgspec.ExposurePublic))
		cr := caddy.Route{
			ID:             g.name + "-" + name,
			Service:        g.name,
			Title:          g.title,
			Host:           rc.Host,
			OldHost:        OldHost(g.lab, rc.Host),
			Upstream:       fmt.Sprintf("%s:%d", g.upstream, rc.Port),
			Private:        rc.Exposure != pkgspec.ExposurePublic,
			PrivateFrom:    PrivateFrom(g.lab),
			PrivateExcept:  g.lab.EdgeAddrs,
			MaxBodySize:    r.MaxBodySize,
			ClientIPHeader: r.ClientIPHeader,
		}
		if !cr.Private {
			// The package's private paths stay home-only unless the Owner
			// opened them (SPEC.md §6.11).
			for _, pp := range r.Public.PrivatePaths {
				if !slices.Contains(rc.PublicPaths, pp.Path) {
					cr.PrivatePaths = append(cr.PrivatePaths, pp.Path)
				}
			}
			cr.LoginPaths = r.Public.LoginPaths
			cr.WAF = g.waf
			// The check link reaches the UI on the Caddy server (internal/public).
			cr.CheckUpstream = fmt.Sprintf("%s:%d", CaddyHostLoopback, g.lab.UIPort)
			g.out.Public = true
		}
		routes = append(routes, cr)
	}
	if len(routes) == 0 {
		return nil
	}
	site, err := caddy.Site(g.name, routes)
	if err != nil {
		return err
	}
	g.out.CaddySite = &File{Name: g.name + ".caddy", Content: site}
	return nil
}

// routeMonitor is how monitoring checks a route (SPEC.md §6.15).
func (g *gen) routeMonitor(name string, r *pkgspec.Route, host string, public bool) RouteMonitor {
	m := RouteMonitor{Route: name, Host: host, Path: "/", Public: public}
	if c := g.pkg.Containers[r.Container]; c != nil && c.Health != nil && c.Health.HTTP != nil && c.Health.HTTP.Port == r.Port {
		m.Path = c.Health.HTTP.Path
	}
	if r.Monitor != nil {
		if r.Monitor.Path != "" {
			m.Path = r.Monitor.Path
		}
		m.Statuses = r.Monitor.Statuses
	}
	return m
}

func (g *gen) secrets() {
	for _, s := range g.pkg.Settings {
		if s.Type == pkgspec.SettingSecret {
			g.out.Secrets = append(g.out.Secrets, Secret{Name: secretName(g.name, s.Key), Generate: s.Generate,
				Setting: s.Key, InstallOnly: s.InstallOnly})
		}
	}
	// A database URL, made from its password, for each one a container
	// asks for (hooks take only secret settings and inputs).
	var refs []string
	for _, c := range g.pkg.Containers {
		refs = slices.AppendSeq(refs, maps.Values(c.Secrets))
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		name, ok := dbURLRef(ref)
		if !ok || seen[name] || g.pkg.Databases[name] == nil {
			continue
		}
		seen[name] = true
		db := g.ctx.Databases[name]
		g.out.Secrets = append(g.out.Secrets, Secret{Name: secretName(g.name, ref), DBURL: &DBURL{
			PasswordSecret: secretName(g.name, "databases."+name+".password"), Scheme: "postgresql",
			User: db.User, Host: db.Host, Port: db.Port, Name: db.Name}})
	}
	slices.SortFunc(g.out.Secrets, func(a, b Secret) int { return strings.Compare(a.Name, b.Name) })
}

func (g *gen) storage() {
	for _, name := range slices.Sorted(maps.Keys(g.pkg.Storage)) {
		dir := g.storagePath(name)
		g.out.Dirs = append(g.out.Dirs, dir)
		if g.pkg.Storage[name].Class == pkgspec.ClassCritical {
			g.out.Backup.Include = append(g.out.Backup.Include, dir)
		}
	}
	if len(g.out.Backup.Prepare) > 0 {
		g.out.Dirs = append(g.out.Dirs, g.dumpDir())
		g.out.Backup.Include = append(g.out.Backup.Include, g.dumpDir())
	}
	slices.Sort(g.out.Dirs)
	slices.Sort(g.out.Backup.Include)
	slices.Sort(g.out.Backup.Exclude)
}

func (g *gen) addQuadlet(name string, u *quadlet.Unit) error {
	if err := u.Validate(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	g.out.Quadlets = append(g.out.Quadlets, File{Name: name, Content: u.String()})
	return nil
}

func (g *gen) serviceDir() string {
	return filepath.Join(g.lab.Paths.ServicesDir, g.name)
}

func (g *gen) dumpDir() string { return filepath.Join(g.serviceDir(), "db-dump") }

// storagePath is the host directory for a storage entry (SPEC.md §6.7).
func (g *gen) storagePath(name string) string {
	if g.pkg.Storage[name].Location == pkgspec.LocationMedia {
		return filepath.Join(g.lab.Paths.MediaDir, g.name, name)
	}
	return filepath.Join(g.serviceDir(), name)
}

// addTimezone gives a container the host's time zone, so apps show local
// times. The zone is named rather than "local": changing it changes the unit
// file, and the reconciler restarts the container.
func addTimezone(cs *quadlet.Section, lab config.Lab) {
	if lab.Timezone != "" {
		cs.Add("Timezone", lab.Timezone)
	}
}

func containerUnitName(svc, container string) string { return "roostack-" + svc + "-" + container }

func dbUnitName(svc, db string) string { return "roostack-" + svc + "-db-" + db }

// secretName maps a package secret reference to a Podman secret name.
func secretName(svc, ref string) string {
	if name, ok := dbURLRef(ref); ok {
		return "roostack-" + svc + "-db-" + name + "-url"
	}
	if db, ok := strings.CutPrefix(ref, "databases."); ok {
		name, _, _ := strings.Cut(db, ".")
		return "roostack-" + svc + "-db-" + name + "-password"
	}
	return "roostack-" + svc + "-" + ref
}

// dbURLRef reports whether ref is databases.<name>.url, and its name.
func dbURLRef(ref string) (string, bool) {
	db, ok := strings.CutPrefix(ref, "databases.")
	if !ok {
		return "", false
	}
	name, field, _ := strings.Cut(db, ".")
	return name, field == "url"
}

// addServiceSections adds the [Service] and [Install] sections every
// container shares.
func addServiceSections(u *quadlet.Unit) {
	s := u.Section("Service")
	s.Add("Restart", "always")
	// Allow time for large image pulls on first start.
	s.Add("TimeoutStartSec", "900")
	u.Section("Install").Add("WantedBy", "default.target")
}

// addDBHealthTiming makes dependants wait until the database is healthy
// (Notify=healthy, ASSUMPTIONS A6), checking often enough to start quickly.
func addDBHealthTiming(cs *quadlet.Section) {
	cs.Add("HealthInterval", "10s")
	cs.Add("HealthStartPeriod", "30s")
	cs.Add("Notify", "healthy")
}

// addPassthrough appends raw Quadlet keys from a package (SPEC.md §8.1).
func addPassthrough(u *quadlet.Unit, q pkgspec.Quadlet) {
	for _, section := range slices.Sorted(maps.Keys(q)) {
		s := u.Section(section)
		for _, key := range slices.Sorted(maps.Keys(q[section])) {
			switch v := q[section][key].(type) {
			case []any:
				for _, e := range v {
					s.Add(key, fmt.Sprint(e))
				}
			default:
				s.Add(key, fmt.Sprint(v))
			}
		}
	}
}

// OldHost is host's name at the lab's previous address while that still
// works, or "" (SPEC.md §6.6): the same subdomain there.
func OldHost(lab config.Lab, host string) string {
	old := lab.Old(time.Now())
	sub, ok := strings.CutSuffix(host, "."+lab.Domain)
	if old == nil || !ok {
		return ""
	}
	return sub + "." + old.Domain
}
