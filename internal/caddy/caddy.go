// Package caddy generates Caddy configuration (SPEC.md §6.5): a main
// Caddyfile with one wildcard site, and one snippet per service that routes
// its subdomains.
//
// Layout inside the Caddy container (see docs/ASSUMPTIONS.md D1):
//
//	/etc/caddy/Caddyfile        main config (Main)
//	/etc/caddy/sites/<svc>.caddy per-service handlers (Site)
package caddy

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// PrivateRanges are the client networks allowed to reach private routes
// in a lab at home: Caddy's private_ranges (RFC1918, loopback, ULA) plus
// the CGNAT range 100.64.0.0/10 used by Tailscale and Roostack's network,
// written out without the ranges of other labs storing backups here
// (100.100.0.0/14) and Roostack Cloud's relay (100.81.0.0/16), which are
// never private (SPEC.md §6.10). The same as agent.LANRanges (a test
// checks); see docs/ASSUMPTIONS.md C4/C5.
const PrivateRanges = "private_ranges 100.64.0.0/12 100.80.0.0/16 100.82.0.0/15 100.84.0.0/14 100.88.0.0/13 " +
	"100.96.0.0/14 100.104.0.0/13 100.112.0.0/12"

// EdgePort is where the lab's edges reach the controller's Caddy (SPEC.md
// §10.5): a listener of its own, which believes their PROXY headers and
// whose sites count every visitor as outside, so a header claiming a home
// address reaches nothing private (review H4). The firewall sends edges'
// connections to 443 here (agent.EdgePort, the same number).
const EdgePort = 8474

// EdgeVar is set on requests that came through an edge (the edge
// listener's sites), and every private check counts them as outside.
const EdgeVar = "roostack_edge"

// CertResolvers are the DNS resolvers Caddy checks a certificate's DNS
// challenge with before asking Let's Encrypt to look (the tls block's
// "resolvers"): public ones first, since the check is only of what the
// internet sees, then pasta's forwarder to the host's own resolver for a
// network that blocks outside DNS (Caddy tries them in turn). A home
// router's resolver had kept a domain's old name servers for hours after
// the domain moved to another DNS host, refreshing them from the old
// host, so the challenge's record was never seen (VM3, 2026-10-14;
// docs/ASSUMPTIONS.md D3).
const CertResolvers = "1.1.1.1 9.9.9.9 169.254.1.1"

// ThisServerOnly is who reaches private routes in a lab on the internet
// (SPEC.md §6.12): only connections made on the server itself, which is
// where an SSH tunnel ends. Caddy's pasta network shows a connection to
// the host's 127.0.0.1 as coming from hostIP, and one to the host's own
// address as coming from hostLoopback (docs/ASSUMPTIONS.md P9). A cloud
// provider's private network (shared with other customers) stays out.
func ThisServerOnly(hostIP, hostLoopback string) string {
	from := []string{"127.0.0.1/8", "::1"}
	for _, a := range []string{hostIP, hostLoopback} {
		if a != "" {
			from = append(from, a)
		}
	}
	return strings.Join(from, " ")
}

// Route is one subdomain handled by Caddy.
type Route struct {
	// ID must be unique across the lab; used to name Caddy matchers.
	ID string
	// Service and Title are the service's name and what people call it,
	// for its error pages (internal/errpage): set as Caddy variables.
	Service, Title string
	Host           string // full hostname, e.g. photos.lab.example.com
	// OldHost is its name at the lab's previous address, served too for a
	// while after switching (SPEC.md §6.6); "" when there's none.
	OldHost  string
	Upstream string // container:port on the roostack-proxy network
	Private  bool
	// PrivateFrom is who reaches it when private, and its private paths
	// when public: remote_ip ranges. Empty means PrivateRanges.
	PrivateFrom string
	// PrivateExcept are addresses inside PrivateFrom that still count as
	// outside: the lab's edges (SPEC.md §10.5), whose own WireGuard
	// address a connection without a PROXY header would show.
	PrivateExcept []string
	MaxBodySize   string // optional, e.g. "50GB"
	// On a public route (SPEC.md §6.11): PrivatePaths (and everything
	// under them) stay home-only, and LoginPaths get a strict rate limit.
	PrivatePaths []string
	LoginPaths   []string
	// CheckUpstream is the Roostack UI, which answers the outside check
	// link on a public route (internal/public). Empty leaves it out.
	CheckUpstream string
}

