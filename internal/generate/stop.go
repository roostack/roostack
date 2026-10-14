package generate

import (
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/pkgspec"
)

// Stop trims a service's output to what still runs while Roostack has
// stopped it because the registry withdrew its version as dangerous
// (SPEC.md §8.3.4): its network, and the databases that are safe to keep
// running.
// Its containers, address and ports go; its folders, secrets and data
// stay, and nothing is deleted (the reconciler removes only Roostack's
// unit and site files).
//
// A database on Roostack's own image (the package names none) keeps
// running, so backups go on as before. One on an image the package sets
// could be the dangerous part, so it runs only while lastBackup is true:
// until a backup of the server has finished since the stop. After that
// its dump step goes, and backups keep the last dump, which nothing
// changes while the service is stopped. SQLite and Redis are backed up
// from their files, which need nothing running.
func Stop(o *Output, p *pkgspec.Package, lastBackup bool) {
	keep := map[string]bool{"roostack-" + o.Service + ".network": true}
	for name, d := range p.Databases {
		if d.Type != pkgspec.DBSQLite && (d.Image == "" || lastBackup) {
			keep[dbUnitName(o.Service, name)+".container"] = true
		}
	}
	o.Quadlets = slices.DeleteFunc(o.Quadlets, func(f File) bool { return !keep[f.Name] })
	var units []string
	for _, u := range o.Units {
		if keep[strings.TrimSuffix(u, ".service")+".container"] {
			units = append(units, u)
		}
	}
	o.Units = units
	o.Backup.Prepare = slices.DeleteFunc(o.Backup.Prepare, func(s PrepareStep) bool {
		return s.Type == pkgspec.DBPostgres && !keep[s.Container+".container"]
	})
	o.CaddySite, o.Hosts, o.Ports, o.HostPorts, o.Public = nil, nil, nil, nil, false
}
