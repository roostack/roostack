package instance

import (
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// vaultwarden is the example package: a "web" route, a bool setting
// (signups_allowed) and a secret (admin_token), plus an "ssh" port.
func vaultwarden(t *testing.T) *pkgspec.Package {
	t.Helper()
	p, err := pkgspec.Load(pkgtest.File("vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	p.Ports = map[string]*pkgspec.Port{"ssh": {}}
	return p
}

func TestParseDefaultsAndUnknownFields(t *testing.T) {
	in, err := Parse([]byte("package: vaultwarden\nversion: \"1\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Updates != UpdatesNotify {
		t.Errorf("updates default to notify, got %q", in.Updates)
	}
	// A typo in a hand-edited file is an error, not silently ignored.
	if _, err := Parse([]byte("package: vaultwarden\nsetings: {}\n")); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	on := true
	in := &Instance{Package: "vaultwarden", Host: "n-0123456789", Version: "1", Updates: UpdatesPatch,
		Routes:   map[string]RouteOverride{"web": {Subdomain: "passwords", Port: 20001}},
		Ports:    map[string]PortOverride{"ssh": {Enabled: &on, HostPort: 2222}},
		Settings: map[string]any{"signups_allowed": true}}
	path := filepath.Join(t.TempDir(), "vaultwarden.yaml")
	if err := in.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := in.Marshal()
	b, _ := got.Marshal()
	if string(a) != string(b) {
		t.Errorf("round trip changed it:\n%s\nvs\n%s", a, b)
	}
}

func TestValidate(t *testing.T) {
	p := vaultwarden(t)
	good := func() *Instance {
		return &Instance{Package: p.Name, Version: p.Version, Updates: UpdatesNotify, Host: "n-0123456789",
			Routes:   map[string]RouteOverride{"web": {Subdomain: "passwords", Exposure: pkgspec.ExposurePrivate, Port: 20001}},
			Ports:    map[string]PortOverride{"ssh": {HostPort: 2222}},
			Settings: map[string]any{"signups_allowed": false}}
	}
	if err := good().Validate(p); err != nil {
		t.Fatalf("a valid instance: %v", err)
	}
	// The Owner opened the package's private path (SPEC.md §6.11).
	opened := good()
	opened.Routes["web"] = RouteOverride{Exposure: pkgspec.ExposurePublic, PublicPaths: []string{"/admin"}}
	if err := opened.Validate(p); err != nil {
		t.Errorf("an opened private path: %v", err)
	}
	for _, tc := range []struct {
		change func(*Instance)
		want   string
	}{
		{func(in *Instance) { in.Package = "other" }, "package:"},
		{func(in *Instance) { in.Version = "0" }, "version:"},
		{func(in *Instance) { in.Host = "../etc" }, "host:"},
		{func(in *Instance) { in.Updates = "sometimes" }, "updates:"},
		{func(in *Instance) { in.Routes["nope"] = RouteOverride{} }, "routes.nope: package has no such route"},
		{func(in *Instance) { in.Routes["web"] = RouteOverride{Subdomain: "Not_A_Label"} }, "routes.web.subdomain"},
		{func(in *Instance) { in.Routes["web"] = RouteOverride{Subdomain: "a.b"} }, "routes.web.subdomain"},
		{func(in *Instance) { in.Routes["web"] = RouteOverride{Exposure: "everyone"} }, "routes.web.exposure"},
		{func(in *Instance) { in.Routes["web"] = RouteOverride{Port: 80} }, "routes.web.port"},
		{func(in *Instance) { in.Routes["web"] = RouteOverride{PublicPaths: []string{"/nope"}} }, "routes.web.public_paths"},
		{func(in *Instance) { in.Ports["nope"] = PortOverride{} }, "ports.nope: package has no such port"},
		{func(in *Instance) { in.Ports["ssh"] = PortOverride{HostPort: 22} }, "ports.ssh.host_port"},
		{func(in *Instance) { in.Ports["ssh"] = PortOverride{Exposure: "everyone"} }, "ports.ssh.exposure"},
		{func(in *Instance) { in.Settings["nope"] = 1 }, "settings.nope: package has no such setting"},
		// Secrets never go in the instance file (it isn't private).
		{func(in *Instance) { in.Settings["admin_token"] = "x" }, "settings.admin_token: secrets are stored"},
		{func(in *Instance) { in.Settings["signups_allowed"] = "yes" }, "settings.signups_allowed: expected a bool"},
	} {
		in := good()
		tc.change(in)
		if err := in.Validate(p); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q, got %v", tc.want, err)
		}
	}
}

// A service's ID is its package's name, or that, a dash and more.
func TestNames(t *testing.T) {
	p := &pkgspec.Package{Name: "forgejo", Version: "1"}
	for name, ok := range map[string]bool{"": true, "forgejo": true, "forgejo-work": true, "forgejo-2": true,
		"gitea": false, "forgejowork": false, "forgejo-": false, "forgejo--work": false, "Forgejo-work": false,
		"forgejo-" + strings.Repeat("x", MaxName): false} {
		in := &Instance{Name: name, Package: "forgejo", Version: "1", Updates: UpdatesNotify}
		if err := in.Validate(p); (err == nil) != ok {
			t.Errorf("%q: %v", name, err)
		}
	}
}
