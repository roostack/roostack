package generate

import (
	"fmt"
	"maps"
	"net"
	"path/filepath"
	"slices"
	"time"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/errpage"
	"roostack.cloud/roostack/internal/quadlet"
	"roostack.cloud/roostack/internal/trust"
)

// CaddyBuildDir holds the Containerfile CaddyImage is built from
// (build/caddy in the source, installed by the package), so that anyone
// can read and rebuild what's in it. Nothing builds it on a server.
const CaddyBuildDir = "/usr/share/roostack/caddy"

// MySQLInitDir holds the script MySQL and MariaDB containers run as
// root when they first start (build/mysql-initdb in the source, installed
// by the package): it lets the app's user make and drop the schemas named
// after its database (SPEC.md §8.1, "MySQL and MariaDB").
const MySQLInitDir = "/usr/share/roostack/mysql-initdb"

// CaddyUnit is the systemd service running the reverse proxy.
const CaddyUnit = "roostack-caddy"

// CaddyHostLoopback is the address inside Caddy's network namespace that
// pasta maps to the host's loopback (--map-host-loopback). Caddy reaches
// services (published on 127.0.0.1:<port>) and the Roostack UI through it.
const CaddyHostLoopback = "169.254.1.3"

// SystemOutput is the lab-wide output: shared network, proxy and main
// Caddyfile.
type SystemOutput struct {
	Quadlets  []File
	Caddyfile File // goes in config.Paths.CaddyDir()
	// Files are other files, Name being the full path.
	Files []File
	// Slices go in config.Paths.UserUnitDir(): Caddy's, and the one for
	// all the services (slices.go).
	Slices  []File
	Secrets []Secret
	Dirs    []string
	Units   []string
}

// CrowdSec (SPEC.md §6.10, §6.11): its image, pinned like every other
// (SPEC.md §8.1), and names.
const (
	CrowdSecImage = "docker.io/crowdsecurity/crowdsec:v1.8.1@sha256:0f2523fa61ef507f15d953045cface490cc880670c62f2755ced17524107f71a"
	// CrowdSecHub is the commit of CrowdSec's Hub (github.com/crowdsecurity/hub,
	// master on 2026-10-19) that its parsers, scenarios and AppSec rules
	// come from, pinned like the image: cscli downloads the Hub's index
	// from that commit and checks every item against the sha256 the index
	// gives it (SPEC.md §6.10 item 4). To update: the Hub's latest commit
	// on master, then check on a VM that CrowdSec starts and blocks
	// (docs/ASSUMPTIONS.md P10).
	CrowdSecHub = "3f61143127559cb501b273219909837a67f361a6"
	// CrowdSecCollections are what CrowdSec installs from the Hub: Caddy's
	// log parsers and HTTP attack scenarios, and AppSec's virtual patches
	// (rules for exploits of known vulnerabilities in popular apps) and
	// generic rules (a few narrow ones, e.g. PHP files run from WordPress's
	// uploads; its experimental rules only log).
	CrowdSecCollections = "crowdsecurity/caddy crowdsecurity/appsec-virtual-patching crowdsecurity/appsec-generic-rules"
	CrowdSecUnit        = "roostack-crowdsec"
	// CrowdSecBouncerKey is the Podman secret with the key Caddy's bouncer
	// uses on CrowdSec's local API; Roostack makes it.
	CrowdSecBouncerKey = "roostack-crowdsec-bouncer-key"
	// appSecPort is where AppSec listens inside CrowdSec's container.
	appSecPort = 7422
	// caddyLogDir holds Caddy's access log inside its container: the
	// host's config.Paths.CaddyLogDir().
	caddyLogDir = "/var/log/caddy"
)

// System generates the lab-wide units. public says whether any service is
// public (Output.Public): then CrowdSec runs, Caddy logs requests for it,
// and its bouncer checks each public request (SPEC.md §6.11).
func System(lab config.Lab, public bool) (*SystemOutput, error) {
	return SystemWith(lab, SystemOptions{Public: public})
}

