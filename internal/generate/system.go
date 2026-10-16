package generate

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"time"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
	"roostack.cloud/roostack/internal/errpage"
	"roostack.cloud/roostack/internal/quadlet"
)

// CaddyImage is Roostack's Caddy build with DNS plugins, built on each
// server from CaddyBuildDir by a Quadlet build unit (so a new install, or
// an upgrade that changes the Containerfile, needs no manual step). The
// Containerfile pins what it's built from; the local tag is the result.
const CaddyImage = "localhost/roostack-caddy:latest"

// CaddyBuildDir holds the Containerfile (build/caddy in the source,
// installed by the package).
const CaddyBuildDir = "/usr/share/roostack/caddy"

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
	Files   []File
	Secrets []Secret
	Dirs    []string
	Units   []string
}

// CrowdSec (SPEC.md §6.10, §6.11): its image, pinned like every other
// (SPEC.md §8.1), and names.
const (
	CrowdSecImage = "docker.io/crowdsecurity/crowdsec:v1.8.1@sha256:0f2523fa61ef507f15d953045cface490cc880670c62f2755ced17524107f71a"
	CrowdSecUnit  = "roostack-crowdsec"
	// CrowdSecBouncerKey is the Podman secret with the key Caddy's bouncer
	// uses on CrowdSec's local API; Roostack makes it.
	CrowdSecBouncerKey = "roostack-crowdsec-bouncer-key"
	// caddyLogDir holds Caddy's access log inside its container: the
	// host's config.Paths.CaddyLogDir().
	caddyLogDir = "/var/log/caddy"
)

