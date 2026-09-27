// Package generate turns desired state (package + instance + lab config)
// into the files MyLab writes: Quadlet units, a Caddy site snippet, plus the
// secrets, directories and backup plan the service needs.
//
// Generation is pure: it reads nothing from disk and has no side effects, so
// its output can be tested with golden files and inspected with
// `mylab render`.
package generate

import (
	"cmp"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mylab/mylab/internal/caddy"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/pkgspec"
	"github.com/mylab/mylab/internal/quadlet"
)

// File is a generated file, named relative to its target directory.
type File struct {
	Name    string
	Content string
}

// Secret is a Podman secret the service needs.
type Secret struct {
	Name string // Podman secret name, e.g. mylab-vaultwarden-admin_token
	// Generate means MyLab creates a random value. Otherwise the value
	// comes from the user (a secret setting without generate).
	Generate bool
	// Setting is the secret setting key, if any.
	Setting string
	// InstallOnly secrets are only needed by install hooks and are deleted
	// once those succeed: create them if a value is at hand, but a missing
	// one is not an error.
	InstallOnly bool
}

// Output is everything generated for one service.
type Output struct {
	Service string
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
	Backup    BackupPlan
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
	Type string // pkgspec.DBSQLite or pkgspec.DBPostgres
	// Container is the unit/container to run a dump in (postgres).
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
	g := &gen{
		lab: lab, pkg: p, ctx: buildContext(lab, p, in),
		out:      &Output{Service: p.Name},
		included: map[string]bool{},
	}
	if err := g.run(); err != nil {
		return nil, fmt.Errorf("%s: %w", p.Name, err)
	}
	return g.out, nil
}

type gen struct {
	lab config.Lab
	pkg *pkgspec.Package
	ctx *Context
	out *Output
	// included records which containers passed their `when:` condition.
	included map[string]bool
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
	u.Section("Unit").Add("Description", "MyLab "+g.pkg.Name+": network")
	u.Section("Network").Add("NetworkName", "mylab-"+g.pkg.Name)
	return g.addQuadlet("mylab-"+g.pkg.Name+".network", &u)
}

func (g *gen) container(name string, c *pkgspec.Container) error {
	svc := g.pkg.Name
	unit := containerUnitName(svc, name)
	at := "containers." + name
	var u quadlet.Unit

	us := u.Section("Unit")
	us.Add("Description", fmt.Sprintf("MyLab %s: %s", svc, name))
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
	cs.Add("Network", "mylab-"+svc+".network")
	// Routed containers are published on the host's loopback only; Caddy
	// reaches them through pasta's loopback mapping (SPEC.md §6.5).
	for _, rn := range slices.Sorted(maps.Keys(g.pkg.Routes)) {
		r := g.pkg.Routes[rn]
		if r.Container != name {
			continue
		}
		port := g.ctx.Routes[rn].Port
		if port == 0 {
			return fmt.Errorf("routes.%s: no host port allocated (run mylab apply)", rn)
		}
		cs.Add("PublishPort", fmt.Sprintf("127.0.0.1:%d:%d", port, r.Port))
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
		// HTTP checks are done by MyLab itself (ASSUMPTIONS F1).
		cs.Add("HealthCmd", c.Health.Cmd)
	}
	cs.Add("Label", "io.mylab.service="+svc)
	cs.Add("Label", "io.mylab.component="+name)
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
	}
	return fmt.Errorf("databases.%s: unsupported type %q", name, d.Type)
}