// CheckPattern is the only path on a public address that reaches
// Roostack: a check link with its token, nothing else (internal/public).
const CheckPattern = `^/\.well-known/roostack-check/[0-9a-f]{32}$`

// Rate limits per visitor address on public routes (SPEC.md §6.11): a
// generous one on everything, which only a flood reaches (a photo grid
// loads hundreds of thumbnails at once), and a strict one on login pages.
const (
	RateLimitEvents      = 1000
	RateLimitWindow      = "10s"
	LoginRateLimitEvents = 10
	LoginRateLimitWindow = "1m"
)

// CrowdSecAPIKeyEnv is the environment variable holding the bouncer's
// key to CrowdSec's local API, in the Caddy container.
const CrowdSecAPIKeyEnv = "CROWDSEC_API_KEY"

// Site renders the snippet for one service's routes. It is imported inside
// the wildcard site block, so it only contains handle blocks.
func Site(service string, routes []Route) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by Roostack for service %q. Do not edit: changes will be overwritten.\n", service)
	for _, r := range routes {
		if err := r.validate(); err != nil {
			return "", err
		}
		hosts := r.Host
		if r.OldHost != "" {
			hosts += " " + r.OldHost
		}
		fmt.Fprintf(&b, "\n@%s host %s\n", r.ID, hosts)
		fmt.Fprintf(&b, "handle @%s {\n", r.ID)
		// What the error pages say about it (internal/errpage).
		private := map[bool]string{true: "yes", false: "no"}[r.Private]
		fmt.Fprintf(&b, "\tvars {\n\t\troostack_name %s\n\t\troostack_title %s\n\t\troostack_private %s\n\t}\n",
			cmp.Or(r.Service, service), quoteTitle(r.Title), private)
		if r.Private {
			fmt.Fprintf(&b, "\t@%s-external %s\n", r.ID, r.notPrivate("\t"))
			fmt.Fprintf(&b, "\tabort @%s-external\n", r.ID)
			if r.MaxBodySize != "" {
				fmt.Fprintf(&b, "\trequest_body {\n\t\tmax_size %s\n\t}\n", r.MaxBodySize)
			}
			fmt.Fprintf(&b, "\treverse_proxy %s\n", r.Upstream)
		} else {
			publicRoute(&b, r)
		}
		b.WriteString("}\n")
	}
	return b.String(), nil
}

// quoteTitle makes a service's title safe as one Caddyfile token: quoted,
// without the characters Caddy (or a template) would treat specially.
func quoteTitle(t string) string {
	t = strings.Map(func(r rune) rune {
		if r < ' ' || strings.ContainsRune("\"{}\\`", r) {
			return -1
		}
		return r
	}, t)
	if t == "" {
		t = "This service"
	}
	return `"` + t + `"`
}

