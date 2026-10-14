package generate

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
)

// Summary is what installing a service would set up, worked out from its
// package and the choices on the install form, for the page to show
// before installing (SPEC.md §8.3.4). It uses the same `when:` conditions
// as the generator, so it describes exactly what would run.
type Summary struct {
	// Kept is what the service stores: its storage, then its databases.
	Kept []Kept
	// Parts are what would run: the containers whose `when:` passes, then
	// the databases with containers of their own, each with the memory it
	// typically uses.
	Parts []Part
	// Memory adds up the parts' memory; parts that don't say (Part.Memory
	// is 0) aren't counted, so it's then at least this much.
	Memory int64
	// Addresses are its web addresses.
	Addresses []Address
	// Ports are the package's extra connections (pkgspec.Port), on or off.
	Ports []Port
	// GPU lists the containers given the server's GPU with these settings.
	GPU []string
	// Files are the files Roostack would write, as `roostack render`
	// shows them: the Quadlet units, then the Caddy site, each named by
	// its full path.
	Files []File
	// Dirs and Backup are the folders it gets and what's backed up of
	// them (the generator's Output).
	Dirs   []string
	Backup BackupPlan
}

// Kept is one of a service's stores: a storage folder or a database.
type Kept struct {
	Name string
	// Title is the storage's own (pkgspec.Storage.Title); empty for
	// databases and storage without one.
	Title string
	// Database is the database type, or empty for storage.
	Database string
	// Replaceable storage isn't backed up: the app makes it again.
	BackedUp, Replaceable bool
	// Path is where it is on the server; empty for a database kept in
	// a storage folder (sqlite) or only in memory (redis without backups).
	Path string
}

// Part is something that runs: a container or a database's container.
type Part struct {
	Name string
	// Database is the database type, or empty for a container.
	Database string
	// Memory is its expected memory in bytes; 0 if it doesn't say.
	Memory int64
}

// Address is a web address of the service.
type Address struct {
	Route  string
	Host   string // localhost until the lab has an address
	Public bool
	// Unsafe is the package's reason it shouldn't be public, if any.
	Unsafe string
}

// Port is one of the package's extra connections.
type Port struct {
	Name, Title string
	PortContext
}

// Summarize works out what installing p as in would set up. in is the
// service as the install form describes it (not saved, no ports
// allocated yet).
func Summarize(lab config.Lab, p *pkgspec.Package, in *instance.Instance) (*Summary, error) {
	out, err := Service(lab, p, in)
	if err != nil {
		return nil, err
	}
	g := newGen(lab, p, in, CaddyHostLoopback)
	s := &Summary{Dirs: out.Dirs, Backup: out.Backup}
	for _, f := range out.Quadlets {
		s.Files = append(s.Files, File{Name: filepath.Join(lab.Paths.QuadletDir(), f.Name), Content: f.Content})
	}
	if out.CaddySite != nil {
		s.Files = append(s.Files, File{Name: filepath.Join(lab.Paths.CaddySitesDir(), out.CaddySite.Name), Content: out.CaddySite.Content})
	}

	for _, name := range slices.Sorted(maps.Keys(p.Storage)) {
		st := p.Storage[name]
		s.Kept = append(s.Kept, Kept{Name: name, Title: st.Title, BackedUp: st.Class == pkgspec.ClassCritical,
			Replaceable: st.Class == pkgspec.ClassReplaceable, Path: g.storagePath(name)})
	}
	for _, name := range slices.Sorted(maps.Keys(p.Databases)) {
		d := p.Databases[name]
		k := Kept{Name: name, Database: d.Type, BackedUp: d.BackedUp()}
		if d.Type == pkgspec.DBPostgres || (d.Type == pkgspec.DBRedis && d.BackedUp()) {
			k.Path = filepath.Join(g.serviceDir(), "db", name)
		}
		s.Kept = append(s.Kept, k)
	}

	for _, name := range slices.Sorted(maps.Keys(p.Containers)) {
		c := p.Containers[name]
		ok, err := g.ctx.enabled("containers."+name+".when", c.When)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		s.Parts = append(s.Parts, Part{Name: name, Memory: c.ExpectedMemory()})
		for i, d := range c.Devices {
			on, err := g.ctx.enabled(fmt.Sprintf("containers.%s.devices[%d].when", name, i), d.When)
			if err != nil {
				return nil, err
			}
			if on && d.GPU != "" {
				s.GPU = append(s.GPU, name)
				break
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Databases)) {
		d := p.Databases[name]
		if d.Type == pkgspec.DBSQLite {
			continue // a file the app opens itself
		}
		s.Parts = append(s.Parts, Part{Name: name, Database: d.Type, Memory: d.ExpectedMemory()})
	}
	for _, part := range s.Parts {
		s.Memory += part.Memory
	}

	for _, name := range slices.Sorted(maps.Keys(p.Routes)) {
		rc := g.ctx.Routes[name]
		s.Addresses = append(s.Addresses, Address{Route: name, Host: rc.Host,
			Public: rc.Exposure == pkgspec.ExposurePublic, Unsafe: p.Routes[name].Public.Unsafe})
	}
	for _, name := range slices.Sorted(maps.Keys(p.Ports)) {
		s.Ports = append(s.Ports, Port{Name: name, Title: p.Ports[name].Title, PortContext: g.ctx.Ports[name]})
	}
	return s, nil
}

// MemoryUnknown reports whether a part doesn't say how much memory it
// uses, so Memory is only the least it needs.
func (s *Summary) MemoryUnknown() bool {
	return slices.ContainsFunc(s.Parts, func(p Part) bool { return p.Memory == 0 })
}