// SystemOptions are what the services ask of the lab-wide units.
type SystemOptions struct {
	// Public: any service is public (System's public).
	Public bool
	// SignIn: a service has single sign-on on (Output.SSO), so Caddy
	// serves the sign-in page; SignInPublic: one of them is public, and so
	// is the sign-in page (SPEC.md §5.2).
	SignIn, SignInPublic bool
	// FirstHosts are each service's first address, by name, where the
	// domain itself may go (DomainRedirect).
	FirstHosts map[string]string
	// DirectHosts, on a server other than the controller, are the names
	// its own Caddy serves (Output.DirectHosts): while there are any, it
	// runs Caddy and CrowdSec for them (SPEC.md §10.5). HomeHosts are those
	// of its private routes, whose certificates come from DNS challenges
	// through the controller.
	DirectHosts, HomeHosts []string
}

// SystemWith is System with everything the services ask for.
func SystemWith(lab config.Lab, opts SystemOptions) (*SystemOutput, error) {
	public := opts.Public || opts.SignInPublic || lab.DomainPublic()
	out := &SystemOutput{Slices: systemSlices(lab.MemoryTotal)}
	if lab.GlancesHere {
		// A live view of this server (SPEC.md §6.15.1), on any server.
		if err := glances(lab, out); err != nil {
			return nil, err
		}
	}
	// Other servers have no Caddy, as the controller's serves their
	// routes, unless they serve their public ones directly.
	direct := lab.IsNode()
	if direct && len(opts.DirectHosts) == 0 {
		return out, nil
	}
	var publicHosts []string
	for _, h := range opts.DirectHosts {
		if !slices.Contains(opts.HomeHosts, h) {
			publicHosts = append(publicHosts, h)
		}
	}
	if !lab.HasAddress() {
		// No address yet: Caddy has nothing to serve, so it doesn't run
		// (SPEC.md §6.6). The UI is reached over an SSH tunnel meanwhile.
		return out, nil
	}

	var u quadlet.Unit
	u.Section("Unit").Add("Description", "Roostack: reverse proxy (Caddy)")
	cs := u.Section("Container")
	cs.Add("ContainerName", CaddyUnit)
	cs.Add("Image", CaddyUnit+".image")
	addTimezone(cs, lab)
	// Caddy has its own pasta network namespace, not a bridge network:
	// pasta's port forwarding keeps each visitor's real IP, which the
	// "private" route check depends on. Rootless bridge networks replace it
	// (docs/ASSUMPTIONS.md C4). --map-host-loopback lets Caddy reach
	// services published on the host's 127.0.0.1.
	cs.Add("Network", "pasta:--map-host-loopback,"+CaddyHostLoopback)
	// Needs net.ipv4.ip_unprivileged_port_start=80, set by roostack-agent
	// (docs/ASSUMPTIONS.md C3).
	cs.Add("PublishPort", "80:80")
	cs.Add("PublishPort", "443:443")
	cs.Add("PublishPort", "443:443/udp") // HTTP/3
	if len(lab.EdgeAddrs) > 0 && !direct {
		// The edges' own listener (caddy.EdgePort); the firewall lets only
		// the edges reach it.
		cs.Add("PublishPort", fmt.Sprintf("%d:%d", caddy.EdgePort, caddy.EdgePort))
	}
	cs.Add("Volume", lab.Paths.CaddyDir()+":/etc/caddy:ro")
	cs.Add("Volume", lab.Paths.CaddyDataDir()+":/data")
	// DNS provider credentials, one Podman secret per field (internal/dns).
	// A server serving directly has none: Let's Encrypt checks each name
	// it serves through the name itself (SPEC.md §10.5).
	var directive string
	var fields []string
	var err error
	switch {
	case !direct:
		if directive, fields, err = dnsConfig(lab); err != nil {
			return nil, err
		}
	case lab.TestAddress():
		directive = "issuer internal"
	}
	for _, f := range fields {
		cs.Add("Secret", fmt.Sprintf("%s,type=env,target=%s", dns.SecretName(f), dns.EnvName(f)))
		// Entered by the user in the setup wizard.
		out.Secrets = append(out.Secrets, Secret{Name: dns.SecretName(f), Holds: CaddyUnit + ".container"})
	}
	// The previous address's provider, while it still works (SPEC.md
	// §6.6): its certificate may need renewing meanwhile.
	old := lab.Old(time.Now())
	if direct {
		old = nil
	}
	var oldDirective string
	if old != nil {
		var oldFields []string
		if oldDirective, oldFields, err = dnsConfig(config.Lab{DNS: old.DNS}); err != nil {
			return nil, fmt.Errorf("the lab's old address: %w", err)
		}
		oldDirective = dns.OldDirective(oldDirective)
		for _, f := range oldFields {
			cs.Add("Secret", fmt.Sprintf("%s,type=env,target=%s", dns.OldSecretName(f), dns.OldEnvName(f)))
			out.Secrets = append(out.Secrets, Secret{Name: dns.OldSecretName(f), Holds: CaddyUnit + ".container"})
		}
	}
	if public {
		cs.Add("Volume", lab.Paths.CaddyLogDir()+":"+caddyLogDir)
		cs.Add("Secret", fmt.Sprintf("%s,type=env,target=%s", CrowdSecBouncerKey, caddy.CrowdSecAPIKeyEnv))
	}
	cs.Add("Label", "io.roostack.service=_system")
	cs.Add("Label", "io.roostack.component=caddy")
	svc := u.Section("Service")
	svc.Add("Slice", CoreSlice)
	// The user manager gives its units 200; 100 is as low as it lets one
	// go, so the kernel picks a service's container before Caddy.
	svc.Add("OOMScoreAdjust", "100")
	svc.Add("ExecReload", "/usr/bin/podman exec "+CaddyUnit+" caddy reload --config /etc/caddy/Caddyfile")
	svc.Add("Restart", "always")
	svc.Add("TimeoutStartSec", "900")
	u.Section("Install").Add("WantedBy", "default.target")
	if err := u.Validate(); err != nil {
		return nil, err
	}
	out.Quadlets = append(out.Quadlets, File{Name: CaddyUnit + ".container", Content: u.String()})

	image, err := caddyImage(lab)
	if err != nil {
		return nil, err
	}
	out.Quadlets = append(out.Quadlets, image)

	mc := caddy.MainConfig{
		Hosts:         publicHosts,
		HomeHosts:     opts.HomeHosts,
		Domain:        lab.Domain,
		Email:         lab.Email,
		DNSDirective:  directive,
		LocalCA:       lab.TestAddress(),
		AdminUpstream: fmt.Sprintf("%s:%d", CaddyHostLoopback, lab.UIPort),
		AdminPublic:   lab.PublicUI(),
		StatusPage:    lab.Monitoring.StatusPage,
		PrivateFrom:   PrivateFrom(lab),
		EdgeFrom:      lab.EdgeAddrs,
		// The lab's VPN devices: Roostack's pages for those that have them,
		// Your account and the domain itself for all (SPEC.md §10.11).
		PagesDevices: lab.Devices.Pages,
		AllDevices:   lab.Devices.All,
	}
	if direct {
		// Its services alone (caddy.MainConfig.Hosts).
		mc.AdminUpstream, mc.AdminPublic, mc.StatusPage, mc.EdgeFrom = "", false, false, nil
		mc.PrivateFrom = caddy.ThisServerOnly(lab.HostIP, CaddyHostLoopback)
		mc.PagesDevices, mc.AllDevices = nil, nil
		mc.HomeDNS = fmt.Sprintf("http://%s:%d", CaddyHostLoopback, config.ChallengePort)
	}
	for _, g := range lab.GlancesSites {
		if direct {
			break
		}
		mc.Glances = append(mc.Glances, caddy.GlancesSite{Host: g.Host, Upstream: g.Upstream,
			Wall: caddy.Wall{Upstream: fmt.Sprintf("%s:%d", CaddyHostLoopback, config.SignInPort), Service: g.Client}})
	}
	if lab.ServesDomain() && !direct {
		// The domain itself (SPEC.md §5.3).
		mc.DomainItself = &caddy.DomainSite{Redirect: DomainRedirect(lab, opts.FirstHosts), Page: lab.Shows() == config.ShowPage, Public: lab.DomainPublic()}
	}
	if opts.SignIn && !direct {
		mc.SignIn = &caddy.SignInSite{Host: lab.SignInHost(), Upstream: fmt.Sprintf("%s:%d", CaddyHostLoopback, config.SignInPort),
			Public: opts.SignInPublic, Containers: []string{ContainerHostGateway}}
		if lab.SignInServers {
			// Apps on the lab's other servers, over Roostack's network.
			mc.SignIn.Containers = append(mc.SignIn.Containers, trust.ServersRange.String())
		}
	}
	if old != nil {
		mc.OldDomain, mc.OldDNSDirective = old.Domain, oldDirective
	}
	out.Dirs = []string{lab.Paths.CaddyDataDir(), lab.Paths.CaddySitesDir(), filepath.Join(lab.Paths.CaddyDir(), "errors"),
		lab.Paths.StatusPageDir(), lab.Paths.DomainPageDir()}
	// The services' error pages (SPEC.md §6.3), which Caddy's handle_errors
	// serves from caddy.ErrorPagesDir.
	pages := errpage.CaddyPages(lab.DisplayName(), "https://"+lab.UIHost())
	for _, name := range slices.Sorted(maps.Keys(pages)) {
		out.Files = append(out.Files, File{Name: filepath.Join(lab.Paths.CaddyDir(), "errors", name), Content: pages[name]})
	}
	out.Units = append(out.Units, CaddyUnit+".service")
	if public {
		mc.CrowdSecAPI = fmt.Sprintf("http://%s:%d", CaddyHostLoopback, config.CrowdSecPort)
		mc.AppSecURL = fmt.Sprintf("http://%s:%d/", CaddyHostLoopback, config.AppSecPort)
		mc.AccessLog = caddyLogDir + "/access.log"
		if err := crowdSec(lab, out); err != nil {
			return nil, err
		}
	}
	main, err := caddy.Main(mc)
	if err != nil {
		return nil, err
	}
	out.Caddyfile = File{Name: "Caddyfile", Content: main}
	return out, nil
}

