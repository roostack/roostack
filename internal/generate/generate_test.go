package generate

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
)

var update = flag.Bool("update", false, "rewrite golden files")

func testLab() config.Lab {
	lab := config.Default()
	lab.Domain = "lab.example.com"
	lab.HasGPU = true
	return lab
}

func TestGolden(t *testing.T) {
	cases := []struct{ name, pkg, instance string }{
		{"vaultwarden", "vaultwarden", "vaultwarden.instance.yaml"},
		{"immich", "immich", "immich.instance.yaml"},
		{"immich-no-ml", "immich", "immich-no-ml.instance.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := pkgspec.Load(filepath.Join("..", "..", "docs", "examples", tc.pkg+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			in, err := instance.Load(filepath.Join("testdata", tc.instance))
			if err != nil {
				t.Fatal(err)
			}
			out, err := Service(testLab(), p, in)
			if err != nil {
				t.Fatal(err)
			}
			got := Dump(out)
			golden := filepath.Join("testdata", "golden", tc.name+".txt")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to create)", err)
			}
			if got != string(want) {
				t.Errorf("output differs from %s (run with -update and review the diff):\n%s", golden, got)
			}
		})
	}
}

func TestErrors(t *testing.T) {
	p, err := pkgspec.Load(filepath.Join("..", "..", "docs", "examples", "immich.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   instance.Instance
		want string
	}{
		{"wrong version", instance.Instance{Package: "immich", Version: "0", Updates: "notify"}, "version"},
		{"unknown setting", instance.Instance{Package: "immich", Version: p.Version, Updates: "notify",
			Settings: map[string]any{"nope": true}}, "no such setting"},
		{"wrong setting type", instance.Instance{Package: "immich", Version: p.Version, Updates: "notify",
			Settings: map[string]any{"machine_learning": "yes"}}, "expected a bool"},
		{"bad subdomain", instance.Instance{Package: "immich", Version: p.Version, Updates: "notify",
			Routes: map[string]instance.RouteOverride{"web": {Subdomain: "Not Valid"}}}, "not a valid DNS label"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Service(testLab(), p, &tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestTemplateCannotReadSecrets(t *testing.T) {
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: app
title: App
version: "1"
settings:
  - { key: token, type: secret, generate: true }
containers:
  web:
    image: example/app:1
    env: { LEAK: "{{ .Settings.token }}" }
`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Service(testLab(), p, &instance.Instance{Package: "app", Version: "1", Updates: "notify"})
	if err == nil {
		t.Fatal("expected an error: secret settings must not be available to templates")
	}
}

// The lab-wide units, private and with something public (then CrowdSec
// runs, Caddy logs requests for it and gets the bouncer's key: SPEC.md
// §6.11).
func TestSystemGolden(t *testing.T) {
	for _, public := range []bool{false, true} {
		name := "system.txt"
		if public {
			name = "system-public.txt"
		}
		t.Run(name, func(t *testing.T) { systemGolden(t, public, name) })
	}
}

func systemGolden(t *testing.T, public bool, name string) {
	lab := testLab()
	lab.Email = "admin@example.com"
	lab.DNS.Provider = "cloudflare"
	lab.Public.NoSharing = public // the opt-out shows in the public output
	out, err := System(lab, public)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, f := range out.Quadlets {
		b.WriteString("==> quadlet/" + f.Name + "\n" + f.Content + "\n")
	}
	b.WriteString("==> caddy/" + out.Caddyfile.Name + "\n" + out.Caddyfile.Content)
	for _, f := range out.Files {
		b.WriteString("\n==> file " + f.Name + "\n" + f.Content)
	}
	b.WriteString("\n==> secrets\n")
	for _, s := range out.Secrets {
		fmt.Fprintf(&b, "%s generate=%v\n", s.Name, s.Generate)
	}
	b.WriteString("==> dirs\n" + strings.Join(out.Dirs, "\n") + "\n==> units\n" + strings.Join(out.Units, "\n") + "\n")
	got := b.String()

	golden := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s:\n%s", golden, got)
	}
}

func TestSystemRoute53(t *testing.T) {
	lab := testLab()
	lab.DNS.Provider = "route53"
	lab.DNS.Fields = []string{"access_key_id", "secret_access_key", "region"}
	out, err := System(lab, false)
	if err != nil {
		t.Fatal(err)
	}
	unit := out.Quadlets[0].Content
	for _, want := range []string{
		"Secret=roostack-caddy-dns-access_key_id,type=env,target=DNS_ACCESS_KEY_ID",
		"Secret=roostack-caddy-dns-secret_access_key,type=env,target=DNS_SECRET_ACCESS_KEY",
		"Secret=roostack-caddy-dns-region,type=env,target=DNS_REGION",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("caddy unit missing %s", want)
		}
	}
	if !strings.Contains(out.Caddyfile.Content, "\t\tdns route53 {\n\t\t\taccess_key_id {env.DNS_ACCESS_KEY_ID}") {
		t.Errorf("Caddyfile:\n%s", out.Caddyfile.Content)
	}
	if len(out.Secrets) != 3 {
		t.Errorf("secrets = %v", out.Secrets)
	}
	lab.DNS.Provider = "nope"
	if _, err := System(lab, false); err == nil {
		t.Error("unknown provider must be an error")
	}
}

// TestCaddyAdaptAllProviders checks the generated Caddyfile for every DNS
// provider with a real Caddy built with all plugins (build/caddy). Only runs
// when ROOSTACK_CADDY_BIN is set.
func TestCaddyAdaptAllProviders(t *testing.T) {
	bin := os.Getenv("ROOSTACK_CADDY_BIN")
	if bin == "" {
		t.Skip("set ROOSTACK_CADDY_BIN to a caddy binary with all DNS plugins")
	}
	for _, p := range dns.Providers() {
		t.Run(p.ID, func(t *testing.T) {
			lab := testLab()
			lab.DNS.Provider = p.ID
			var env []string
			for _, f := range p.Fields {
				lab.DNS.Fields = append(lab.DNS.Fields, f.Key)
				// Realistically shaped values: some plugins check the format.
				v := "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-Ab"
				switch f.Key {
				case "region":
					v = "us-east-1"
				case "client_ip":
					v = "203.0.113.10"
				case "user":
					v = "someone"
				}
				env = append(env, dns.EnvName(f.Key)+"="+v)
			}
			out, err := System(lab, false)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte(out.Caddyfile.Content), 0o644)
			cmd := exec.Command(bin, "adapt", "--config", "Caddyfile")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), env...)
			got, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("caddy adapt: %v\n%s\n--- Caddyfile:\n%s", err, got, out.Caddyfile.Content)
			}
			// validate also provisions the DNS module with the env values.
			v := exec.Command(bin, "validate", "--config", "Caddyfile")
			v.Dir = dir
			v.Env = append(os.Environ(), env...)
			if vout, err := v.CombinedOutput(); err != nil {
				t.Errorf("caddy validate: %v\n%s", err, lastLines(string(vout), 3))
			}
			if !strings.Contains(string(got), `"name":"`+p.ID+`"`) {
				t.Errorf("adapted config doesn't use the %s provider:\n%s", p.ID, got)
			}
		})
	}
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func TestTimezone(t *testing.T) {
	lab := testLab()
	lab.Timezone = "Australia/Melbourne"
	lab.DNS.Provider = "cloudflare" // System needs one
	p, err := pkgspec.Load(filepath.Join("..", "..", "docs", "examples", "immich.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "immich.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Service(lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range out.Quadlets {
		if strings.HasSuffix(f.Name, ".container") && !strings.Contains(f.Content, "\nTimezone=Australia/Melbourne\n") {
			t.Errorf("%s has no Timezone", f.Name)
		}
	}
	sys, err := System(lab, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sys.Quadlets[0].Content, "Timezone=Australia/Melbourne") {
		t.Error("Caddy has no Timezone")
	}
}

func TestCheck(t *testing.T) {
	for _, name := range []string{"vaultwarden", "forgejo", "immich"} {
		p, err := pkgspec.Load(filepath.Join("..", "..", "docs", "examples", name+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if err := Check(p); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The Forgejo mistake that only showed at install time.
	p, err := pkgspec.Parse([]byte(`schema: roostack/v1alpha1
name: app
title: App
version: "1"
settings:
  - { key: signups, type: bool, default: true }
containers:
  web:
    image: x
    env: { DISABLE: '{{ if eq .Settings.signups "true" }}no{{ end }}' }
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(p); err == nil || !strings.Contains(err.Error(), "incompatible types") {
		t.Errorf("want the bool/string mismatch, got %v", err)
	}
}

func TestNodeAndProxy(t *testing.T) {
	p, err := pkgspec.Load(filepath.Join("..", "..", "docs", "examples", "vaultwarden.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "vaultwarden.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lab := testLab()
	lab.Role, lab.OverlayAddr = config.RoleNode, "100.96.0.2"
	out, err := Service(lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.CaddySite != nil || len(out.Hosts) == 0 {
		t.Error("a node has no Caddy site but still knows its hostnames")
	}
	unit := out.Quadlets[len(out.Quadlets)-1].Content
	for _, f := range out.Quadlets {
		if strings.Contains(f.Content, "PublishPort=") {
			unit = f.Content
		}
	}
	if !strings.Contains(unit, "PublishPort=100.96.0.2:") || strings.Contains(unit, "127.0.0.1:") {
		t.Errorf("a node publishes routes on its network address:\n%s", unit)
	}
	if sys, err := System(lab, false); err != nil || len(sys.Quadlets) != 0 || sys.Caddyfile.Name != "" {
		t.Errorf("a node runs no Caddy: %+v %v", sys, err)
	}

	proxy, err := ProxySite(testLab(), p, in, "100.96.0.2")
	if err != nil {
		t.Fatal(err)
	}
	if proxy.CaddySite == nil || !strings.Contains(proxy.CaddySite.Content, "reverse_proxy 100.96.0.2:") ||
		len(proxy.Quadlets) != 0 || len(proxy.Ports) != 0 || len(proxy.Hosts) == 0 {
		t.Errorf("proxy site: %+v", proxy)
	}
}
