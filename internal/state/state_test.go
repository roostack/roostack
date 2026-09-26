package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mylab/mylab/internal/config"
)

// newLab sets up a catalog with the reference examples and an empty state dir.
func newLab(t *testing.T) config.Lab {
	t.Helper()
	root := t.TempDir()
	lab := config.Default()
	lab.Domain = "lab.example.com"
	lab.DNS.Provider = "cloudflare"
	lab.Paths.StateDir = filepath.Join(root, "state")
	lab.Paths.CatalogDir = filepath.Join(root, "catalog")
	for _, name := range []string{"vaultwarden", "immich"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "docs", "examples", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, lab.Paths.PackageFile(name), string(data))
	}
	return lab
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAndGenerate(t *testing.T) {
	lab := newLab(t)
	dir := lab.Paths.InstancesDir()
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"), "package: vaultwarden\nversion: 1.34.3-mylab.1\n")
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 2.1.0-mylab.1\n")

	services, err := LoadServices(lab)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Desired(lab, services); err == nil || !strings.Contains(err.Error(), "no host port") {
		t.Errorf("generating before ports are allocated should fail, got %v", err)
	}
	if _, err := AllocatePorts(services); err != nil {
		t.Fatal(err)
	}
	d, err := Desired(lab, services)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Services) != 2 || d.Services[0].Service != "immich" {
		t.Errorf("want immich and vaultwarden in order, got %d services", len(d.Services))
	}
}

func TestAllocatePorts(t *testing.T) {
	lab := newLab(t)
	dir := lab.Paths.InstancesDir()
	// Vaultwarden already has a port; Immich needs one.
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"), "package: vaultwarden\nversion: 1.34.3-mylab.1\nroutes:\n  web: { port: 20000 }\n")
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 2.1.0-mylab.1\n")
	services, _ := LoadServices(lab)

	changed, err := AllocatePorts(services)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0].Package.Name != "immich" {
		t.Fatalf("changed = %v, want only immich", changed)
	}
	if got := changed[0].Instance.Routes["web"].Port; got != 20001 {
		t.Errorf("immich port = %d, want 20001 (20000 is taken)", got)
	}
	if err := changed[0].Instance.Save(InstanceFile(lab, "immich")); err != nil {
		t.Fatal(err)
	}

	// Reloading keeps the stored port and allocates nothing new.
	services, _ = LoadServices(lab)
	if changed, _ := AllocatePorts(services); len(changed) != 0 {
		t.Errorf("ports must be stable, but reallocated %v", changed)
	}
	if _, err := Desired(lab, services); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicatePort(t *testing.T) {
	lab := newLab(t)
	dir := lab.Paths.InstancesDir()
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"), "package: vaultwarden\nversion: 1.34.3-mylab.1\nroutes:\n  web: { port: 20005 }\n")
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 2.1.0-mylab.1\nroutes:\n  web: { port: 20005 }\n")
	services, _ := LoadServices(lab)
	if _, err := Desired(lab, services); err == nil || !strings.Contains(err.Error(), "port 20005 is already used") {
		t.Errorf("err = %v, want duplicate port", err)
	}
}

func TestDuplicateHostname(t *testing.T) {
	lab := newLab(t)
	dir := lab.Paths.InstancesDir()
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"),
		"package: vaultwarden\nversion: 1.34.3-mylab.1\nroutes:\n  web: { subdomain: photos, port: 20001 }\n")
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 2.1.0-mylab.1\nroutes:\n  web: { port: 20002 }\n")

	services, err := LoadServices(lab)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Desired(lab, services); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("error = %v, want duplicate hostname", err)
	}
}

func TestAdminHostnameReserved(t *testing.T) {
	lab := newLab(t)
	writeFile(t, filepath.Join(lab.Paths.InstancesDir(), "vaultwarden.yaml"),
		"package: vaultwarden\nversion: 1.34.3-mylab.1\nroutes:\n  web: { subdomain: mylab, port: 20001 }\n")
	services, err := LoadServices(lab)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Desired(lab, services); err == nil || !strings.Contains(err.Error(), "admin UI") {
		t.Errorf("error = %v, want admin hostname conflict", err)
	}
}

func TestInstanceFileNameMustMatch(t *testing.T) {
	lab := newLab(t)
	writeFile(t, filepath.Join(lab.Paths.InstancesDir(), "passwords.yaml"), "package: vaultwarden\nversion: 1.34.3-mylab.1\n")
	if _, err := LoadServices(lab); err == nil {
		t.Error("expected an error when the file name doesn't match the package")
	}
}
