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
