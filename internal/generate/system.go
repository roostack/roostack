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

	var net quadlet.Unit
	net.Section("Unit").Add("Description", "MyLab: proxy network")
	net.Section("Network").Add("NetworkName", ProxyNetwork)
	out.Quadlets = append(out.Quadlets, File{Name: ProxyNetwork + ".network", Content: net.String()})

	var u quadlet.Unit
	u.Section("Unit").Add("Description", "MyLab: reverse proxy (Caddy)")
	cs := u.Section("Container")
	cs.Add("ContainerName", CaddyUnit)
	cs.Add("Image", DefaultCaddyImage)
	cs.Add("Network", ProxyNetwork+".network")
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
		AdminUpstream: fmt.Sprintf("host.containers.internal:%d", lab.UIPort),
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