// publicRoute writes a public route's handler (SPEC.md §6.11). Everything
// is inside one route block, which runs in the order written: Caddy would
// otherwise run a route block before an abort next to it, letting outside
// visitors into private paths (docs/ASSUMPTIONS.md P2). Then CrowdSec's
// bouncer turns away banned addresses, and rate limits apply before the
// service sees the request.
func publicRoute(b *strings.Builder, r Route) {
	if len(r.PrivatePaths) > 0 {
		fmt.Fprintf(b, "\t@%s-private-paths {\n\t\t%s\n\t\tpath %s\n\t}\n",
			r.ID, r.notPrivate("\t\t"), pathPatterns(r.PrivatePaths))
	}
	b.WriteString("\troute {\n")
	if len(r.PrivatePaths) > 0 {
		fmt.Fprintf(b, "\t\tabort @%s-private-paths\n", r.ID)
	}
	b.WriteString("\t\tcrowdsec\n")
	b.WriteString("\t\trate_limit {\n")
	if len(r.LoginPaths) > 0 {
		fmt.Fprintf(b, "\t\t\tzone %s-login {\n\t\t\t\tmatch {\n\t\t\t\t\tpath %s\n\t\t\t\t}\n", r.ID, pathPatterns(r.LoginPaths))
		fmt.Fprintf(b, "\t\t\t\tkey {remote_host}\n\t\t\t\tevents %d\n\t\t\t\twindow %s\n\t\t\t}\n", LoginRateLimitEvents, LoginRateLimitWindow)
	}
	fmt.Fprintf(b, "\t\t\tzone %s {\n\t\t\t\tkey {remote_host}\n\t\t\t\tevents %d\n\t\t\t\twindow %s\n\t\t\t}\n", r.ID, RateLimitEvents, RateLimitWindow)
	b.WriteString("\t\t}\n")
	if r.CheckUpstream != "" {
		fmt.Fprintf(b, "\t\t@%s-check path_regexp %s\n", r.ID, CheckPattern)
		fmt.Fprintf(b, "\t\treverse_proxy @%s-check %s\n", r.ID, r.CheckUpstream)
	}
	if r.MaxBodySize != "" {
		fmt.Fprintf(b, "\t\trequest_body {\n\t\t\tmax_size %s\n\t\t}\n", r.MaxBodySize)
	}
	fmt.Fprintf(b, "\t\treverse_proxy %s\n", r.Upstream)
	b.WriteString("\t}\n")
}

func (r Route) privateFrom() string {
	if r.PrivateFrom == "" {
		return PrivateRanges
	}
	return r.PrivateFrom
}

// notPrivate is the matcher for a connection from outside, at indent:
// "not (private, not through an edge, and not from an edge)": not from
// PrivateFrom, through the edge listener (EdgeVar), whatever address its
// PROXY header claims, or from one of PrivateExcept (an edge's own
// address, which a connection without a header shows). Checked with
// Caddy, docs/ASSUMPTIONS.md Q11, Q12.
func (r Route) notPrivate(indent string) string {
	lines := []string{"remote_ip " + r.privateFrom(), "not vars " + EdgeVar + " yes"}
	if len(r.PrivateExcept) > 0 {
		lines = append(lines, "not remote_ip "+strings.Join(r.PrivateExcept, " "))
	}
	return "not {\n" + indent + "\t" + strings.Join(lines, "\n"+indent+"\t") + "\n" + indent + "}"
}

// pathPatterns matches each path and everything under it.
func pathPatterns(paths []string) string {
	var ps []string
	for _, p := range paths {
		ps = append(ps, p, p+"/*")
	}
	return strings.Join(ps, " ")
}

func (r Route) validate() error {
	if strings.ContainsAny(r.PrivateFrom, "\t\n{}\"#") {
		return fmt.Errorf("caddy route %s: invalid private addresses %q", r.ID, r.PrivateFrom)
	}
	if err := checkPrefixes(r.PrivateExcept); err != nil {
		return fmt.Errorf("caddy route %s: %w", r.ID, err)
	}
	for _, f := range []struct{ name, val string }{
		{"id", r.ID}, {"host", r.Host}, {"old host", r.OldHost}, {"upstream", r.Upstream}, {"max body size", r.MaxBodySize},
		{"check upstream", r.CheckUpstream},
	} {
		// Guard against anything that could break out of the Caddyfile syntax.
		if strings.ContainsAny(f.val, " \t\n{}\"#") {
			return fmt.Errorf("caddy route %s: invalid characters in %q", f.name, f.val)
		}
	}
	for _, p := range append(slices.Clone(r.PrivatePaths), r.LoginPaths...) {
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " \t\n{}\"#*") {
			return fmt.Errorf("caddy route %s: invalid path %q", r.ID, p)
		}
	}
	if r.ID == "" || r.Host == "" || r.Upstream == "" {
		return errors.New("caddy route: id, host and upstream are required")
	}
	return nil
}