// caddyImage is the Quadlet that downloads Roostack's Caddy image
// (CaddyImage); Caddy's container waits for it.
func caddyImage(lab config.Lab) (File, error) {
	var b quadlet.Unit
	b.Section("Unit").Add("Description", "Roostack: download the reverse proxy image (Caddy with its plugins)")
	is := b.Section("Image")
	is.Add("Image", CaddyImage)
	bsvc := b.Section("Service")
	// Only when it isn't here: a pull asks the registry even for an image
	// it has, which fails without the internet, and Caddy would then not
	// start after a reboot (docs/ASSUMPTIONS.md A10). Skipped, the unit
	// doesn't fail, and Caddy starts.
	bsvc.Add("ExecCondition", `/bin/sh -c "! /usr/bin/podman image exists `+CaddyImage+`"`)
	bsvc.Add("TimeoutStartSec", "900")
	// Its output goes to a file the UI can show (config.Paths.CaddyImageLog).
	bsvc.Add("StandardOutput", "truncate:"+lab.Paths.CaddyImageLog())
	bsvc.Add("StandardError", "inherit")
	if err := b.Validate(); err != nil {
		return File{}, err
	}
	return File{Name: CaddyUnit + ".image", Content: b.String()}, nil
}

// Edge generates an edge's Caddy (SPEC.md §10.5), on a server that isn't
// the controller (nodes have no Caddy otherwise): the same unit and image
// as the controller's, publishing TCP 80 and 443 only (no HTTP/3: a PROXY
// header can't come with it, docs/ASSUMPTIONS.md Q7), with no
// certificates, DNS logins or sites of its own: just c, passed on to the
// controller over Roostack's network. Like the controller's, it's on pasta,
// which keeps visitors' addresses for the PROXY header.
func Edge(lab config.Lab, c caddy.EdgeConfig) (*SystemOutput, error) {
	conf, err := caddy.Edge(c)
	if err != nil {
		return nil, err
	}
	var u quadlet.Unit
	u.Section("Unit").Add("Description", "Roostack: the lab's edge (Caddy, passing public visitors on)")
	cs := u.Section("Container")
	cs.Add("ContainerName", CaddyUnit)
	cs.Add("Image", CaddyUnit+".image")
	addTimezone(cs, lab)
	cs.Add("Network", "pasta")
	cs.Add("PublishPort", "80:80")
	cs.Add("PublishPort", "443:443")
	cs.Add("Volume", lab.Paths.CaddyDir()+":/etc/caddy:ro")
	cs.Add("Volume", lab.Paths.CaddyDataDir()+":/data")
	cs.Add("Label", "io.roostack.service=_system")
	cs.Add("Label", "io.roostack.component=edge")
	svc := u.Section("Service")
	svc.Add("Slice", CoreSlice)
	// The user manager gives its units 200; 100 is as low as it lets one
	// go, so the kernel picks a service's container before Caddy.
	svc.Add("OOMScoreAdjust", "100")
	svc.Add("ExecReload", "/usr/bin/podman exec "+CaddyUnit+" caddy reload --config /etc/caddy/Caddyfile")
	svc.Add("Restart", "always")
	svc.Add("TimeoutStartSec", "900")
	u.Section("Install").Add("WantedBy", "default.target")
	if err := u.Validate(); err != nil {
		return nil, err
	}
	image, err := caddyImage(lab)
	if err != nil {
		return nil, err
	}
	return &SystemOutput{
		Quadlets:  []File{{Name: CaddyUnit + ".container", Content: u.String()}, image},
		Caddyfile: File{Name: "Caddyfile", Content: conf},
		Dirs:      []string{lab.Paths.CaddyDataDir()},
		Units:     []string{CaddyUnit + ".service"},
	}, nil
}

