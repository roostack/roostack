package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/generate"
	"roostack.cloud/roostack/internal/pkgtest"
)

// newLab sets up a catalog of the package fixtures and an empty state dir.
func newLab(t *testing.T) config.Lab {
	t.Helper()
	root := t.TempDir()
	lab := config.Default()
	lab.Domain = "lab.example.com"
	lab.DNS.Provider = "cloudflare"
	lab.Paths.StateDir = filepath.Join(root, "state")
	lab.Paths.CatalogDir = filepath.Join(root, "catalog")
	pkgtest.Catalog(t, lab.Paths.CatalogDir)
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
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"), "package: vaultwarden\nversion: 1.34.3-roostack.1\n")
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 2.1.0-roostack.1\n")

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
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"), "package: vaultwarden\nversion: 1.34.3-roostack.1\nroutes:\n  web: { port: 20000 }\n")
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 2.1.0-roostack.1\n")
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
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"), "package: vaultwarden\nversion: 1.34.3-roostack.1\nroutes:\n  web: { port: 20005 }\n")
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 2.1.0-roostack.1\nroutes:\n  web: { port: 20005 }\n")
	services, _ := LoadServices(lab)
	if _, err := Desired(lab, services); err == nil || !strings.Contains(err.Error(), "port 20005 is already used") {
		t.Errorf("err = %v, want duplicate port", err)
	}
}

func TestHostPorts(t *testing.T) {
	lab := newLab(t)
	file := filepath.Join(lab.Paths.InstancesDir(), "forgejo.yaml")
	desired := func(ports string) (*generate.Output, error) {
		t.Helper()
		writeFile(t, file, "package: forgejo\nversion: 15.0.9-roostack.1\nroutes:\n  web: { port: 20000 }\n"+ports)
		services, err := LoadServices(lab)
		if err != nil {
			t.Fatal(err)
		}
		d, err := Desired(lab, services)
		if err != nil {
			return nil, err
		}
		return d.Services[0], nil
	}

	// Off by default.
	out, err := desired("")
	if err != nil || len(out.HostPorts) != 0 {
		t.Fatalf("default: %v, host ports %v", err, out.HostPorts)
	}
	out, err = desired("ports:\n  ssh: { enabled: true }\n")
	if err != nil {
		t.Fatal(err)
	}
	want := generate.HostPort{Name: "ssh", Title: "Git over SSH", Port: 2222, Protocol: "tcp", Exposure: "private"}
	if len(out.HostPorts) != 1 || out.HostPorts[0] != want {
		t.Errorf("host ports = %+v, want %+v", out.HostPorts, want)
	}
	var app string
	for _, f := range out.Quadlets {
		if f.Name == "roostack-forgejo-app.container" {
			app = f.Content
		}
	}
	for _, line := range []string{"PublishPort=2222:2222/tcp", "FORGEJO__server__DISABLE_SSH=false", "FORGEJO__server__SSH_PORT=2222"} {
		if !strings.Contains(app, line) {
			t.Errorf("app unit missing %s", line)
		}
	}

	for ports, want := range map[string]string{
		"ports:\n  ssh: { enabled: true, host_port: 8480 }\n":  "already used by the Roostack admin UI",
		"ports:\n  ssh: { enabled: true, host_port: 20000 }\n": "reserved for Roostack's web services",
	} {
		var pe *PortInUseError
		if _, err := desired(ports); !errors.As(err, &pe) || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", ports, err, want)
		}
	}
}

func TestDuplicateHostname(t *testing.T) {
	lab := newLab(t)
	dir := lab.Paths.InstancesDir()
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"),
		"package: vaultwarden\nversion: 1.34.3-roostack.1\nroutes:\n  web: { subdomain: photos, port: 20001 }\n")
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 2.1.0-roostack.1\nroutes:\n  web: { port: 20002 }\n")

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
		"package: vaultwarden\nversion: 1.34.3-roostack.1\nroutes:\n  web: { subdomain: roost, port: 20001 }\n")
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
	writeFile(t, filepath.Join(lab.Paths.InstancesDir(), "passwords.yaml"), "package: vaultwarden\nversion: 1.34.3-roostack.1\n")
	if _, err := LoadServices(lab); err == nil {
		t.Error("expected an error when the file name doesn't match the package")
	}
}

func TestHostsAndNodes(t *testing.T) {
	lab := newLab(t)
	dir := lab.Paths.InstancesDir()
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"), "package: vaultwarden\nversion: 1.34.3-roostack.1\n")
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 2.1.0-roostack.1\nhost: n-0000000002\n")
	services, err := LoadServices(lab)
	if err != nil {
		t.Fatal(err)
	}
	changed := AssignHosts(services, "n-0000000001")
	if len(changed) != 1 || changed[0].Package.Name != "vaultwarden" || changed[0].Instance.Host != "n-0000000001" {
		t.Fatalf("AssignHosts changed %+v", changed)
	}
	mine := OnNode(services, "n-0000000001")
	if len(mine) != 1 || mine[0].Package.Name != "vaultwarden" {
		t.Errorf("this node's services: %+v", mine)
	}
	if other := OnNode(services, "n-0000000002"); len(other) != 1 || other[0].Package.Name != "immich" {
		t.Errorf("the other node's services: %+v", other)
	}
	// A bad host is rejected when the instance is validated.
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"), "package: vaultwarden\nversion: 1.34.3-roostack.1\nhost: ../x\n")
	if _, err := LoadServices(lab); err == nil || !strings.Contains(err.Error(), "not a node ID") {
		t.Errorf("bad host: %v", err)
	}
}

