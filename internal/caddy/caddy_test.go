package caddy

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSiteRejectsInjection(t *testing.T) {
	for _, r := range []Route{
		{ID: "a", Host: "x.example.com } evil {", Upstream: "a:1"},
		{ID: "a", Host: "x.example.com", Upstream: "a:1\nabort"},
		{ID: "a", Host: "x.example.com", Upstream: "a:1", MaxBodySize: "1GB #"},
	} {
		if _, err := Site("svc", []Route{r}); err == nil {
			t.Errorf("expected an error for %+v", r)
		}
	}
}

// A public route keeps its private paths home-only, then checks CrowdSec
// and the rate limits, all inside one route block so they run in order
// (docs/ASSUMPTIONS.md P2).
func TestPublicRoute(t *testing.T) {
	site, err := Site("vw", []Route{{ID: "vw-web", Host: "vault.example.com", Upstream: "169.254.1.3:20001",
		PrivatePaths: []string{"/admin"}, LoginPaths: []string{"/identity/connect/token"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `
@vw-web host vault.example.com
handle @vw-web {
	@vw-web-private-paths {
		not remote_ip private_ranges 100.64.0.0/10
		path /admin /admin/*
	}
	route {
		abort @vw-web-private-paths
		crowdsec
		rate_limit {
			zone vw-web-login {
				match {
					path /identity/connect/token /identity/connect/token/*
				}
				key {remote_host}
				events 10
				window 1m
			}
			zone vw-web {
				key {remote_host}
				events 1000
				window 10s
			}
		}
		reverse_proxy 169.254.1.3:20001
	}
}
`
	if _, body, _ := strings.Cut(site, "\n"); body != want {
		t.Errorf("public route:\n%s\nwant:\n%s", body, want)
	}
	for _, r := range []Route{
		{ID: "a", Host: "x", Upstream: "a:1", PrivatePaths: []string{"/a b"}},
		{ID: "a", Host: "x", Upstream: "a:1", LoginPaths: []string{"login"}},
		{ID: "a", Host: "x", Upstream: "a:1", PrivatePaths: []string{"/a}"}},
	} {
		if _, err := Site("svc", []Route{r}); err == nil {
			t.Errorf("expected an error for %+v", r)
		}
	}
}

func TestMainNeedsDNS(t *testing.T) {
	if _, err := Main(MainConfig{Domain: "example.com"}); err == nil {
		t.Error("expected an error without a DNS directive")
	}
}

// TestCaddyAdapt checks generated config with a real Caddy binary built with
// the Cloudflare DNS plugin. It only runs when ROOSTACK_CADDY_BIN is set.
func TestCaddyAdapt(t *testing.T) {
	bin := os.Getenv("ROOSTACK_CADDY_BIN")
	if bin == "" {
		t.Skip("set ROOSTACK_CADDY_BIN to a caddy binary with the cloudflare DNS module")
	}
	dir := t.TempDir()
	main, err := Main(MainConfig{
		Domain: "lab.example.com", Email: "a@example.com", DNSDirective: "dns cloudflare {env.DNS_API_TOKEN}",
		AdminUpstream: "host.containers.internal:8480",
		CrowdSecAPI:   "http://169.254.1.3:8473", AccessLog: "/var/log/caddy/access.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	site, err := Site("svc", []Route{
		{ID: "svc-web", Host: "web.lab.example.com", Upstream: "roostack-svc-web:80", Private: true, MaxBodySize: "10GB"},
		{ID: "svc-api", Host: "api.lab.example.com", Upstream: "roostack-svc-api:8080",
			PrivatePaths: []string{"/admin", "/-/admin"}, LoginPaths: []string{"/login"}, MaxBodySize: "1GB"},
	})
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, "sites"), 0o755)
	os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte(main), 0o644)
	os.WriteFile(filepath.Join(dir, "sites", "svc.caddy"), []byte(site), 0o644)

	cmd := exec.Command(bin, "adapt", "--config", "Caddyfile")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DNS_API_TOKEN=test", CrowdSecAPIKeyEnv+"=test")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("caddy adapt failed: %v\n%s", err, out)
	}
	for _, want := range []string{`"web.lab.example.com"`, `"api.lab.example.com"`, `"*.lab.example.com"`, `"cloudflare"`,
		`"handler":"crowdsec"`, `"handler":"rate_limit"`, `"/-/admin/*"`, `"crowdsec":{`, `"filename":"/var/log/caddy/access.log"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("adapted config missing %s", want)
		}
	}
}