// PrivateFrom is who reaches the lab's private addresses (SPEC.md §6.12):
// the home network, or on a lab on the internet only this server itself
// (an SSH tunnel).
func PrivateFrom(lab config.Lab) string {
	if lab.OnInternet() {
		return caddy.ThisServerOnly(lab.HostIP, CaddyHostLoopback)
	}
	return caddy.PrivateRanges
}

// crowdSecAcquis tells CrowdSec to read Caddy's access log.
const crowdSecAcquis = `# Generated by Roostack. Do not edit: changes will be overwritten.
# CrowdSec reads Caddy's access log (SPEC.md §6.11).
filenames:
  - ` + caddyLogDir + `/*.log
labels:
  type: caddy
`

// crowdSecAppSec turns on CrowdSec's AppSec component (SPEC.md §6.10
// item 4) with the Hub's default AppSec settings, which run the virtual
// patches and generic rules (CrowdSecCollections) and block what they
// match.
var crowdSecAppSec = fmt.Sprintf(`# Generated by Roostack. Do not edit: changes will be overwritten.
# CrowdSec's AppSec component checks each request to a public service
# before Caddy passes it on (SPEC.md §6.10).
source: appsec
name: roostack
listen_addr: 0.0.0.0:%d
appsec_configs:
  - crowdsecurity/appsec-default
labels:
  type: appsec
`, appSecPort)

