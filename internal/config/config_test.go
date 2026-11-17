package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lab.yaml")
	lab := Default()
	lab.Domain = "lab.example.com"
	lab.Email = "me@example.com"
	lab.DNS.Provider = "cloudflare"
	lab.Backup = Backup{Repository: "s3:https://s3.example.com/b", Env: []string{"AWS_ACCESS_KEY_ID=x"}}
	lab.HasGPU = true // runtime only: must not be saved
	if err := lab.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Domain != lab.Domain || got.Backup.Repository != lab.Backup.Repository || got.Backup.Env[0] != "AWS_ACCESS_KEY_ID=x" || got.HasGPU {
		t.Errorf("round trip: %+v", got)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o640 {
		t.Errorf("mode = %o", info.Mode().Perm())
	}
}

func TestUpdateKeepsOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lab.yaml")
	lab := Default()
	lab.Domain = "lab.example.com"
	lab.DNS.Provider = "cloudflare"
	lab.Save(path)
	// Two settings changed by different parts of Roostack, one after the other.
	Update(path, func(l *Lab) { l.Backup.Repository = "/mnt/backup" })
	Update(path, func(l *Lab) { l.DNS.ManageRecords = true })
	got, err := Load(path)
	if err != nil || got.Backup.Repository != "/mnt/backup" || !got.DNS.ManageRecords || got.Domain != "lab.example.com" {
		t.Errorf("got %+v, %v", got, err)
	}
	// A config that isn't complete yet can still be updated.
	fresh := filepath.Join(t.TempDir(), "lab.yaml")
	Default().Save(fresh)
	if err := Update(fresh, func(l *Lab) { l.Domain = "new.example.com" }); err != nil {
		t.Error(err)
	}
}

// A new install's lab.yaml has only comments: no address yet (SPEC.md §6.6).
// TestNameAt: the lab's name as the file says now, not as it was read at
// start (the wizard names the lab while Roostack runs); the name at start
// if the file can't be read.
func TestNameAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lab.yaml")
	if err := Default().Save(path); err != nil {
		t.Fatal(err)
	}
	start, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	name := NameAt(path, start)
	if got := name(); got != "My lab" {
		t.Errorf("before it's named: %q", got)
	}
	if err := Update(path, func(l *Lab) { l.Name = "Test" }); err != nil {
		t.Fatal(err)
	}
	if got := name(); got != "Test" {
		t.Errorf("after the wizard named it: %q", got)
	}
	os.Remove(path)
	if got := name(); got != "My lab" {
		t.Errorf("no file: %q", got)
	}
}

func TestLoadNewInstall(t *testing.T) {
	data, err := os.ReadFile("../../packaging/deb/lab.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "lab.yaml")
	os.WriteFile(path, data, 0o640)
	lab, err := Load(path)
	if err != nil || lab.HasAddress() || lab.Domain != "" || lab.UIPort != DefaultUIPort || lab.Cloud.Disabled {
		t.Fatalf("lab = %+v, %v", lab, err)
	}
	if lab.DisplayName() != "My lab" {
		t.Errorf("name = %q", lab.DisplayName())
	}
	os.WriteFile(path, []byte("cloud:\n  disabled: true\n"), 0o640)
	if lab, err := Load(path); err != nil || !lab.Cloud.Disabled {
		t.Errorf("cloud off: %+v, %v", lab.Cloud, err)
	}
}

// What the domain itself shows (SPEC.md §5.3): nothing on an own domain
// and Your account on a roost.ac name by default, the Owner's choice
// otherwise; a choice that isn't whole is refused.
func TestDomainItself(t *testing.T) {
	own := Lab{Domain: "smith.family"}
	roost := Lab{Domain: "smiths.roost.ac", DNS: DNS{Provider: ProviderCloud}}
	if own.Shows() != ShowNothing || own.ServesDomain() || roost.Shows() != ShowAccount || (Lab{}).ServesDomain() {
		t.Errorf("defaults: %q %q", own.Shows(), roost.Shows())
	}
	if roost.UIHost() != "roost.smiths.roost.ac" || own.UIHost() != "roost.smith.family" {
		t.Errorf("Roostack's pages: %q %q", roost.UIHost(), own.UIHost())
	}
	roost.DomainItself = DomainItself{Show: ShowNothing, Public: true}
	own.DomainItself = DomainItself{Show: ShowPage, Public: true}
	if roost.ServesDomain() || roost.DomainPublic() || own.Shows() != ShowPage || !own.DomainPublic() {
		t.Errorf("chosen: %q %q", own.Shows(), roost.Shows())
	}
	for _, bad := range []DomainItself{{Show: "website"}, {Show: ShowService}, {Show: ShowAddress, Address: "ftp://x"},
		{Show: ShowAddress, Address: "https://x.example/{a}"}, {Show: ShowPage, Page: SimplePage{Heading: strings.Repeat("x", 101)}}} {
		if bad.Validate() == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	if err := (DomainItself{Show: ShowAddress, Address: "https://blog.example/"}).Validate(); err != nil {
		t.Error(err)
	}
}

// TestTestAddress: a test address is only ever a name under .localhost,
// and such a name only ever a test address, so Caddy's own certificates
// can't stand in for real ones on a real address (SPEC.md §6.6).
func TestTestAddress(t *testing.T) {
	for name, want := range map[string]bool{"localhost": true, "lab.localhost": true, "Lab.Localhost.": true,
		"lab.localhost.example.com": false, "notlocalhost": false, "lab.example.com": false, "": false} {
		if LocalhostName(name) != want {
			t.Errorf("LocalhostName(%q) = %v", name, !want)
		}
	}
	test := Default()
	test.Domain, test.DNS = "lab.localhost", DNS{Provider: ProviderTestAddress}
	if err := test.Validate(); err != nil || !test.TestAddress() || !test.HasAddress() || test.RoostAc() {
		t.Errorf("a test address: %v", err)
	}
	if test.SignInHost() != "my.lab.localhost" || test.UIHost() != "roost.lab.localhost" {
		t.Errorf("hosts: %s %s", test.SignInHost(), test.UIHost())
	}
	bad := map[string]Lab{
		"the provider on a real domain":          {Domain: "lab.example.com", DNS: DNS{Provider: ProviderTestAddress}},
		"the provider with no domain":            {DNS: DNS{Provider: ProviderTestAddress}},
		"a .localhost name with a real provider": {Domain: "lab.localhost", DNS: DNS{Provider: "cloudflare"}},
		"a .localhost name with Cloud's":         {Domain: "lab.localhost", DNS: DNS{Provider: ProviderCloud}},
		"a .localhost name with none":            {Domain: "lab.localhost"},
		"an old test address": {Domain: "lab.example.com", DNS: DNS{Provider: "cloudflare"},
			OldAddress: &OldAddress{Domain: "lab.localhost", DNS: DNS{Provider: ProviderTestAddress}}},
	}
	for what, l := range bad {
		lab := Default()
		lab.Domain, lab.DNS, lab.OldAddress = l.Domain, l.DNS, l.OldAddress
		if lab.Validate() == nil {
			t.Errorf("%s was accepted", what)
		}
	}
	// Loading refuses one too, so Roostack won't start with it.
	path := filepath.Join(t.TempDir(), "lab.yaml")
	os.WriteFile(path, []byte("domain: lab.localhost\ndns:\n  provider: cloudflare\n"), 0o600)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "roostack test-address") {
		t.Errorf("loading a .localhost name with a real provider: %v", err)
	}
}
