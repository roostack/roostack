package caddy

import (
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/trust"
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
		not {
			remote_ip PRIVATE
			not vars roostack_edge yes
		}
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
	want = strings.Replace(want, "PRIVATE", PrivateRanges, 1)
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

// TestPublicRouteWAF: with the WAF on, AppSec checks outside visitors'
// requests after the bans and the rate limits, just before the service
// (SPEC.md §6.10 item 4); home devices, and private routes, skip it.
func TestPublicRouteWAF(t *testing.T) {
	site, err := Site("vw", []Route{{ID: "vw-web", Host: "vault.example.com", Upstream: "169.254.1.3:20001", WAF: true,
		PrivateExcept: []string{"100.96.0.2/32"}},
		{ID: "vw-home", Host: "home.example.com", Upstream: "169.254.1.3:20002", Private: true, WAF: true}})
	if err != nil {
		t.Fatal(err)
	}
	want := `	@vw-web-outside not {
		remote_ip PRIVATE
		not vars roostack_edge yes
		not remote_ip 100.96.0.2/32
	}
	route {
		crowdsec
		rate_limit {
			zone vw-web {
				key {remote_host}
				events 1000
				window 10s
			}
		}
		appsec @vw-web-outside
		reverse_proxy 169.254.1.3:20001
	}
`
	want = strings.Replace(want, "PRIVATE", PrivateRanges, 1)
	if !strings.Contains(site, want) {
		t.Errorf("public route with the WAF:\n%s\nwant:\n%s", site, want)
	}
	if strings.Count(site, "appsec") != 1 {
		t.Errorf("only the public route is checked:\n%s", site)
	}
	// Off: no AppSec at all.
	site, _ = Site("vw", []Route{{ID: "vw-web", Host: "vault.example.com", Upstream: "169.254.1.3:20001"}})
	if strings.Contains(site, "appsec") || strings.Contains(site, "-outside") {
		t.Errorf("the WAF turned off:\n%s", site)
	}
}

// TestMainAppSec: the bouncer's AppSec settings (fail open, a body cap),
// and the UI checked by AppSec when it's public.
func TestMainAppSec(t *testing.T) {
	c := MainConfig{Domain: "lab.example.com", DNSDirective: "dns cloudflare {env.DNS_API_TOKEN}",
		AdminUpstream: "169.254.1.3:8480", CrowdSecAPI: "http://169.254.1.3:8473", AppSecURL: "http://169.254.1.3:8475/"}
	main, err := Main(c)
	if err != nil {
		t.Fatal(err)
	}
	want := "\tcrowdsec {\n\t\tapi_url http://169.254.1.3:8473\n\t\tapi_key {env.CROWDSEC_API_KEY}\n\t\tticker_interval 15s\n" +
		"\t\tappsec_url http://169.254.1.3:8475/\n\t\tappsec_max_body_bytes 1048576\n\t\tappsec_fail_open\n\t}\n"
	if !strings.Contains(main, want) {
		t.Errorf("crowdsec block:\n%s", main)
	}
	if strings.Contains(main, "appsec @") {
		t.Error("a private UI is checked by AppSec")
	}
	c.AdminPublic = true
	if main, _ = Main(c); !strings.Contains(main, "appsec @roostack-admin-outside") {
		t.Errorf("a public UI isn't checked by AppSec:\n%s", main)
	}
	c.AppSecURL = "http://x/ {"
	if _, err := Main(c); err == nil {
		t.Error("an AppSec address that breaks the Caddyfile")
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
		CrowdSecAPI:   "http://169.254.1.3:8473", AppSecURL: "http://169.254.1.3:8475/", AccessLog: "/var/log/caddy/access.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	site, err := Site("svc", []Route{
		{ID: "svc-web", Host: "web.lab.example.com", Upstream: "roostack-svc-web:80", Private: true, MaxBodySize: "10GB"},
		{ID: "svc-api", Host: "api.lab.example.com", Upstream: "roostack-svc-api:8080",
			PrivatePaths: []string{"/admin", "/-/admin"}, LoginPaths: []string{"/login"}, MaxBodySize: "1GB",
			CheckUpstream: "169.254.1.3:8480", WAF: true},
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
		`"handler":"crowdsec"`, `"handler":"rate_limit"`, `"/-/admin/*"`, `"crowdsec":{`, `"filename":"/var/log/caddy/access.log"`, `"path_regexp"`,
		`"handler":"appsec"`, `"appsec_url":"http://169.254.1.3:8475/"`, `"appsec_fail_open":true`, `"appsec_max_body_bytes":1048576`} {
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
// it alone, on its own listener (EdgePort), whose sites count every
// visitor as outside whatever address the header claims; port 443
// believes no header, and counts the edge's own address as outside
// (review H4).
func TestEdgeFrom(t *testing.T) {
	main, err := Main(MainConfig{Domain: "smith.family", DNSDirective: "dns cloudflare {env.X}", AdminUpstream: "169.254.1.3:8480",
		EdgeFrom: []string{"100.96.0.2/32"}, OldDomain: "old.family", OldDNSDirective: "dns cloudflare {env.X}"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"\tservers :8474 {\n\t\tlistener_wrappers {\n\t\t\tproxy_protocol {\n\t\t\t\tallow 100.96.0.2/32\n\t\t\t\tfallback_policy reject\n\t\t\t}\n\t\t\ttls\n",
		"@roostack-admin-external not {\n\t\t\tremote_ip " + PrivateRanges + "\n\t\t\tnot vars roostack_edge yes\n\t\t\tnot remote_ip 100.96.0.2/32\n\t\t}",
		"\n*.smith.family {\n", "\n*.old.family {\n",
		"\n*.smith.family:8474 {\n\tvars roostack_edge yes\n", "\n*.old.family:8474 {\n\tvars roostack_edge yes\n",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("missing %q in\n%s", want, main)
		}
	}
	if strings.Contains(main, "servers :443") || strings.Count(main, "proxy_protocol") != 1 {
		t.Errorf("port 443 believes PROXY headers:\n%s", main)
	}
	// The edge sites have no Roostack pages, and import the services.
	_, edgeSites, _ := strings.Cut(main, "*.smith.family:8474 {")
	if strings.Contains(edgeSites, "roostack-admin") || strings.Count(edgeSites, "import sites/*.caddy") != 2 {
		t.Errorf("edge sites:\n%s", edgeSites)
	}
	site, err := Site("photos", []Route{{ID: "photos", Host: "photos.smith.family", Upstream: "169.254.1.3:20000",
		PrivatePaths: []string{"/admin"}, PrivateExcept: []string{"100.96.0.2/32"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "@photos-private-paths {\n\t\tnot {\n\t\t\tremote_ip " + PrivateRanges +
		"\n\t\t\tnot vars roostack_edge yes\n\t\t\tnot remote_ip 100.96.0.2/32\n\t\t}\n\t\tpath /admin /admin/*\n\t}"; !strings.Contains(site, want) {
		t.Errorf("private paths:\n%s", site)
	}
	if _, err := Main(MainConfig{Domain: "smith.family", DNSDirective: "dns x", EdgeFrom: []string{"nonsense"}}); err == nil {
		t.Error("accepted a bad edge address")
	}
	// Without an edge, there's no edge listener.
	plain, _ := Main(MainConfig{Domain: "smith.family", DNSDirective: "dns x", AdminUpstream: "169.254.1.3:8480"})
	if strings.Contains(plain, "proxy_protocol") || strings.Contains(plain, ":8474") ||
		!strings.Contains(plain, "@roostack-admin-external not {\n\t\t\tremote_ip "+PrivateRanges) {
		t.Errorf("without an edge:\n%s", plain)
	}
}

// TestPrivateRanges: Caddy's private list is the firewall's home network
// (internal/trust): an address is private to Caddy if and only if it's
// home, apart from loopback (Caddy's own, private_ranges), and IPv6
// link-local and fc00::/8, which only the firewall's set has (Caddy never
// lets in more than the firewall counts as home).
func TestPrivateRanges(t *testing.T) {
	want := "private_ranges 100.64.0.0/12 100.80.0.0/16 100.82.0.0/15 100.84.0.0/14 100.88.0.0/13 " +
		"100.96.128.0/17 100.97.0.0/16 100.98.0.0/15 100.104.0.0/13 100.112.0.0/12"
	if PrivateRanges != want {
		t.Errorf("PrivateRanges = %q, want %q", PrivateRanges, want)
	}
	// Caddy's private_ranges shorthand.
	var caddy []netip.Prefix
	for _, r := range strings.Fields(strings.Replace(PrivateRanges, "private_ranges",
		"192.168.0.0/16 172.16.0.0/12 10.0.0.0/8 127.0.0.1/8 fd00::/8 ::1/128", 1)) {
		caddy = append(caddy, netip.MustParsePrefix(r).Masked())
	}
	private := func(a netip.Addr) bool {
		for _, p := range caddy {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	var samples []netip.Addr
	for _, p := range append(slices.Clone(trust.HomeRanges), trust.CGNATRange, trust.GuestRange, trust.RelayRange,
		trust.OverlayRange, trust.ServersRange, trust.DevicesRange, netip.MustParsePrefix("100.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32")) {
		// Each range's first and last address, and the ones either side.
		first, last := p.Masked().Addr(), lastAddr(p)
		samples = append(samples, first, first.Prev(), last, last.Next())
	}
	for _, a := range samples {
		if !a.IsValid() || a.IsLoopback() || a.IsLinkLocalUnicast() || netip.MustParsePrefix("fc00::/8").Contains(a) {
			continue
		}
		if private(a) != trust.IsHome(a) {
			t.Errorf("%s: private to Caddy %v, home to the firewall %v", a, private(a), trust.IsHome(a))
		}
	}
}

// lastAddr is p's last address.
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}