// System generates the lab-wide units. public says whether any service is
// public (Output.Public): then CrowdSec runs, Caddy logs requests for it,
// and its bouncer checks each public request (SPEC.md §6.11).
func System(lab config.Lab, public bool) (*SystemOutput, error) {
	out := &SystemOutput{}
	if lab.IsNode() {
		// Nodes have no Caddy: the controller's serves their routes.
		return out, nil
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
	cs.Add("Image", CaddyUnit+".build")
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
	if len(lab.EdgeAddrs) > 0 {
		// The edges' own listener (caddy.EdgePort); the firewall lets only
		// the edges reach it.
		cs.Add("PublishPort", fmt.Sprintf("%d:%d", caddy.EdgePort, caddy.EdgePort))
	}
	cs.Add("Volume", lab.Paths.CaddyDir()+":/etc/caddy:ro")
	cs.Add("Volume", lab.Paths.CaddyDataDir()+":/data")
	// DNS provider credentials, one Podman secret per field (internal/dns).
	directive, fields, err := dnsConfig(lab)
	if err != nil {
		return nil, err
	}
	for _, f := range fields {
		cs.Add("Secret", fmt.Sprintf("%s,type=env,target=%s", dns.SecretName(f), dns.EnvName(f)))
		// Entered by the user in the setup wizard.
		out.Secrets = append(out.Secrets, Secret{Name: dns.SecretName(f), Holds: CaddyUnit + ".container"})
	}
	// The previous address's provider, while it still works (SPEC.md
	// §6.6): its certificate may need renewing meanwhile.
	old := lab.Old(time.Now())
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
	svc.Add("ExecReload", "/usr/bin/podman exec "+CaddyUnit+" caddy reload --config /etc/caddy/Caddyfile")
	svc.Add("Restart", "always")
	svc.Add("TimeoutStartSec", "900")
	u.Section("Install").Add("WantedBy", "default.target")
	if err := u.Validate(); err != nil {
		return nil, err
	}
	out.Quadlets = append(out.Quadlets, File{Name: CaddyUnit + ".container", Content: u.String()})

	build, err := caddyBuild(lab)
	if err != nil {
		return nil, err
	}
	out.Quadlets = append(out.Quadlets, build)

	mc := caddy.MainConfig{
		Domain:        lab.Domain,
		Email:         lab.Email,
		DNSDirective:  directive,
		AdminUpstream: fmt.Sprintf("%s:%d", CaddyHostLoopback, lab.UIPort),
		AdminPublic:   lab.PublicUI(),
		PrivateFrom:   PrivateFrom(lab),
		EdgeFrom:      lab.EdgeAddrs,
	}
	mc.AdminHost = lab.UIHost()
	if old != nil {
		mc.OldDomain, mc.OldDNSDirective = old.Domain, oldDirective
		mc.OldAdminHost = config.Lab{Domain: old.Domain, DNS: old.DNS}.UIHost()
	}
	out.Dirs = []string{lab.Paths.CaddyDataDir(), lab.Paths.CaddySitesDir(), filepath.Join(lab.Paths.CaddyDir(), "errors")}
	// The services' error pages (SPEC.md §6.3), which Caddy's handle_errors
	// serves from caddy.ErrorPagesDir.
	pages := errpage.CaddyPages(lab.DisplayName(), "https://"+lab.UIHost())
	for _, name := range slices.Sorted(maps.Keys(pages)) {
		out.Files = append(out.Files, File{Name: filepath.Join(lab.Paths.CaddyDir(), "errors", name), Content: pages[name]})
	}
	out.Units = []string{CaddyUnit + ".service"}
	if public {
		mc.CrowdSecAPI = fmt.Sprintf("http://%s:%d", CaddyHostLoopback, config.CrowdSecPort)
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

// caddyBuild is the Quadlet that builds Roostack's Caddy image (with its
// plugins) on the server.
func caddyBuild(lab config.Lab) (File, error) {
	var b quadlet.Unit
	b.Section("Unit").Add("Description", "Roostack: build the reverse proxy image (Caddy with DNS plugins)")
	bs := b.Section("Build")
	bs.Add("ImageTag", CaddyImage)
	bs.Add("File", CaddyBuildDir+"/Containerfile")
	bs.Add("SetWorkingDirectory", "file")
	// The first build compiles Caddy: minutes, not seconds.
	bsvc := b.Section("Service")
	bsvc.Add("TimeoutStartSec", "1800")
	// Its output goes to a file the UI can show (config.Paths.CaddyBuildLog).
	bsvc.Add("StandardOutput", "truncate:"+lab.Paths.CaddyBuildLog())
	bsvc.Add("StandardError", "inherit")
	if err := b.Validate(); err != nil {
		return File{}, err
	}
	return File{Name: CaddyUnit + ".build", Content: b.String()}, nil
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
	cs.Add("Image", CaddyUnit+".build")
	addTimezone(cs, lab)
	cs.Add("Network", "pasta")
	cs.Add("PublishPort", "80:80")
	cs.Add("PublishPort", "443:443")
	cs.Add("Volume", lab.Paths.CaddyDir()+":/etc/caddy:ro")
	cs.Add("Volume", lab.Paths.CaddyDataDir()+":/data")
	cs.Add("Label", "io.roostack.service=_system")
	cs.Add("Label", "io.roostack.component=edge")
	svc := u.Section("Service")
	svc.Add("ExecReload", "/usr/bin/podman exec "+CaddyUnit+" caddy reload --config /etc/caddy/Caddyfile")
	svc.Add("Restart", "always")
	svc.Add("TimeoutStartSec", "900")
	u.Section("Install").Add("WantedBy", "default.target")
	if err := u.Validate(); err != nil {
		return nil, err
	}
	build, err := caddyBuild(lab)
	if err != nil {
		return nil, err
	}
	return &SystemOutput{
		Quadlets:  []File{{Name: CaddyUnit + ".container", Content: u.String()}, build},
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

// crowdSec adds CrowdSec, while anything is public: it reads Caddy's
// access log, bans addresses that attack, and Caddy's bouncer turns them
// away (checked on a VM: docs/ASSUMPTIONS.md P1-P5). Its local API is on
// the host's loopback, where Caddy reaches it through pasta.
func crowdSec(lab config.Lab, out *SystemOutput) error {
	dir := lab.Paths.CrowdSecDir()
	var u quadlet.Unit
	u.Section("Unit").Add("Description", "Roostack: CrowdSec, which blocks attackers of public services")
	cs := u.Section("Container")
	cs.Add("ContainerName", CrowdSecUnit)
	cs.Add("Image", CrowdSecImage)
	addTimezone(cs, lab)
	cs.Add("PublishPort", fmt.Sprintf("127.0.0.1:%d:8080", config.CrowdSecPort))
	// The Caddy parsers and HTTP attack scenarios (from the CrowdSec Hub).
	cs.Add("Environment", "COLLECTIONS=crowdsecurity/caddy")
	if lab.Public.NoSharing {
		// The Owner's opt-out: nothing goes to CrowdSec's central API.
		cs.Add("Environment", "DISABLE_ONLINE_API=true")
	}
	// Registers Caddy's bouncer with this key.
	cs.Add("Secret", CrowdSecBouncerKey+",type=env,target=BOUNCER_KEY_caddy")
	cs.Add("Volume", dir+"/config:/etc/crowdsec")
	cs.Add("Volume", dir+"/data:/var/lib/crowdsec/data")
	cs.Add("Volume", dir+"/acquis.yaml:/etc/crowdsec/acquis.d/caddy.yaml:ro")
	cs.Add("Volume", lab.Paths.CaddyLogDir()+":"+caddyLogDir+":ro")
	cs.Add("Label", "io.roostack.service=_system")
	cs.Add("Label", "io.roostack.component=crowdsec")
	svc := u.Section("Service")
	svc.Add("Restart", "always")
	// The first start downloads the image and the Hub's collections.
	svc.Add("TimeoutStartSec", "900")
	u.Section("Install").Add("WantedBy", "default.target")
	if err := u.Validate(); err != nil {
		return err
	}
	out.Quadlets = append(out.Quadlets, File{Name: CrowdSecUnit + ".container", Content: u.String()})
	out.Files = append(out.Files, File{Name: dir + "/acquis.yaml", Content: crowdSecAcquis})
	out.Secrets = append(out.Secrets, Secret{Name: CrowdSecBouncerKey, Generate: true})
	out.Dirs = append(out.Dirs, lab.Paths.CaddyLogDir(), dir+"/config", dir+"/data")
	out.Units = append(out.Units, CrowdSecUnit+".service")
	return nil
}

// dnsConfig returns the Caddyfile directive and the credential fields for
// the lab's DNS provider.
func dnsConfig(lab config.Lab) (string, []string, error) {
	p := dns.Get(lab.DNS.Provider)
	if p == nil {
		return "", nil, fmt.Errorf("unknown DNS provider %q", lab.DNS.Provider)
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