// crowdSecHub pins CrowdSec's Hub to one commit (CrowdSecHub), in
// config.yaml.local, which CrowdSec reads over its own config.yaml. Each
// start of the container fetches the index (it looks for a recent one
// under the usual name, which this isn't) and its "hub upgrade" brings
// every item to that commit. The index is named after the commit, so an
// older commit's is never used for a new one: if GitHub can't be reached
// just after Roostack moves to a new commit, CrowdSec doesn't start until
// it can (docs/ASSUMPTIONS.md P10).
var crowdSecHub = fmt.Sprintf(`# Generated by Roostack. Do not edit: changes will be overwritten.
# CrowdSec's Hub, pinned to one commit (SPEC.md §6.10).
config_paths:
  index_path: /etc/crowdsec/hub/.index-%s.json
cscli:
  hub_branch: %s
  __hub_url_template__: https://raw.githubusercontent.com/crowdsecurity/hub/%%s/%%s
`, CrowdSecHub, CrowdSecHub)

// crowdSec adds CrowdSec, while anything is public: it reads Caddy's
// access log, bans addresses that attack, and Caddy's bouncer turns them
// away (checked on a VM: docs/ASSUMPTIONS.md P1-P5); its AppSec component
// checks requests as they come (P10). Its local API and AppSec are on the
// host's loopback, where Caddy reaches them through pasta.
func crowdSec(lab config.Lab, out *SystemOutput) error {
	dir := lab.Paths.CrowdSecDir()
	var u quadlet.Unit
	u.Section("Unit").Add("Description", "Roostack: CrowdSec, which blocks attackers of public services")
	cs := u.Section("Container")
	cs.Add("ContainerName", CrowdSecUnit)
	cs.Add("Image", CrowdSecImage)
	addTimezone(cs, lab)
	cs.Add("PublishPort", fmt.Sprintf("127.0.0.1:%d:8080", config.CrowdSecPort))
	cs.Add("PublishPort", fmt.Sprintf("127.0.0.1:%d:%d", config.AppSecPort, appSecPort))
	// Quoted: systemd splits an unquoted Environment= at spaces, which
	// left only the first collection (seen on VM1, 2026-10-19).
	cs.Env("COLLECTIONS", CrowdSecCollections)
	if lab.Public.NoSharing {
		// The Owner's opt-out: nothing goes to CrowdSec's central API.
		cs.Add("Environment", "DISABLE_ONLINE_API=true")
	}
	// Registers Caddy's bouncer with this key.
	cs.Add("Secret", CrowdSecBouncerKey+",type=env,target=BOUNCER_KEY_caddy")
	cs.Add("Volume", dir+"/config:/etc/crowdsec")
	cs.Add("Volume", dir+"/data:/var/lib/crowdsec/data")
	cs.Add("Volume", dir+"/acquis.yaml:/etc/crowdsec/acquis.d/caddy.yaml:ro")
	cs.Add("Volume", dir+"/appsec.yaml:/etc/crowdsec/acquis.d/appsec.yaml:ro")
	cs.Add("Volume", dir+"/config.yaml.local:/etc/crowdsec/config.yaml.local:ro")
	cs.Add("Volume", lab.Paths.CaddyLogDir()+":"+caddyLogDir+":ro")
	cs.Add("Label", "io.roostack.service=_system")
	cs.Add("Label", "io.roostack.component=crowdsec")
	svc := u.Section("Service")
	svc.Add("Slice", ServicesSlice) // with the services: Caddy carries on without it
	svc.Add("Restart", "always")
	// The first start downloads the image and the Hub's collections.
	svc.Add("TimeoutStartSec", "900")
	u.Section("Install").Add("WantedBy", "default.target")
	if err := u.Validate(); err != nil {
		return err
	}
	out.Quadlets = append(out.Quadlets, File{Name: CrowdSecUnit + ".container", Content: u.String()})
	q := CrowdSecUnit + ".container"
	out.Files = append(out.Files, File{Name: dir + "/acquis.yaml", Content: crowdSecAcquis, Restarts: q},
		File{Name: dir + "/appsec.yaml", Content: crowdSecAppSec, Restarts: q},
		File{Name: dir + "/config.yaml.local", Content: crowdSecHub, Restarts: q})
	out.Secrets = append(out.Secrets, Secret{Name: CrowdSecBouncerKey, Generate: true})
	out.Dirs = append(out.Dirs, lab.Paths.CaddyLogDir(), dir+"/config", dir+"/data")
	out.Units = append(out.Units, CrowdSecUnit+".service")
	return nil
}

