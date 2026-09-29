package generate

import (
	"fmt"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/config"
	"roostack.cloud/roostack/internal/dns"
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
	Secrets   []Secret
	Dirs      []string
	Units     []string
}

// System generates the lab-wide units.
func System(lab config.Lab) (*SystemOutput, error) {
	out := &SystemOutput{}
	if lab.IsNode() {
		// Nodes have no Caddy: the controller's serves their routes.
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

	var b quadlet.Unit
	b.Section("Unit").Add("Description", "Roostack: build the reverse proxy image (Caddy with DNS plugins)")
	bs := b.Section("Build")
	bs.Add("ImageTag", CaddyImage)
	bs.Add("File", CaddyBuildDir+"/Containerfile")
	bs.Add("SetWorkingDirectory", "file")
	// The first build compiles Caddy: minutes, not seconds.
	b.Section("Service").Add("TimeoutStartSec", "1800")
	if err := b.Validate(); err != nil {
		return nil, err
	}
	out.Quadlets = append(out.Quadlets, File{Name: CaddyUnit + ".build", Content: b.String()})

	main, err := caddy.Main(caddy.MainConfig{
		Domain:        lab.Domain,
		Email:         lab.Email,
		DNSDirective:  directive,
		AdminUpstream: fmt.Sprintf("%s:%d", CaddyHostLoopback, lab.UIPort),
	})
	if err != nil {
		return nil, err
	}
	out.Caddyfile = File{Name: "Caddyfile", Content: main}
	out.Dirs = []string{lab.Paths.CaddyDataDir(), lab.Paths.CaddySitesDir()}
	out.Units = []string{CaddyUnit + ".service"}
	return out, nil
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
		directive = p.Directive(fields)
	}
	return directive, fields, nil
}
