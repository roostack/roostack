package generate

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
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
		// A second copy: everything named after it, not the package.
		{"immich-second", "immich", "immich-second.instance.yaml"},
		// Single sign-on on (SPEC.md §5.2): an OIDC app, and a walled one.
		{"mealie-sso", "mealie", "mealie-sso.instance.yaml"},
		{"stirling-pdf-sso", "stirling-pdf", "stirling-pdf-sso.instance.yaml"},
		// An app set by a command, in its sso hook (Forgejo).
		{"forgejo-sso", "forgejo", "forgejo-sso.instance.yaml"},
		// A config file with secrets (SPEC.md §8.1): placeholders only.
		{"pingvin-share-x-sso", "pingvin-share-x", "pingvin-share-x-sso.instance.yaml"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := pkgspec.Load(pkgtest.File(tc.pkg))
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

// On a lab on the internet, a private address is for this server only (an
// SSH tunnel ends there), not the cloud provider's shared network
// (SPEC.md §6.12).
func TestPrivateOnInternet(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "vaultwarden.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lab := testLab()
	lab.Network, lab.HostIP = config.NetworkInternet, "203.0.113.5"
	out, err := Service(lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	site := out.CaddySite.Content
	if !strings.Contains(site, "\tremote_ip 127.0.0.1/8 ::1 203.0.113.5 169.254.1.3\n") || strings.Contains(site, "private_ranges") {
		t.Errorf("private route on the internet:\n%s", site)
	}
}

// TestNoWAF: AppSec checks a public service's routes unless the Owner
// turned it off for the service (SPEC.md §6.10 item 4).
func TestNoWAF(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("immich"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "immich-no-ml.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Service(testLab(), p, in)
	if err != nil || !strings.Contains(out.CaddySite.Content, "appsec @immich-web-outside") {
		t.Fatalf("on by default: %v\n%s", err, out.CaddySite.Content)
	}
	in.NoWAF = true
	if out, err = Service(testLab(), p, in); err != nil || strings.Contains(out.CaddySite.Content, "appsec") ||
		!strings.Contains(out.CaddySite.Content, "crowdsec") {
		t.Errorf("turned off, CrowdSec's bans stay: %v\n%s", err, out.CaddySite.Content)
	}
}

// TestPaused: a service whose public access is paused (SPEC.md §6.16), by
// its own pause, the lab's for everything, or the lab's for something it's
// built with, answers visitors from outside with the paused page, opens
// its public ports to home only, and no longer needs the sign-in page
// public; a pause of something else leaves it as it was.
func TestPaused(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "forgejo-sso.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	on := true
	in.Routes["web"] = instance.RouteOverride{Port: in.Routes["web"].Port, Exposure: pkgspec.ExposurePublic}
	in.Ports = map[string]instance.PortOverride{"ssh": {Enabled: &on, Exposure: pkgspec.ExposurePublic}}
	since := config.Pause{Since: time.Date(2026, 10, 23, 9, 14, 0, 0, time.UTC), By: "tom"}
	paused := func(t *testing.T, lab config.Lab, in *instance.Instance, want bool) {
		t.Helper()
		out, err := Service(lab, p, in)
		if err != nil {
			t.Fatal(err)
		}
		site := out.CaddySite.Content
		if got := strings.Contains(site, "error @forgejo-web-outside 503"); got != want {
			t.Errorf("paused route = %v, want %v:\n%s", got, want, site)
		}
		if len(out.HostPorts) != 1 || (out.HostPorts[0].Exposure == pkgspec.ExposurePrivate) != want {
			t.Errorf("ports: %+v", out.HostPorts)
		}
		if !out.Public || out.SSOPublic == want {
			t.Errorf("public %v, sign-in page public %v: CrowdSec stays, and the sign-in page follows", out.Public, out.SSOPublic)
		}
		if want != Paused(lab, p, in) {
			t.Errorf("Paused = %v", !want)
		}
	}
	paused(t, testLab(), in, false)
	lab := testLab()
	lab.Public.Pause.BuiltWith = map[string]config.Pause{"php": since}
	paused(t, lab, in, false)
	lab.Public.Pause.BuiltWith["postgres"] = since // from its database
	paused(t, lab, in, true)
	lab = testLab()
	lab.Public.Pause.All = &since
	paused(t, lab, in, true)
	in.PublicPause = &since
	paused(t, testLab(), in, true)
}

// A service whose install hooks haven't succeeded keeps its public
// addresses and ports private (SPEC.md §8.1, "Public addresses wait for
// setup"), and says what it will make public.
func TestPublicAfterSetup(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "forgejo-sso.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	on := true
	in.Routes["web"] = instance.RouteOverride{Port: in.Routes["web"].Port, Exposure: pkgspec.ExposurePublic}
	in.Ports = map[string]instance.PortOverride{"ssh": {Enabled: &on, Exposure: pkgspec.ExposurePublic}}
	check := func(settingUp bool) {
		t.Helper()
		in.SettingUp = settingUp
		out, err := Service(testLab(), p, in)
		if err != nil {
			t.Fatal(err)
		}
		site := out.CaddySite.Content
		if got := strings.Contains(site, "abort @forgejo-web-external"); got != settingUp {
			t.Errorf("private route = %v, want %v:\n%s", got, settingUp, site)
		}
		wantExposure := map[bool]string{true: pkgspec.ExposurePrivate, false: pkgspec.ExposurePublic}[settingUp]
		if len(out.HostPorts) != 1 || out.HostPorts[0].Exposure != wantExposure {
			t.Errorf("ports: %+v", out.HostPorts)
		}
		if out.Public == settingUp || out.SSOPublic == settingUp {
			t.Errorf("public %v, sign-in page public %v, while setting up %v", out.Public, out.SSOPublic, settingUp)
		}
		_, routes := Values(testLab(), p, in)
		if rc := routes["web"]; rc.AfterSetup != settingUp || rc.Exposure != wantExposure {
			t.Errorf("route = %+v", rc)
		}
		if pc := ResolvePorts(p, in)["ssh"]; pc.AfterSetup != settingUp || pc.Exposure != wantExposure {
			t.Errorf("port = %+v", pc)
		}
	}
	check(true)
	check(false)
	// A private address has nothing to wait for.
	in.Routes["web"] = instance.RouteOverride{Port: in.Routes["web"].Port}
	in.SettingUp = true
	if _, routes := Values(testLab(), p, in); routes["web"].AfterSetup {
		t.Error("a private address isn't waiting to be public")
	}
}

func TestErrors(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("immich"))
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

// Each way past the package policy the security review of 2026-10-16
// found (H2) is refused: one the unit file wouldn't read back as written
// by pkgspec (Validate and Risks) and by the generator (quadlet.Validate);
// a line that's valid but reaches beyond the service by the policy.
func TestPassthroughBypasses(t *testing.T) {
	tests := []struct {
		name, section, key string
		value              any
		// syntax: the unit file wouldn't read it back as written.
		syntax bool
	}{
		{"space after the key", "Container", "Volume ", "/var/lib/roostack:/lab", true},
		{"= in the key", "Container", "Volume=/:/host", "rw", true},
		{"newline in the key", "Container", "ShmSize=1m\nVolume", "/:/host", true},
		{"newline in the value", "Container", "ShmSize", "1m\nVolume=/:/host", true},
		{"backslash ending the value", "Container", "ShmSize", `1m\`, true},
		{"containers.conf module", "Container", "ContainersConfModule", "/srv/roostack/services/vaultwarden/x.conf", false},
		{"Podman's environment", "Service", "Environment", "CONTAINERS_CONF_OVERRIDE=/srv/roostack/services/vaultwarden/x.conf", false},
		{"output to a file", "Service", "StandardOutput", "append:/var/lib/roostack/lab.yaml", false},
		{"conflicts", "Unit", "Conflicts", "roostack-caddy.service", false},
		{"requires", "Unit", "Requires", "roostack-other-web.service", false},
		{"on failure", "Unit", "OnFailure", "roostack-caddy.service", false},
		{"user 00", "Container", "User", "00", false},
		{"two mounts on a line", "Container", "Mount", "type=tmpfs,destination=/t type=bind,source=/srv,destination=/srv", false},
		{"two labels on a line", "Container", "Label", "a=b io.roostack.service=other", false},
		{"unit named after Caddy", "Container", "ServiceName", "roostack-caddy", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := pkgspec.Load(pkgtest.File("vaultwarden"))
			if err != nil {
				t.Fatal(err)
			}
			in, err := instance.Load(filepath.Join("testdata", "vaultwarden.instance.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			p.Containers["app"].Quadlet = pkgspec.Quadlet{tt.section: {tt.key: tt.value}}
			if risks := p.Risks(); len(risks) != 1 {
				t.Errorf("Risks = %q, want one problem", risks)
			}
			err = p.Validate()
			if tt.syntax != (err != nil) {
				t.Errorf("Validate() = %v, want an error %v", err, tt.syntax)
			}
			_, err = Service(testLab(), p, in)
			if tt.syntax != (err != nil) {
				t.Errorf("Service() = %v, want an error %v", err, tt.syntax)
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

// .Lab.TimeZone is the server's time zone, or UTC when it isn't known.
func TestTemplateTimeZone(t *testing.T) {
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web:
    image: example/app:1
    env: { APP_TIME_ZONE: "{{ .Lab.TimeZone }}" }
`))
	if err != nil {
		t.Fatal(err)
	}
	for tz, want := range map[string]string{"Australia/Melbourne": "Australia/Melbourne", "": "UTC"} {
		lab := testLab()
		lab.Timezone = tz
		out, err := Service(lab, p, &instance.Instance{Package: "app", Version: "1", Updates: "notify"})
		if err != nil {
			t.Fatal(err)
		}
		var c string
		for _, q := range out.Quadlets {
			if q.Name == "roostack-app-web.container" {
				c = q.Content
			}
		}
		if !strings.Contains(c, "Environment=APP_TIME_ZONE="+want+"\n") {
			t.Errorf("time zone %q: container lacks APP_TIME_ZONE=%s:\n%s", tz, want, c)
		}
	}
}

// dbURLPackage asks for its database as a URL, in two containers.
const dbURLPackage = `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
databases:
  main: { type: postgres }
containers:
  web:
    image: example/app:1
    secrets: { DATABASE_URL: databases.main.url, DB_PASSWORD: databases.main.password }
  worker:
    image: example/app:1
    secrets: { DATABASE_URL: databases.main.url }
`

// databases.<name>.url is a secret of its own, made from the password
// (SPEC.md §8.1), once however many containers ask for it.
func TestDatabaseURLSecret(t *testing.T) {
	p, err := pkgspec.Parse([]byte(dbURLPackage))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Service(testLab(), p, &instance.Instance{Package: "app", Version: "1", Updates: "notify"})
	if err != nil {
		t.Fatal(err)
	}
	var urls []Secret
	for _, s := range out.Secrets {
		if s.DBURL != nil {
			urls = append(urls, s)
		}
	}
	if len(urls) != 1 || urls[0].Name != "roostack-app-db-main-url" || *urls[0].DBURL != (DBURL{
		PasswordSecret: "roostack-app-db-main-password", Scheme: "postgresql",
		User: "app", Host: "roostack-app-db-main", Port: 5432, Name: "app"}) {
		t.Fatalf("URL secrets = %+v", urls)
	}
	var app string
	for _, q := range out.Quadlets {
		if q.Name == "roostack-app-web.container" {
			app = q.Content
		}
	}
	for _, want := range []string{"Secret=roostack-app-db-main-url,type=env,target=DATABASE_URL",
		"Secret=roostack-app-db-main-password,type=env,target=DB_PASSWORD"} {
		if !strings.Contains(app, want) {
			t.Errorf("container lacks %q:\n%s", want, app)
		}
	}
	// The password is escaped in the URL.
	if got := urls[0].DBURL.URL("p@ss/w:rd"); got != "postgresql://app:p%40ss%2Fw%3Ard@roostack-app-db-main:5432/app" {
		t.Errorf("URL = %s", got)
	}
}

// The lab-wide units, private and with something public (then CrowdSec
// runs, Caddy logs requests for it and gets the bouncer's key: SPEC.md
// §6.11).
//
// On the internet (SPEC.md §6.12), Roostack's own pages are public too, and
// private routes are for this server only (an SSH tunnel).
func TestSystemGolden(t *testing.T) {
	for _, c := range []struct {
		name             string
		public, internet bool
	}{{"system.txt", false, false}, {"system-public.txt", true, false}, {"system-internet.txt", true, true}} {
		t.Run(c.name, func(t *testing.T) { systemGolden(t, c.public, c.internet, c.name) })
	}
}

func systemGolden(t *testing.T, public, internet bool, name string) {
	lab := testLab()
	lab.Email = "admin@example.com"
	lab.DNS.Provider = "cloudflare"
	lab.Public.NoSharing = public && !internet // the opt-out shows in the public output
	if internet {
		lab.Network, lab.HostIP = config.NetworkInternet, "203.0.113.5"
	}
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
	for _, p := range dns.All() {
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
	p, err := pkgspec.Load(pkgtest.File("immich"))
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
		p, err := pkgspec.Load(pkgtest.File(name))
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
	p, err := pkgspec.Load(pkgtest.File("vaultwarden"))
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

// Before the lab has an address, a service runs on its local port with no
// web address, and Caddy doesn't run (SPEC.md §6.6).
func TestNoAddressYet(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "vaultwarden.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lab := testLab()
	lab.Domain = ""
	out, err := Service(lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.CaddySite != nil || len(out.Hosts) != 0 || len(out.Ports) == 0 || len(out.Quadlets) == 0 {
		t.Errorf("site %v, hosts %v, ports %v, %d quadlets", out.CaddySite, out.Hosts, out.Ports, len(out.Quadlets))
	}
	_, routes := Values(lab, p, in)
	// Its address is the local one an SSH tunnel reaches, so apps that need
	// to know it (Vaultwarden's DOMAIN) still start.
	for name, r := range routes {
		if r.Host != "localhost" || r.URL != fmt.Sprintf("http://localhost:%d", r.Port) || r.Port == 0 {
			t.Errorf("route %s = %+v", name, r)
		}
	}
	if q := out.Quadlets[len(out.Quadlets)-1].Content; !strings.Contains(Dump(out), "DOMAIN=http://localhost:") {
		t.Errorf("Vaultwarden's DOMAIN:\n%s", q)
	}
	sys, err := System(lab, false)
	if err != nil || len(sys.Quadlets) != 0 || len(sys.Units) != 0 || sys.Caddyfile.Content != "" {
		t.Errorf("system without an address: %+v, %v", sys, err)
	}
}

// After switching address, the old one keeps working for a while: a
// second site with its own certificate, and every route answering to both
// names; once it's over, nothing of it is left (SPEC.md §6.6).
func TestOldAddress(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "vaultwarden.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lab := testLab()
	lab.Domain = "smith.family"
	lab.DNS.Provider = "cloudflare"
	lab.OldAddress = &config.OldAddress{Domain: "lab.example.com", Until: time.Now().Add(time.Hour),
		DNS: config.DNS{Provider: "porkbun", Fields: []string{"api_key", "api_secret_key"}}}
	out, err := Service(lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.CaddySite.Content, "host passwords.smith.family passwords.lab.example.com\n") ||
		slices.Contains(out.Hosts, "passwords.lab.example.com") {
		t.Errorf("site:\n%s\nhosts %v", out.CaddySite.Content, out.Hosts)
	}
	if d := Dump(out); !strings.Contains(d, "DOMAIN=https://passwords.smith.family") {
		t.Errorf("the app uses the new address:\n%s", d)
	}
	sys, err := System(lab, false)
	if err != nil {
		t.Fatal(err)
	}
	cf := sys.Caddyfile.Content
	for _, want := range []string{"*.smith.family {", "*.lab.example.com {", "{env.DNS_OLD_API_KEY}",
		"host roost.lab.example.com", "host roost.smith.family"} {
		if !strings.Contains(cf, want) {
			t.Errorf("Caddyfile missing %q:\n%s", want, cf)
		}
	}
	if q := Dump(&Output{Quadlets: sys.Quadlets}); !strings.Contains(q, "Secret=roostack-caddy-dns-old-api_key,type=env,target=DNS_OLD_API_KEY") {
		t.Errorf("old provider's secrets:\n%s", q)
	}

	lab.OldAddress.Until = time.Now().Add(-time.Minute)
	out, _ = Service(lab, p, in)
	sys, _ = System(lab, false)
	if strings.Contains(out.CaddySite.Content+sys.Caddyfile.Content, "lab.example.com") {
		t.Errorf("after it ends:\n%s\n%s", out.CaddySite.Content, sys.Caddyfile.Content)
	}
}

// A roost.ac name (SPEC.md §6.6): Roostack at the name itself, in the
// same site as the wildcard, and certificates through Roostack Cloud's
// acme-dns.
func TestRoostAcAddress(t *testing.T) {
	lab := testLab()
	lab.Domain = "smiths.roost.ac"
	lab.DNS = config.DNS{Provider: config.ProviderCloud, Fields: []string{"password"},
		Values: map[string]string{"server_url": "https://acme.roost.ac", "username": "acme_x", "subdomain": "abc", "records_url": "https://acme.roost.ac/v1/records"},
		Zone:   "smiths.roost.ac", ManageRecords: true}
	sys, err := System(lab, false)
	if err != nil {
		t.Fatal(err)
	}
	cf := sys.Caddyfile.Content
	// Only the password is a placeholder: acmedns reads placeholders in
	// nothing else.
	for _, want := range []string{"*.smiths.roost.ac, smiths.roost.ac {", "dns acmedns {", `username "acme_x"`,
		"password {env.DNS_PASSWORD}", `subdomain "abc"`, `server_url "https://acme.roost.ac"`, "@roostack-admin host smiths.roost.ac\n"} {
		if !strings.Contains(cf, want) {
			t.Errorf("Caddyfile missing %q:\n%s", want, cf)
		}
	}
	if strings.Contains(cf, "records_url") || strings.Contains(cf, "roost.smiths") {
		t.Errorf("Caddyfile:\n%s", cf)
	}
	if q := Dump(&Output{Quadlets: sys.Quadlets}); !strings.Contains(q, "Secret=roostack-caddy-dns-password,type=env,target=DNS_PASSWORD") ||
		strings.Contains(q, "dns-username") {
		t.Errorf("only the password is a secret:\n%s", q)
	}
	// A value that could break out of Caddy's config is refused.
	lab.DNS.Values["username"] = `x" }`
	if _, err := System(lab, false); err == nil {
		t.Error("an unsafe value was written")
	}
}

// TestEdge: an edge server gets Roostack's Caddy (built the same way) with
// only TCP 80 and 443, no certificates or DNS logins, and the edge config.
func TestEdge(t *testing.T) {
	lab := testLab()
	lab.Role = config.RoleNode
	out, err := Edge(lab, caddy.EdgeConfig{Hosts: []string{"photos.lab.example.com"}, Upstream: "100.96.0.1:443"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Quadlets) != 2 || out.Quadlets[0].Name != CaddyUnit+".container" || out.Quadlets[1].Name != CaddyUnit+".build" {
		t.Fatalf("quadlets: %+v", out.Quadlets)
	}
	c := out.Quadlets[0].Content
	for _, want := range []string{"PublishPort=80:80\n", "PublishPort=443:443\n", "Network=pasta\n", "Image=" + CaddyUnit + ".build\n"} {
		if !strings.Contains(c, want) {
			t.Errorf("missing %q in\n%s", want, c)
		}
	}
	for _, not := range []string{"/udp", "Secret=", "map-host-loopback"} {
		if strings.Contains(c, not) {
			t.Errorf("has %q:\n%s", not, c)
		}
	}
	if !strings.Contains(out.Caddyfile.Content, "tls sni photos.lab.example.com") || len(out.Secrets) != 0 {
		t.Errorf("caddyfile:\n%s", out.Caddyfile.Content)
	}
}

// A service's output lists its images (downloaded before an update) and
// how to check it works afterwards: an HTTP check through the route's
// published port (SPEC.md §7, ASSUMPTIONS F1).
func TestImagesAndChecks(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "vaultwarden.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Service(testLab(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Images) != 1 || out.Images[0] != p.Containers["app"].Image {
		t.Errorf("images %v", out.Images)
	}
	want := HealthCheck{Container: "roostack-vaultwarden-app",
		HTTP: fmt.Sprintf("http://127.0.0.1:%d/alive", in.Routes["web"].Port)}
	if len(out.Checks) != 1 || out.Checks[0] != want {
		t.Errorf("checks %+v, want %+v", out.Checks, want)
	}
}

// MySQL and MariaDB run as Postgres does: Roostack's pinned image, the
// app's user and database, its password from a secret, a random root
// password nobody keeps, a health check over TCP, and a dump before each
// backup (SPEC.md §8.1).
func TestMySQLDatabases(t *testing.T) {
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: app
title: App
version: "1"
databases:
  main: { type: mysql }
  extra: { type: mariadb }
containers:
  web:
    image: example/app:1
    env: { DB: "{{ .Databases.main.Host }}:{{ .Databases.main.Port }}/{{ .Databases.main.Name }} as {{ .Databases.main.User }}", DB2: "{{ .Databases.extra.Name }}" }
    secrets: { DB_PASSWORD: databases.main.password }
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Service(testLab(), p, &instance.Instance{Package: "app", Version: "1", Updates: "notify"})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, q := range out.Quadlets {
		files[q.Name] = q.Content
	}
	for unit, want := range map[string][]string{
		"roostack-app-db-main.container": {"Image=" + DefaultMySQLImage, "Environment=MYSQL_USER=app", "Environment=MYSQL_DATABASE=app",
			"Environment=MYSQL_PASSWORD_FILE=/run/secrets/db-password", "Environment=MYSQL_RANDOM_ROOT_PASSWORD=yes",
			"Secret=roostack-app-db-main-password,type=mount,target=db-password", "/db/main:/var/lib/mysql",
			"HealthCmd=mysqladmin ping -h 127.0.0.1 --silent", "Notify=healthy"},
		"roostack-app-db-extra.container": {"Image=" + DefaultMariaDBImage, "Environment=MARIADB_USER=app_extra",
			"Environment=MARIADB_AUTO_UPGRADE=1", "Environment=MARIADB_RANDOM_ROOT_PASSWORD=yes", "HealthCmd=mariadb-admin ping -h 127.0.0.1 --silent"},
		"roostack-app-web.container": {"Environment=\"DB=roostack-app-db-main:3306/app as app\"", "Environment=DB2=app_extra",
			"Secret=roostack-app-db-main-password,type=env,target=DB_PASSWORD"},
	} {
		for _, w := range want {
			if !strings.Contains(files[unit], w) {
				t.Errorf("%s lacks %q:\n%s", unit, w, files[unit])
			}
		}
	}
	if strings.Contains(files["roostack-app-db-main.container"], "AUTO_UPGRADE") {
		t.Error("MySQL upgrades by itself; MARIADB_AUTO_UPGRADE is for MariaDB")
	}
	var steps []string
	for _, s := range out.Backup.Prepare {
		steps = append(steps, s.Type+" "+s.Container+" "+s.User+" "+s.Database+" "+filepath.Base(s.Target))
	}
	slices.Sort(steps)
	if want := []string{"mariadb roostack-app-db-extra app_extra app_extra extra.sql", "mysql roostack-app-db-main app app main.sql"}; !slices.Equal(steps, want) {
		t.Errorf("prepare steps = %q, want %q", steps, want)
	}
}

// A MySQL or MariaDB database's more schemas are named after it, reach
// templates, are made by the app's user each time its container starts
// (the first-start script having let it), and are dumped with it.
func TestMySQLSchemas(t *testing.T) {
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: seafile
title: Seafile
version: "1"
databases:
  main: { type: mariadb, schemas: [ccnet, seahub] }
  other: { type: mysql }
containers:
  web:
    image: example/app:1
    env: { SEAFILE: "{{ .Databases.main.Name }}", CCNET: "{{ .Databases.main.Schemas.ccnet }}", SEAHUB: "{{ .Databases.main.Schemas.seahub }}" }
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Service(testLab(), p, &instance.Instance{Package: "seafile", Version: "1", Updates: "notify"})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, q := range out.Quadlets {
		files[q.Name] = q.Content
	}
	for unit, want := range map[string][]string{
		"roostack-seafile-db-main.container": {"Volume=" + MySQLInitDir + ":/docker-entrypoint-initdb.d:ro",
			`ExecStartPost=/usr/bin/podman exec roostack-seafile-db-main sh -c 'MYSQL_PWD="$$(cat /run/secrets/db-password)" exec mariadb ` +
				`-h 127.0.0.1 -u seafile -e "CREATE DATABASE IF NOT EXISTS seafile_ccnet; CREATE DATABASE IF NOT EXISTS seafile_seahub"'`},
		// Without schemas, the script still runs, for a schema a later
		// version adds; nothing to make at start.
		"roostack-seafile-db-other.container": {"Volume=" + MySQLInitDir + ":/docker-entrypoint-initdb.d:ro"},
		"roostack-seafile-web.container":      {"Environment=SEAFILE=seafile", "Environment=CCNET=seafile_ccnet", "Environment=SEAHUB=seafile_seahub"},
	} {
		for _, w := range want {
			if !strings.Contains(files[unit], w) {
				t.Errorf("%s lacks %q:\n%s", unit, w, files[unit])
			}
		}
	}
	if strings.Contains(files["roostack-seafile-db-other.container"], "ExecStartPost") {
		t.Error("a database without schemas has nothing to make")
	}
	for _, s := range out.Backup.Prepare {
		want := map[string][]string{"seafile": {"seafile_ccnet", "seafile_seahub"}, "seafile_other": nil}[s.Database]
		if !slices.Equal(s.Schemas, want) {
			t.Errorf("%s: schemas %q, want %q", s.Database, s.Schemas, want)
		}
	}
	// A template naming a schema the database hasn't fails.
	p.Containers["web"].Env["X"] = "{{ .Databases.main.Schemas.nope }}"
	if _, err := Service(testLab(), p, &instance.Instance{Package: "seafile", Version: "1", Updates: "notify"}); err == nil {
		t.Error("an unknown schema rendered")
	}
}

// MySQL user names have at most 32 characters: a longer name keeps its
// start and a hash of the whole, the same every time.
func TestMySQLIdent(t *testing.T) {
	if got := mysqlIdent("ghost", "main"); got != "ghost" {
		t.Errorf("short: %q", got)
	}
	long := mysqlIdent("a-very-long-service-name-indeed", "reporting")
	if len(long) != 32 || !strings.HasPrefix(long, "a_very_long_service_nam_") || long != mysqlIdent("a-very-long-service-name-indeed", "reporting") {
		t.Errorf("long: %q", long)
	}
	if long == mysqlIdent("a-very-long-service-name-indeed", "reportinh") {
		t.Error("different names must stay different")
	}
}

// A container reading media libraries binds the service's view folder,
// read-only and rslave, so libraries come and go inside it (SPEC.md §8.1).
func TestLibrariesMount(t *testing.T) {
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: player
title: Player
version: "1"
storage:
  config: { class: critical }
containers:
  app:
    image: example/player:1
    mounts:
      - { storage: config, path: /config }
      - { libraries: true, path: /media }
`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Service(testLab(), p, &instance.Instance{Package: "player", Version: "1", Updates: "notify", Libraries: []string{"films"}})
	if err != nil {
		t.Fatal(err)
	}
	var app string
	for _, q := range out.Quadlets {
		if q.Name == "roostack-player-app.container" {
			app = q.Content
		}
	}
	if !strings.Contains(app, "Volume=/mnt/roostack-library-views/player:/media:ro,rslave\n") || !out.ReadsLibraries {
		t.Errorf("reads libraries %v:\n%s", out.ReadsLibraries, app)
	}
	for _, d := range out.Backup.Include {
		if strings.Contains(d, "library") {
			t.Errorf("libraries must never be backed up: %q", d)
		}
	}
}

// Libraries at their old paths (SPEC.md §8.4): at a shared parent, the
// second view folder goes there; otherwise each library is bound from the
// view folder at its own path, and named for its server's check.
func TestLibraryPaths(t *testing.T) {
	p, err := pkgspec.Parse([]byte(`
schema: roostack/v1alpha1
name: player
title: Player
version: "1"
storage:
  config: { class: critical }
containers:
  app:
    image: example/player:1
    mounts:
      - { storage: config, path: /config }
      - { libraries: true, path: /media }
`))
	if err != nil {
		t.Fatal(err)
	}
	app := func(paths map[string]string) (string, *Output) {
		t.Helper()
		out, err := Service(testLab(), p, &instance.Instance{Package: "player", Version: "1", Updates: "notify",
			Libraries: []string{"films", "tv"}, LibraryPaths: paths})
		if err != nil {
			t.Fatal(err)
		}
		for _, q := range out.Quadlets {
			if q.Name == "roostack-player-app.container" {
				return q.Content, out
			}
		}
		t.Fatal("no app container")
		return "", nil
	}
	got, out := app(map[string]string{"films": "/data/movies", "tv": "/data/tv"})
	if !strings.Contains(got, "Volume=/mnt/roostack-library-views/player:/media:ro,rslave\n"+
		"Volume=/mnt/roostack-library-views/player.at:/data:ro,rslave\n") || len(out.LibraryBinds) != 0 ||
		out.LibraryAt["movies"] != "films" || out.LibraryAt["tv"] != "tv" {
		t.Errorf("a shared parent: %v %v\n%s", out.LibraryAt, out.LibraryBinds, got)
	}
	got, out = app(map[string]string{"films": "/movies", "tv": "/tv"})
	if !strings.Contains(got, "Volume=/mnt/roostack-library-views/player/films:/movies:ro,rslave\n"+
		"Volume=/mnt/roostack-library-views/player/tv:/tv:ro,rslave\n") || out.LibraryAt != nil ||
		!slices.Equal(out.LibraryBinds, []string{"films", "tv"}) || !slices.Equal(out.LibraryUnits, []string{"roostack-player-app.service"}) {
		t.Errorf("at the top: %v %v\n%s", out.LibraryAt, out.LibraryBinds, got)
	}
	// Under /srv, a view folder would hide what the image keeps there.
	if _, out = app(map[string]string{"films": "/srv/movies", "tv": "/srv/tv"}); len(out.LibraryBinds) != 2 {
		t.Errorf("under /srv: %v %v", out.LibraryAt, out.LibraryBinds)
	}
	for paths, want := range map[string]string{
		"/etc/films": "one of the container's own folders",
		"/config/x":  "overlaps /config",
		"/media/x":   "overlaps /media",
		"films":      "full path",
		"/a b":       "full path",
		"/x/../etc":  "full path",
	} {
		_, err := Service(testLab(), p, &instance.Instance{Package: "player", Version: "1", Updates: "notify",
			Libraries: []string{"films"}, LibraryPaths: map[string]string{"films": paths}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", paths, want, err)
		}
	}
	_, err = Service(testLab(), p, &instance.Instance{Package: "player", Version: "1", Updates: "notify",
		Libraries: []string{"films", "tv"}, LibraryPaths: map[string]string{"films": "/data", "tv": "/data/tv"}})
	if err == nil || !strings.Contains(err.Error(), "where the library") {
		t.Errorf("nested libraries: %v", err)
	}
	_, err = Service(testLab(), p, &instance.Instance{Package: "player", Version: "1", Updates: "notify",
		LibraryPaths: map[string]string{"films": "/movies"}})
	if err == nil || !strings.Contains(err.Error(), "doesn't read that library") {
		t.Errorf("a library it doesn't read: %v", err)
	}
}

// Each route with an address gets a monitor (SPEC.md §6.15): the
// container's health path when the route leads to its port, or the
// package's monitor settings.
func TestRouteMonitors(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("vaultwarden"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "vaultwarden.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Service(testLab(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Monitors) != 1 || out.Monitors[0].Path != "/alive" || out.Monitors[0].Host != out.Hosts[0] || out.Monitors[0].Route != "web" {
		t.Errorf("monitors %+v (hosts %v)", out.Monitors, out.Hosts)
	}
	p.Routes["web"].Monitor = &pkgspec.RouteMonitor{Path: "/api/ping", Statuses: []int{200}}
	if out, err = Service(testLab(), p, in); err != nil {
		t.Fatal(err)
	}
	if m := out.Monitors[0]; m.Path != "/api/ping" || !slices.Equal(m.Statuses, []int{200}) {
		t.Errorf("with the package's monitor: %+v", m)
	}
}

// TestRefused: a service Roostack Cloud won't carry (config.Public.
// Refused) gets the same site as if the Owner hadn't made it public, and
// needs neither CrowdSec nor the sign-in page public.
func TestRefused(t *testing.T) {
	p, err := pkgspec.Load(pkgtest.File("forgejo"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := instance.Load(filepath.Join("testdata", "forgejo-sso.instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	private, err := Service(testLab(), p, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Routes["web"] = instance.RouteOverride{Port: in.Routes["web"].Port, Exposure: pkgspec.ExposurePublic}
	lab := testLab()
	lab.Public.Refused = map[string]string{in.ID(): "needs_approval"}
	refused, err := Service(lab, p, in)
	if err != nil {
		t.Fatal(err)
	}
	if refused.CaddySite.Content != private.CaddySite.Content || refused.Public || refused.SSOPublic {
		t.Errorf("refused: public %v, sign-in page public %v, site:\n%s\nwant:\n%s", refused.Public, refused.SSOPublic,
			refused.CaddySite.Content, private.CaddySite.Content)
	}
	for _, mon := range refused.Monitors {
		if mon.Public {
			t.Errorf("monitor %+v checks it from outside", mon)
		}
	}
	if public, _ := Service(testLab(), p, in); !public.Public {
		t.Error("not public without the refusal")
	}
}