// MainConfig is the input for the main Caddyfile.
type MainConfig struct {
	Domain string
	Email  string
	// DNSDirective (with CertResolvers) is the tls "dns ..." directive for the DNS-01 challenge
	// (from internal/dns); it may span several lines.
	DNSDirective string
	// AdminUpstream is where the Roostack web UI listens, as seen from the
	// Caddy container. Empty disables the admin route.
	AdminUpstream string
	// AdminHost is where it's served: roost.<domain> when empty, or the
	// domain itself (a roost.ac name, SPEC.md §6.6). OldAdminHost is the
	// same at the old address.
	AdminHost, OldAdminHost string
	// AdminPublic serves the UI to anyone, like a public service (CrowdSec,
	// rate limits, a strict one on logging in): on a lab on the internet
	// (SPEC.md §6.3.2). Otherwise it's private.
	AdminPublic bool
	// PrivateFrom is who reaches private routes (Route.PrivateFrom).
	PrivateFrom string
	// CrowdSecAPI is the address of CrowdSec's local API, as seen from the
	// Caddy container, set while anything is public (SPEC.md §6.11). The
	// bouncer's key is in the CrowdSecAPIKeyEnv environment variable.
	CrowdSecAPI string
	// AccessLog is the file Caddy logs requests to, for CrowdSec. Empty
	// logs none.
	AccessLog string
	// OldDomain is the lab's previous address, served as well while it
	// still works (SPEC.md §6.6), with its own certificate through
	// OldDNSDirective.
	OldDomain       string
	OldDNSDirective string
	// EdgeFrom are the lab's edges' WireGuard addresses (SPEC.md §10.5):
	// the only sources whose PROXY header (the visitor's address) Caddy
	// believes, on EdgePort only, and outside for the private check.
	EdgeFrom []string
}

// ErrorPagesDir is where the error pages are inside Caddy's container
// (config.Paths.CaddyDir()/errors, written by internal/generate).
const ErrorPagesDir = "/etc/caddy/errors"

// AdminSubdomain is where the Roostack UI is served: roost.<domain>
// (renamed from roostack. on 2026-10-01: shorter to type and say).
const AdminSubdomain = "roost"

// LinkSubdomain names this lab for other servers and labs connecting to it
// over WireGuard (joining, storage for others): link.<domain>, pointing at
// the home's public address once someone chooses it (SPEC.md §10.2). Not
// a web address: no service may use it.
const LinkSubdomain = "link"

// AdminLoginPaths get the strict rate limit when the UI is public: logging
// in (password, then code) and the recovery key's password-and-code check.
// Not /setup: the setup code is too long to guess, and the wizard's pages
// are under it.
var AdminLoginPaths = []string{"/login", "/backups/recovery-key"}

