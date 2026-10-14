package generate

import (
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// summarize is Summarize with the version and updates an install form's
// instance has.
func summarize(t *testing.T, p *pkgspec.Package, in *instance.Instance) (*Summary, error) {
	t.Helper()
	in.Version, in.Updates = p.Version, instance.UpdatesNotify
	// The manager allocates the route's port first.
	if in.Routes == nil {
		in.Routes = map[string]instance.RouteOverride{}
	}
	r := in.Routes["web"]
	r.Port = 20001
	in.Routes["web"] = r
	return Summarize(testLab(), p, in)
}

func loadExample(t *testing.T, name string) *pkgspec.Package {
	t.Helper()
	p, err := pkgspec.Load(pkgtest.File(name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The summary adds up the memory of what would run with the settings
// chosen, and says what's kept and backed up (SPEC.md §8.3.4).
func TestSummarize(t *testing.T) {
	p := loadExample(t, "immich")
	partNames := func(s *Summary) []string {
		var names []string
		for _, part := range s.Parts {
			names = append(names, part.Name)
		}
		return names
	}

	all, err := summarize(t, p, &instance.Instance{Package: "immich",
		Settings: map[string]any{"machine_learning": true, "hardware_acceleration": true}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := partNames(all), []string{"machine-learning", "server", "cache", "main"}; !slices.Equal(got, want) {
		t.Errorf("parts = %v, want %v", got, want)
	}
	// 1 GB server + 1.5 GB machine learning + 50 MB redis + 200 MB postgres.
	if all.Memory != 2_750_000_000 || all.MemoryUnknown() {
		t.Errorf("memory = %d (unknown %t), want 2.75 GB", all.Memory, all.MemoryUnknown())
	}
	if !slices.Equal(all.GPU, []string{"server"}) {
		t.Errorf("GPU = %v, want [server]", all.GPU)
	}

	less, err := summarize(t, p, &instance.Instance{Package: "immich",
		Settings: map[string]any{"machine_learning": false, "hardware_acceleration": false}})
	if err != nil {
		t.Fatal(err)
	}
	if less.Memory != 1_250_000_000 || len(less.GPU) != 0 || slices.Contains(partNames(less), "machine-learning") {
		t.Errorf("without machine learning: memory %d, GPU %v, parts %v", less.Memory, less.GPU, partNames(less))
	}
	// The files are the generator's, as roostack render shows them.
	for _, f := range less.Files {
		if strings.Contains(f.Name, "machine-learning") {
			t.Errorf("files include %s, which wouldn't run", f.Name)
		}
	}
	if last := less.Files[len(less.Files)-1].Name; last != "/var/lib/roostack/caddy/sites/immich.caddy" {
		t.Errorf("last file = %s, want the Caddy site", last)
	}

	kept := map[string]Kept{}
	for _, k := range all.Kept {
		kept[k.Name] = k
	}
	if k := kept["library"]; !k.BackedUp || k.Title != "Your photos and videos" || k.Path != "/srv/roostack/media/immich/library" {
		t.Errorf("library = %+v", k)
	}
	if k := kept["thumbs"]; k.BackedUp || !k.Replaceable {
		t.Errorf("thumbs = %+v, want replaceable", k)
	}
	if k := kept["cache"]; k.BackedUp || k.Database != pkgspec.DBRedis || k.Path != "" {
		t.Errorf("cache = %+v, want an unsaved redis", k)
	}
	if k := kept["main"]; !k.BackedUp || k.Path != "/srv/roostack/services/immich/db/main" {
		t.Errorf("main = %+v", k)
	}
	if len(all.Addresses) != 1 || all.Addresses[0].Host != "photos.lab.example.com" || all.Addresses[0].Public {
		t.Errorf("addresses = %+v", all.Addresses)
	}
}

// Vaultwarden's SQLite runs inside the app; Forgejo's SSH port is off
// until it's turned on.
func TestSummarizeSmall(t *testing.T) {
	vw, err := summarize(t, loadExample(t, "vaultwarden"), &instance.Instance{Package: "vaultwarden", Name: "vaultwarden-home",
		Routes: map[string]instance.RouteOverride{"web": {Subdomain: "passwords", Exposure: pkgspec.ExposurePublic}}})
	if err != nil {
		t.Fatal(err)
	}
	if vw.Memory != 50_000_000 || len(vw.Parts) != 1 {
		t.Errorf("vaultwarden: memory %d, parts %+v", vw.Memory, vw.Parts)
	}
	if a := vw.Addresses[0]; a.Host != "passwords.lab.example.com" || !a.Public {
		t.Errorf("vaultwarden address = %+v", a)
	}
	if vw.Kept[0].Path != "/srv/roostack/services/vaultwarden-home/data" {
		t.Errorf("a second copy's folders are its own: %+v", vw.Kept[0])
	}

	fj, err := summarize(t, loadExample(t, "forgejo"), &instance.Instance{Package: "forgejo",
		Settings: map[string]any{"admin_username": "admin", "admin_email": "admin@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if fj.Memory != 450_000_000 {
		t.Errorf("forgejo memory = %d, want 450 MB", fj.Memory)
	}
	if len(fj.Ports) != 1 || fj.Ports[0].Enabled || fj.Ports[0].HostPort != 2222 || fj.Ports[0].Title != "Git over SSH" {
		t.Errorf("forgejo ports = %+v", fj.Ports)
	}
}

// A container without a memory figure isn't counted, and the summary
// says so.
func TestSummarizeMemoryUnknown(t *testing.T) {
	p := loadExample(t, "vaultwarden")
	p.Containers["app"].Memory = ""
	s, err := summarize(t, p, &instance.Instance{Package: "vaultwarden"})
	if err != nil {
		t.Fatal(err)
	}
	if s.Memory != 0 || !s.MemoryUnknown() {
		t.Errorf("memory %d, unknown %t", s.Memory, s.MemoryUnknown())
	}
}
