package config

import (
	"os"
	"path/filepath"
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