func (g *gen) postgres(name string, d *pkgspec.Database) error {
	svc := g.pkg.Name
	unit := dbUnitName(svc, name)
	db := g.ctx.Databases[name]
	dataDir := filepath.Join(g.serviceDir(), "db", name)

	var u quadlet.Unit
	u.Section("Unit").Add("Description", fmt.Sprintf("MyLab %s: database %s (postgres)", svc, name))
	cs := u.Section("Container")
	cs.Add("ContainerName", unit)
	cs.Add("Image", cmp.Or(d.Image, DefaultPostgresImage))
	addTimezone(cs, g.lab)
	cs.Add("Network", "mylab-"+svc+".network")
	cs.Env("POSTGRES_USER", db.User)
	cs.Env("POSTGRES_DB", db.Name)
	cs.Env("POSTGRES_PASSWORD_FILE", "/run/secrets/db-password")
	cs.Add("Secret", secretName(svc, "databases."+name+".password")+",type=mount,target=db-password")
	// PGDATA location differs in some images (e.g. Postgres 18). ASSUMPTIONS I1.
	cs.Add("Volume", dataDir+":/var/lib/postgresql/data")
	cs.Add("HealthCmd", fmt.Sprintf("pg_isready -U %s -d %s", db.User, db.Name))
	addDBHealthTiming(cs)
	cs.Add("Label", "io.mylab.service="+svc)
	cs.Add("Label", "io.mylab.component=db-"+name)
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

func (g *gen) redis(name string, d *pkgspec.Database) error {
	svc := g.pkg.Name
	unit := dbUnitName(svc, name)

	var u quadlet.Unit
	u.Section("Unit").Add("Description", fmt.Sprintf("MyLab %s: database %s (redis)", svc, name))
	cs := u.Section("Container")
	cs.Add("ContainerName", unit)
	cs.Add("Image", cmp.Or(d.Image, DefaultRedisImage))
	addTimezone(cs, g.lab)
	cs.Add("Network", "mylab-"+svc+".network")
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
	cs.Add("Label", "io.mylab.service="+svc)
	cs.Add("Label", "io.mylab.component=db-"+name)
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
			return fmt.Errorf("routes.%s: no host port allocated (run mylab apply)", name)
		}
		g.out.Hosts = append(g.out.Hosts, rc.Host)
		g.out.Ports = append(g.out.Ports, rc.Port)
		routes = append(routes, caddy.Route{
			ID:          g.pkg.Name + "-" + name,
			Host:        rc.Host,
			Upstream:    fmt.Sprintf("%s:%d", CaddyHostLoopback, rc.Port),
			Private:     rc.Exposure != pkgspec.ExposurePublic,
			MaxBodySize: r.MaxBodySize,
		})
	}
	if len(routes) == 0 {
		return nil
	}
	site, err := caddy.Site(g.pkg.Name, routes)
	if err != nil {
		return err
	}
	g.out.CaddySite = &File{Name: g.pkg.Name + ".caddy", Content: site}
	return nil
}

func (g *gen) secrets() {
	for _, s := range g.pkg.Settings {
		if s.Type == pkgspec.SettingSecret {
			g.out.Secrets = append(g.out.Secrets, Secret{Name: secretName(g.pkg.Name, s.Key), Generate: s.Generate,
				Setting: s.Key, InstallOnly: s.InstallOnly})
		}
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
	return filepath.Join(g.lab.Paths.ServicesDir, g.pkg.Name)
}

func (g *gen) dumpDir() string { return filepath.Join(g.serviceDir(), "db-dump") }

// storagePath is the host directory for a storage entry (SPEC.md §6.7).
func (g *gen) storagePath(name string) string {
	if g.pkg.Storage[name].Location == pkgspec.LocationMedia {
		return filepath.Join(g.lab.Paths.MediaDir, g.pkg.Name, name)
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

func containerUnitName(svc, container string) string { return "mylab-" + svc + "-" + container }

func dbUnitName(svc, db string) string { return "mylab-" + svc + "-db-" + db }

// secretName maps a package secret reference to a Podman secret name.
func secretName(svc, ref string) string {
	if db, ok := strings.CutPrefix(ref, "databases."); ok {
		name, _, _ := strings.Cut(db, ".")
		return "mylab-" + svc + "-db-" + name + "-password"
	}
	return "mylab-" + svc + "-" + ref
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
