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
	// A title Caddy or a template would misread loses those characters.
	site, err := Site("vw", []Route{{ID: "vw-web", Title: `Vault "{warden}"`, Host: "vault.example.com", Upstream: "169.254.1.3:20001",
		PrivatePaths: []string{"/admin"}, LoginPaths: []string{"/identity/connect/token"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `
@vw-web host vault.example.com
handle @vw-web {
	vars {
		roostack_name vw
		roostack_title "Vault warden"
		roostack_private no
	}
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
			PrivatePaths: []string{"/admin", "/-/admin"}, LoginPaths: []string{"/login"}, MaxBodySize: "1GB",
			CheckUpstream: "169.254.1.3:8480"},
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
		`"handler":"crowdsec"`, `"handler":"rate_limit"`, `"/-/admin/*"`, `"crowdsec":{`, `"filename":"/var/log/caddy/access.log"`, `"path_regexp"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("adapted config missing %s", want)
		}
	}
}

// TestEdge: the edge passes its public names through to the controller
// with a PROXY header, redirects plain HTTP for them, and nothing else
// (the config itself checked with Caddy: docs/ASSUMPTIONS.md Q11).
func TestEdge(t *testing.T) {
	out, err := Edge(EdgeConfig{Hosts: []string{"photos.smith.family", "vault.smith.family"}, Upstream: "100.96.0.1:443"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\tauto_https off\n",
		"\t\t\t@public tls sni photos.smith.family vault.smith.family\n",
		"\t\t\t\t\tproxy_protocol v2\n\t\t\t\t\tupstream 100.96.0.1:443\n",
		"http://photos.smith.family, http://vault.smith.family {\n\tredir https://{host}{uri} 308\n}\n",
		"http:// {\n\tabort\n}\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// Nothing public yet: nothing passed on.
	if out, _ := Edge(EdgeConfig{Upstream: "100.96.0.1:443"}); strings.Contains(out, "layer4") || strings.Contains(out, "redir") {
		t.Errorf("no names:\n%s", out)
	}
	for _, bad := range []EdgeConfig{{Hosts: []string{"a b"}, Upstream: "100.96.0.1:443"}, {Hosts: []string{"*.x"}, Upstream: "100.96.0.1:443"},
		{Hosts: []string{"a.x"}, Upstream: "nowhere"}} {
		if _, err := Edge(bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}

// TestEdgeFrom: with an edge, the controller believes PROXY headers from
// it alone, and its address counts as outside for the private check.
func TestEdgeFrom(t *testing.T) {
	main, err := Main(MainConfig{Domain: "smith.family", DNSDirective: "dns cloudflare {env.X}", AdminUpstream: "169.254.1.3:8480",
		EdgeFrom: []string{"100.96.0.2/32"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\tservers :443 {\n\t\tlistener_wrappers {\n\t\t\tproxy_protocol {\n\t\t\t\tallow 100.96.0.2/32\n\t\t\t\tfallback_policy reject\n\t\t\t}\n\t\t\ttls\n",
		"@roostack-admin-external not {\n\t\t\tremote_ip private_ranges 100.64.0.0/10\n\t\t\tnot remote_ip 100.96.0.2/32\n\t\t}",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("missing %q in\n%s", want, main)
		}
	}
	site, err := Site("photos", []Route{{ID: "photos", Host: "photos.smith.family", Upstream: "169.254.1.3:20000",
		PrivatePaths: []string{"/admin"}, PrivateExcept: []string{"100.96.0.2/32"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "@photos-private-paths {\n\t\tnot {\n\t\t\tremote_ip private_ranges 100.64.0.0/10\n\t\t\tnot remote_ip 100.96.0.2/32\n\t\t}\n\t\tpath /admin /admin/*\n\t}"; !strings.Contains(site, want) {
		t.Errorf("private paths:\n%s", site)
	}
	if _, err := Main(MainConfig{Domain: "smith.family", DNSDirective: "dns x", EdgeFrom: []string{"nonsense"}}); err == nil {
		t.Error("accepted a bad edge address")
	}
	// Without an edge, nothing changes.
	plain, _ := Main(MainConfig{Domain: "smith.family", DNSDirective: "dns x", AdminUpstream: "169.254.1.3:8480"})
	if strings.Contains(plain, "proxy_protocol") || !strings.Contains(plain, "@roostack-admin-external not remote_ip private_ranges") {
		t.Errorf("without an edge:\n%s", plain)
	}
}