// Main renders the main Caddyfile.
func Main(c MainConfig) (string, error) {
	if strings.TrimSpace(c.DNSDirective) == "" {
		return "", errors.New("caddy: no DNS provider configured")
	}
	if c.Domain == "" || strings.ContainsAny(c.Domain+c.Email+c.CrowdSecAPI+c.AccessLog, " \t\n{}\"#") {
		return "", fmt.Errorf("caddy: invalid domain, email, CrowdSec address or log file")
	}

	var b strings.Builder
	b.WriteString("# Generated by Roostack. Do not edit: changes will be overwritten.\n")
	b.WriteString("{\n")
	if c.Email != "" {
		fmt.Fprintf(&b, "\temail %s\n", c.Email)
	}
	if len(c.EdgeFrom) > 0 {
		if err := checkPrefixes(c.EdgeFrom); err != nil {
			return "", fmt.Errorf("caddy: edges: %w", err)
		}
		// Visitors through an edge arrive on EdgePort from its WireGuard
		// address with a PROXY header saying who they are. Anyone else
		// sending one is refused. Port 443 believes no header at all
		// (checked with Caddy, docs/ASSUMPTIONS.md Q3, Q11, Q12).
		fmt.Fprintf(&b, "\tservers :%d {\n\t\tlistener_wrappers {\n\t\t\tproxy_protocol {\n\t\t\t\tallow %s\n"+
			"\t\t\t\tfallback_policy reject\n\t\t\t}\n\t\t\ttls\n\t\t}\n\t}\n", EdgePort, strings.Join(c.EdgeFrom, " "))
	}
	if c.CrowdSecAPI != "" {
		// Caddy keeps serving while CrowdSec's API is unreachable, and
		// retries (docs/ASSUMPTIONS.md P5).
		fmt.Fprintf(&b, "\tcrowdsec {\n\t\tapi_url %s\n\t\tapi_key {env.%s}\n\t\tticker_interval 15s\n\t}\n",
			c.CrowdSecAPI, CrowdSecAPIKeyEnv)
	}
	b.WriteString("}\n")
	if c.OldDomain != "" && (strings.ContainsAny(c.OldDomain, " \t\n{}\"#") || strings.TrimSpace(c.OldDNSDirective) == "") {
		return "", fmt.Errorf("caddy: invalid old address %q", c.OldDomain)
	}
	// The sites again on the edge listener, if there are edges.
	for _, edge := range []bool{false, true} {
		if edge && len(c.EdgeFrom) == 0 {
			break
		}
		if err := site(&b, c, c.Domain, c.AdminHost, c.DNSDirective, "", edge); err != nil {
			return "", err
		}
		// The previous address (SPEC.md §6.6): the same services, through
		// the same snippets (their matchers name both hosts).
		if c.OldDomain == "" {
			continue
		}
		if err := site(&b, c, c.OldDomain, c.OldAdminHost, c.OldDNSDirective, c.Domain, edge); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

// site writes one wildcard site block for domain, with Roostack's pages
// at adminHost (roost.<domain> if empty; the domain itself is added to the
// block, since the wildcard doesn't cover it). now is the lab's current
// address when domain is its previous one. An edge site is the same on
// EdgePort, for visitors through an edge: it sets EdgeVar, so every
// private check counts them as outside, and has no Roostack pages (the
// UI is never reached through an edge).
func site(b *strings.Builder, c MainConfig, domain, adminHost, directive, now string, edge bool) error {
	if adminHost == "" {
		adminHost = AdminSubdomain + "." + domain
	}
	if strings.ContainsAny(adminHost, " \t\n{}\"#,") {
		return fmt.Errorf("caddy: invalid admin host %q", adminHost)
	}
	b.WriteString("\n")
	if now != "" {
		fmt.Fprintf(b, "# The lab's previous address, until it stops working; it's now %s.\n", now)
	}
	port := ""
	if edge {
		port = fmt.Sprintf(":%d", EdgePort)
		fmt.Fprintf(b, "# Visitors through the lab's edges: never private (SPEC.md §10.5).\n")
	}
	if adminHost == domain {
		fmt.Fprintf(b, "*.%s%s, %s%s {\n", domain, port, domain, port)
	} else {
		fmt.Fprintf(b, "*.%s%s {\n", domain, port)
	}
	if edge {
		fmt.Fprintf(b, "\tvars %s yes\n", EdgeVar)
	}
	b.WriteString("\ttls {\n")
	for _, line := range strings.Split(strings.TrimSpace(directive), "\n") {
		fmt.Fprintf(b, "\t\t%s\n", line)
	}
	fmt.Fprintf(b, "\t\tresolvers %s\n", CertResolvers)
	b.WriteString("\t}\n")
	if c.AccessLog != "" {
		fmt.Fprintf(b, "\tlog {\n\t\toutput file %s {\n\t\t\troll_size 20MiB\n\t\t\troll_keep 5\n\t\t}\n\t\tformat json\n\t}\n", c.AccessLog)
	}
	if c.AdminUpstream != "" && !edge {
		route := Route{ID: "roostack-admin", Title: "Roostack", Host: adminHost,
			Upstream: c.AdminUpstream, Private: !c.AdminPublic, PrivateFrom: c.PrivateFrom, PrivateExcept: c.EdgeFrom}
		if c.AdminPublic {
			route.LoginPaths = AdminLoginPaths
		}
		admin, err := Site("roostack", []Route{route})
		if err != nil {
			return err
		}
		// Indent the admin handler into the site block, skipping its header.
		_, body, _ := strings.Cut(admin, "\n")
		for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
			fmt.Fprintf(b, "\t%s\n", line)
		}
	}
	b.WriteString("\n\timport sites/*.caddy\n\n")
	// An unknown subdomain gets the "nobody roosts here" page, which names
	// no lab (anyone can see the wildcard certificate's domain anyway).
	b.WriteString("\thandle {\n\t\terror 404\n\t}\n\n")
	// Errors (a service not answering, a blocked visitor, the 404 above)
	// get Roostack's pages (internal/errpage), with the error's status.
	fmt.Fprintf(b, "\thandle_errors {\n\t\troot * %s\n\t\ttry_files /{err.status_code}.html /error.html\n\t\ttemplates\n"+
		"\t\tfile_server {\n\t\t\tstatus {err.status_code}\n\t\t}\n\t}\n", ErrorPagesDir)
	b.WriteString("}\n")
	return nil
}

// checkPrefixes checks addresses or ranges (100.96.0.2/32) for Caddy.
func checkPrefixes(list []string) error {
	for _, p := range list {
		if _, err := netip.ParsePrefix(p); err != nil {
			if _, err := netip.ParseAddr(p); err != nil {
				return fmt.Errorf("invalid address %q", p)
			}
		}
	}
	return nil
}

// EdgeConfig is a lab's edge (SPEC.md §10.5): a cloud server passing
// visitors' connections for the lab's public names to the server whose
// Caddy serves them, without decrypting them.
type EdgeConfig struct {
	// Hosts are the public names it passes on.
	Hosts []string
	// Upstream is where (host:port on Roostack's network): the
	// controller's Caddy.
	Upstream string
}

// Edge renders an edge's Caddyfile: Caddy's layer-4 plugin passes TLS
// connections for Hosts through to Upstream by the name in the handshake,
// with a PROXY header carrying the visitor's address; other names get
// nothing. Plain HTTP for Hosts is redirected to HTTPS. No certificates:
// they stay with the server that serves the names.
func Edge(c EdgeConfig) (string, error) {
	if _, err := netip.ParseAddrPort(c.Upstream); err != nil {
		return "", fmt.Errorf("caddy: edge upstream %q: %w", c.Upstream, err)
	}
	for _, h := range c.Hosts {
		if h == "" || strings.ContainsAny(h, " \t\n{}\"#,*/:") {
			return "", fmt.Errorf("caddy: invalid public name %q", h)
		}
	}
	var b strings.Builder
	b.WriteString("# Generated by Roostack: this server is the lab's edge. Do not edit: changes will be overwritten.\n")
	b.WriteString("{\n\tauto_https off\n")
	if len(c.Hosts) > 0 {
		fmt.Fprintf(&b, "\tlayer4 {\n\t\t:443 {\n\t\t\t@public tls sni %s\n", strings.Join(c.Hosts, " "))
		fmt.Fprintf(&b, "\t\t\troute @public {\n\t\t\t\tproxy {\n\t\t\t\t\tproxy_protocol v2\n\t\t\t\t\tupstream %s\n"+
			"\t\t\t\t}\n\t\t\t}\n\t\t}\n\t}\n", c.Upstream)
	}
	b.WriteString("}\n")
	if len(c.Hosts) > 0 {
		var hs []string
		for _, h := range c.Hosts {
			hs = append(hs, "http://"+h)
		}
		fmt.Fprintf(&b, "\n%s {\n\tredir https://{host}{uri} 308\n}\n", strings.Join(hs, ", "))
	}
	b.WriteString("\n# Any other name: nothing.\nhttp:// {\n\tabort\n}\n")
	return b.String(), nil
}
