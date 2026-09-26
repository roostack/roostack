package generate

import (
	"fmt"

	"github.com/mylab/mylab/internal/caddy"
	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/quadlet"
)

// DefaultCaddyImage is MyLab's Caddy build with DNS plugins
// (build/caddy/Containerfile). TODO: publish and pin by digest.
const DefaultCaddyImage = "localhost/mylab-caddy:latest"

// CaddyUnit is the systemd service running the reverse proxy.
const CaddyUnit = "mylab-caddy"

// CaddyDNSSecret holds the DNS provider API token.
const CaddyDNSSecret = "mylab-caddy-dns-token"

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
	cs.Add("Secret", fmt.Sprintf("%s,type=env,target=%s", CaddyDNSSecret, caddy.DNSTokenEnv))
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
		DNSProvider:   lab.DNS.Provider,
		DNSDirective:  lab.DNS.Directive,
		AdminUpstream: fmt.Sprintf("%s:%d", CaddyHostLoopback, lab.UIPort),
	})
	if err != nil {
		return nil, err
	}
	out.Caddyfile = File{Name: "Caddyfile", Content: main}
	// The DNS token is entered by the user during setup.
	out.Secrets = []Secret{{Name: CaddyDNSSecret}}
	out.Dirs = []string{lab.Paths.CaddyDataDir(), lab.Paths.CaddySitesDir()}
	out.Units = []string{CaddyUnit + ".service"}
	return out, nil
}