// Services generate from the lab's own copy of their package version, not
// the catalog's (SPEC.md §8.3.1).
func TestLabKeepsItsPackages(t *testing.T) {
	lab := newLab(t)
	dir := lab.Paths.InstancesDir()
	writeFile(t, filepath.Join(dir, "vaultwarden.yaml"), "package: vaultwarden\nversion: 1.34.3-roostack.1\n")
	kept := lab.Paths.LabPackageFile("vaultwarden", "1.34.3-roostack.1")

	// Installed before the lab kept copies: it takes one from the catalog.
	if _, err := LoadServices(lab); err != nil {
		t.Fatal(err)
	}
	catalog, _ := os.ReadFile(lab.Paths.CatalogPackageFile("vaultwarden", "1.34.3-roostack.1"))
	if got, err := os.ReadFile(kept); err != nil || string(got) != string(catalog) {
		t.Fatalf("the lab's copy is the catalog's file exactly: %v", err)
	}

	// A newer catalog changes nothing that's installed.
	writeFile(t, lab.Paths.CatalogPackageFile("vaultwarden", "1.35.0-roostack.1"), strings.ReplaceAll(string(catalog), "1.34.3", "1.35.0"))
	os.Remove(lab.Paths.CatalogPackageFile("vaultwarden", "1.34.3-roostack.1"))
	services, err := LoadServices(lab)
	if err != nil || len(services) != 1 || services[0].Package.Version != "1.34.3-roostack.1" {
		t.Fatalf("services = %+v, %v", services, err)
	}
	if err := KeepPackage(lab.Paths, "vaultwarden", "1.36.0-roostack.1"); err == nil {
		t.Error("the catalog isn't that version")
	}

	// A version neither the lab nor the catalog has is an error that says so.
	writeFile(t, filepath.Join(dir, "immich.yaml"), "package: immich\nversion: 1.0.0-roostack.1\n")
	if _, err := LoadServices(lab); err == nil || !strings.Contains(err.Error(), "no copy of immich 1.0.0-roostack.1") {
		t.Errorf("err = %v", err)
	}
	os.Remove(filepath.Join(dir, "immich.yaml"))
	if _, err := LoadPackage(lab.Paths, "../vaultwarden", "1"); err == nil {
		t.Error("a name that isn't a package's")
	}
	if _, err := LoadPackage(lab.Paths, "vaultwarden", "../../lab"); err == nil {
		t.Error("a version that isn't a file name")
	}

	// Copies no service uses go; the ones in use stay.
	if err := KeepPackage(lab.Paths, "forgejo", "15.0.9-roostack.1"); err != nil {
		t.Fatal(err)
	}
	old := lab.Paths.LabPackageFile("vaultwarden", "1.33.0-roostack.1")
	writeFile(t, old, "an older copy")
	services, _ = LoadServices(lab)
	if err := PrunePackages(lab.Paths, services); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Error("the copy in use must stay")
	}
	for _, gone := range []string{old, filepath.Dir(lab.Paths.LabPackageFile("forgejo", "x"))} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s should be gone: %v", gone, err)
		}
	}
}

// The catalog has the registry's layout, a file per version (SPEC.md
// §8.3.3): installing takes the newest, and registry.yaml isn't a version.
func TestCatalogNewestVersion(t *testing.T) {
	lab := newLab(t)
	old := lab.Paths.CatalogPackageFile("vaultwarden", "1.34.3-roostack.1")
	data, _ := os.ReadFile(old)
	for _, v := range []string{"1.34.3-roostack.10", "1.9.0-roostack.1"} {
		newer := strings.ReplaceAll(string(data), "version: 1.34.3-roostack.1", "version: "+v)
		writeFile(t, lab.Paths.CatalogPackageFile("vaultwarden", v), newer)
	}
	p, err := LoadCatalogPackage(lab.Paths, "vaultwarden")
	if err != nil || p.Version != "1.34.3-roostack.10" {
		t.Fatalf("newest = %+v, %v; want 1.34.3-roostack.10", p, err)
	}

	// A file that isn't the version it's named after is refused.
	writeFile(t, lab.Paths.CatalogPackageFile("vaultwarden", "2.0.0-roostack.1"), string(data))
	if _, err := LoadCatalogPackage(lab.Paths, "vaultwarden"); err == nil {
		t.Error("a file named after another version loaded")
	}

	for _, name := range []string{"nothing", "../vaultwarden"} {
		if _, err := LoadCatalogPackage(lab.Paths, name); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: err = %v, want not found", name, err)
		}
	}
	// A package folder with only the registry's file has no version.
	writeFile(t, filepath.Join(lab.Paths.CatalogPackageDir("empty"), "registry.yaml"), "tier: official\n")
	if _, err := LoadCatalogPackage(lab.Paths, "empty"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("registry.yaml alone: err = %v, want not found", err)
	}
}