// Glances, a live view of a server (SPEC.md §6.15.1): its official image,
// pinned like every other (SPEC.md §8.1), and where it listens. To
// update: the release's tag, its index's digest (Docker Hub's
// Docker-Content-Digest for the tag), then check on a VM that it starts
// and shows the host's processes.
const (
	GlancesImage = "docker.io/nicolargo/glances:4.5.9@sha256:1d994d181732cfbf68b7c5826a1f71b89624e3eaaa2fe28b28048b2f61b200d7"
	GlancesUnit  = "roostack-glances"
	GlancesPort  = 61208
)

// glances adds Glances, while an Owner or Admin has it on for this server:
// it sees the host's processes and network (--pid=host, the host's
// network) and reads /sys, but has no Podman socket and nothing it can
// write. Its web server listens at GlancesBind (the host's loopback on
// the controller, its address on Roostack's network elsewhere), where the
// controller's Caddy reaches it, behind single sign-on.
func glances(lab config.Lab, out *SystemOutput) error {
	if net.ParseIP(lab.GlancesBind) == nil {
		return fmt.Errorf("glances: invalid address %q", lab.GlancesBind)
	}
	var u quadlet.Unit
	u.Section("Unit").Add("Description", "Roostack: Glances, a live view of this server")
	cs := u.Section("Container")
	cs.Add("ContainerName", GlancesUnit)
	cs.Add("Image", GlancesImage)
	addTimezone(cs, lab)
	cs.Add("Network", "host")
	cs.Add("PodmanArgs", "--pid=host")
	cs.Add("Volume", "/sys:/sys:ro")
	cs.Add("ReadOnly", "true")
	cs.Add("Tmpfs", "/tmp")
	cs.Add("NoNewPrivileges", "true")
	cs.Add("DropCapability", "ALL")
	cs.Env("GLANCES_OPT", fmt.Sprintf("-w --disable-autodiscover --disable-check-update -B %s -p %d", lab.GlancesBind, GlancesPort))
	cs.Add("Label", "io.roostack.service=_system")
	cs.Add("Label", "io.roostack.component=glances")
	svc := u.Section("Service")
	svc.Add("Slice", ServicesSlice)
	svc.Add("Restart", "always")
	// The first start downloads the image.
	svc.Add("TimeoutStartSec", "900")
	u.Section("Install").Add("WantedBy", "default.target")
	if err := u.Validate(); err != nil {
		return err
	}
	out.Quadlets = append(out.Quadlets, File{Name: GlancesUnit + ".container", Content: u.String()})
	out.Units = append(out.Units, GlancesUnit+".service")
	return nil
}

