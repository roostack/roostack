package generate

import (
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// Stopped, Immich keeps its network and Roostack's Redis; its own
// Postgres image runs until the last backup, then its dump step goes and
// the backup keeps the last dump. Nothing it stores leaves the backup.
func TestStop(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("immich"))
	if err != nil {
		t.Fatal(err)
	}
	in := &instance.Instance{Package: "immich", Version: p.Version, Updates: "notify",
		Routes: map[string]instance.RouteOverride{"web": {Port: 18000}}}
	names := func(o *Output) []string {
		var out []string
		for _, f := range o.Quadlets {
			out = append(out, f.Name)
		}
		slices.Sort(out)
		return out
	}
	full, err := Service(testLab(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		lastBackup bool
		want       []string
		dump       bool
	}{
		{true, []string{"roostack-immich-db-cache.container", "roostack-immich-db-main.container", "roostack-immich.network"}, true},
		{false, []string{"roostack-immich-db-cache.container", "roostack-immich.network"}, false},
	} {
		o, _ := Service(testLab(), p, in)
		Stop(o, p, tc.lastBackup)
		if got := names(o); !slices.Equal(got, tc.want) {
			t.Errorf("last backup %v: quadlets %v, want %v", tc.lastBackup, got, tc.want)
		}
		if len(o.Units) != len(tc.want)-1 {
			t.Errorf("units %v", o.Units)
		}
		dump := slices.ContainsFunc(o.Backup.Prepare, func(s PrepareStep) bool { return s.Type == pkgspec.DBPostgres })
		if dump != tc.dump {
			t.Errorf("last backup %v: dump step %v", tc.lastBackup, dump)
		}
		if o.CaddySite != nil || len(o.Hosts)+len(o.Ports)+len(o.HostPorts) > 0 || o.Public {
			t.Error("its addresses and ports go")
		}
		if !slices.Equal(o.Backup.Include, full.Backup.Include) || !slices.Equal(o.Dirs, full.Dirs) || len(o.Secrets) != len(full.Secrets) {
			t.Error("its folders, secrets and backups stay")
		}
	}
}

// A service turned off (SPEC.md §6.3.5) keeps only its network and its
// Caddy site, whose routes answer with the "turned off" page; its folders,
// secrets and backups stay, without dump steps or checks.
func TestOff(t *testing.T) {
	t.Parallel()
	p, err := pkgspec.Load(pkgtest.File("immich"))
	if err != nil {
		t.Fatal(err)
	}
	in := &instance.Instance{Package: "immich", Version: p.Version, Updates: "notify",
		Routes: map[string]instance.RouteOverride{"web": {Port: 18000}}}
	full, err := Service(testLab(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Off = &config.Pause{By: "sam"}
	o, err := Service(testLab(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	Off(o)
	if len(o.Quadlets) != 1 || o.Quadlets[0].Name != "roostack-immich.network" || len(o.Units) != 0 {
		t.Errorf("quadlets %v, units %v; want only its network", o.Quadlets, o.Units)
	}
	if o.CaddySite == nil || !strings.Contains(o.CaddySite.Content, "vars roostack_off yes") || strings.Contains(o.CaddySite.Content, "reverse_proxy") ||
		!slices.Equal(o.Hosts, full.Hosts) {
		t.Errorf("its site should answer with the turned-off page, at the same addresses:\n%v", o.CaddySite)
	}
	if len(o.Ports)+len(o.HostPorts)+len(o.Monitors) > 0 || slices.ContainsFunc(o.Backup.Prepare, func(s PrepareStep) bool { return s.Container != "" }) {
		t.Error("its ports, checks and dump steps go")
	}
	if !slices.Equal(o.Backup.Include, full.Backup.Include) || !slices.Equal(o.Dirs, full.Dirs) || len(o.Secrets) != len(full.Secrets) {
		t.Error("its folders, secrets and backups stay")
	}
}
