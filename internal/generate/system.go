package generate

import (
	"fmt"

	"github.com/mylab/mylab/internal/caddy"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/dns"
	"github.com/mylab/mylab/internal/quadlet"
)

// DefaultCaddyImage is MyLab's Caddy build with DNS plugins
// (build/caddy/Containerfile). TODO: publish and pin by digest.
const DefaultCaddyImage = "localhost/mylab-caddy:latest"

// CaddyUnit is the systemd service running the reverse proxy.
const CaddyUnit = "mylab-caddy"

// CaddyHostLoopback is the address inside Caddy's network namespace that
// pasta maps to the host's loopback (--map-host-loopback). Caddy reaches
// services (published on 127.0.0.1:<port>) and the MyLab UI through it.
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

	var u quadlet.Unit
	u.Section("Unit").Add("Description", "MyLab: reverse proxy (Caddy)")
	cs := u.Section("Container")
	cs.Add("ContainerName", CaddyUnit)
	cs.Add("Image", DefaultCaddyImage)
	// Caddy has its own pasta network namespace, not a bridge network:
	// pasta's port forwarding keeps each visitor's real IP, which the
	// "private" route check depends on. Rootless bridge networks replace it
	// (docs/ASSUMPTIONS.md C4). --map-host-loopback lets Caddy reach
	// services published on the host's 127.0.0.1.
	cs.Add("Network", "pasta:--map-host-loopback,"+CaddyHostLoopback)
	// Needs net.ipv4.ip_unprivileged_port_start=80, set by mylab-agent
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
		out.Secrets = append(out.Secrets, Secret{Name: dns.SecretName(f)})
	}
	cs.Add("Label", "io.mylab.service=_system")
	cs.Add("Label", "io.mylab.component=caddy")
	svc := u.Section("Service")
	svc.Add("ExecReload", "/usr/bin/podman exec "+CaddyUnit+" caddy reload --config /etc/caddy/Caddyfile")
	svc.Add("Restart", "always")
	svc.Add("TimeoutStartSec", "900")
	u.Section("Install").Add("WantedBy", "default.target")
	if err := u.Validate(); err != nil {
		return nil, err
	}
	out.Quadlets = append(out.Quadlets, File{Name: CaddyUnit + ".container", Content: u.String()})

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