// dnsConfig returns the Caddyfile directive and the credential fields for
// the lab's DNS provider.
func dnsConfig(lab config.Lab) (string, []string, error) {
	p := dns.Get(lab.DNS.Provider)
	if p == nil {
		return "", nil, fmt.Errorf("unknown DNS provider %q", lab.DNS.Provider)
	}
	// Caddy's own certificates only ever for a name under .localhost
	// (SPEC.md §6.6), whatever lab.yaml says: config.Lab.Validate refuses
	// it too.
	if lab.TestAddress() != config.LocalhostName(lab.Domain) {
		return "", nil, fmt.Errorf("a test address must be a name under %s, and such a name a test address: got %q with DNS provider %q",
			config.TestAddressSuffix, lab.Domain, lab.DNS.Provider)
	}
	fields := lab.DNS.Fields
	if len(fields) == 0 {
		for _, f := range p.Fields {
			if !f.Optional {
				fields = append(fields, f.Key)
			}
		}
	}
	directive := lab.DNS.Directive
	if directive == "" {
		var err error
		if directive, err = p.Directive(fields, lab.DNS.Values); err != nil {
			return "", nil, err
		}
	}
	return directive, fields, nil
}

// DomainRedirect is where the domain itself goes when it's a redirect
// (SPEC.md §5.3): Your account, a web address, or a service's first
// address, from first (each service's, by name; one turned off answers
// that it is); "" otherwise, and for a service that isn't there.
func DomainRedirect(lab config.Lab, first map[string]string) string {
	switch lab.Shows() {
	case config.ShowAccount:
		return "https://" + lab.SignInHost() + "/"
	case config.ShowAddress:
		return lab.DomainItself.Address
	case config.ShowService:
		if h := first[lab.DomainItself.Service]; h != "" {
			return "https://" + h + "/"
		}
	}
	return ""
}
