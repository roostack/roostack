package generate

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/mylab/mylab/internal/config"
	"github.com/mylab/mylab/internal/instance"
	"github.com/mylab/mylab/internal/pkgspec"
)

// Context is the data available to package templates, e.g.
// {{ .Routes.web.Host }} or {{ .Settings.signups_allowed }}.
//
// Secret settings are deliberately absent from Settings, so a template can
// never copy a secret into a generated (readable) unit file.
type Context struct {
	Service   ServiceContext
	Lab       LabContext
	Settings  map[string]any
	Routes    map[string]RouteContext
	Ports     map[string]PortContext
	Databases map[string]DatabaseContext
	// Containers lets containers find each other, e.g.
	// {{ (index .Containers "machine-learning").Host }}.
	Containers map[string]ContainerContext
	// Inputs are an action hook's inputs (non-secret only).
	Inputs map[string]string
}

// ContainerContext describes a sibling container.
type ContainerContext struct {
	Host string // container name on the service network
}

// ServiceContext describes the service being generated.
type ServiceContext struct {
	Name string
}

// LabContext exposes lab-wide values.
type LabContext struct {
	Domain string
}

// RouteContext is a resolved route.
type RouteContext struct {
	Subdomain string
	Host      string // e.g. passwords.lab.example.com
	URL       string // e.g. https://passwords.lab.example.com
	Exposure  string
	// Port is the host loopback port the route's container is published
	// on (0 until allocated).
	Port int
}

// PortContext is a resolved package port, e.g. for an app to show
// "ssh://git@host:{{ .Ports.ssh.HostPort }}" in its clone URLs.
type PortContext struct {
	Enabled  bool
	HostPort int
	Protocol string
	Exposure string
}

// DatabaseContext gives an app what it needs to connect to its database.
type DatabaseContext struct {
	Host string // container name on the service network
	Port int
	User string
	Name string
}

func buildContext(lab config.Lab, p *pkgspec.Package, in *instance.Instance) *Context {
	ctx := &Context{
		Service:    ServiceContext{Name: p.Name},
		Lab:        LabContext{Domain: lab.Domain},
		Settings:   map[string]any{},
		Routes:     map[string]RouteContext{},
		Ports:      map[string]PortContext{},
		Databases:  map[string]DatabaseContext{},
		Containers: map[string]ContainerContext{},
	}
	for name := range p.Containers {
		ctx.Containers[name] = ContainerContext{Host: containerUnitName(p.Name, name)}
	}

	for _, s := range p.Settings {
		if s.Type == pkgspec.SettingSecret {
			continue
		}
		val, ok := in.Settings[s.Key]
		if !ok {
			val = defaultValue(s, lab)
		}
		ctx.Settings[s.Key] = val
	}

	for name, r := range p.Routes {
		sub, exp, port := r.Subdomain, r.Exposure, 0
		if o, ok := in.Routes[name]; ok {
			if o.Subdomain != "" {
				sub = o.Subdomain
			}
			if o.Exposure != "" {
				exp = o.Exposure
			}
			port = o.Port
		}
		host := sub + "." + lab.Domain
		ctx.Routes[name] = RouteContext{Subdomain: sub, Host: host, URL: "https://" + host, Exposure: exp, Port: port}
	}

	ctx.Ports = ResolvePorts(p, in)

	for name, d := range p.Databases {
		switch d.Type {
		case pkgspec.DBPostgres:
			ctx.Databases[name] = DatabaseContext{
				Host: dbUnitName(p.Name, name), Port: 5432,
				User: pgIdent(p.Name, name), Name: pgIdent(p.Name, name),
			}
		case pkgspec.DBRedis:
			ctx.Databases[name] = DatabaseContext{Host: dbUnitName(p.Name, name), Port: 6379}
		}
	}
	return ctx
}

// ResolvePorts applies an instance's port overrides to the package's
// defaults.
func ResolvePorts(p *pkgspec.Package, in *instance.Instance) map[string]PortContext {
	ports := map[string]PortContext{}
	for name, pp := range p.Ports {
		pc := PortContext{Enabled: pp.Enabled, HostPort: pp.HostPort, Protocol: pp.Proto(), Exposure: pp.DefaultExposure()}
		if o, ok := in.Ports[name]; ok {
			if o.Enabled != nil {
				pc.Enabled = *o.Enabled
			}
			if o.HostPort != 0 {
				pc.HostPort = o.HostPort
			}
			if o.Exposure != "" {
				pc.Exposure = o.Exposure
			}
		}
		ports[name] = pc
	}
	return ports
}

func defaultValue(s pkgspec.Setting, lab config.Lab) any {
	if s.Default == pkgspec.DefaultAuto {
		return lab.HasGPU // docs/ASSUMPTIONS.md G1
	}
	if s.Default != nil {
		return s.Default
	}
	switch s.Type {
	case pkgspec.SettingBool:
		return false
	case pkgspec.SettingInt:
		return 0
	}
	return ""
}

// pgIdent returns a Postgres-safe user/database name: the service name, plus
// the database name when it isn't "main".
func pgIdent(service, db string) string {
	id := service
	if db != "main" {
		id += "_" + db
	}
	return strings.ReplaceAll(id, "-", "_")
}

// render executes a template string against the context. Strings without
// template syntax are returned unchanged.
func (c *Context) render(where, s string) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	t, err := template.New(where).Option("missingkey=error").Parse(s)
	if err != nil {
		return "", fmt.Errorf("%s: %w", where, err)
	}
	var b strings.Builder
	if err := t.Execute(&b, c); err != nil {
		return "", fmt.Errorf("%s: %w", where, err)
	}
	return b.String(), nil
}

// enabled evaluates a `when:` condition. Empty means always enabled.
func (c *Context) enabled(where, when string) (bool, error) {
	if when == "" {
		return true, nil
	}
	out, err := c.render(where, when)
	if err != nil {
		return false, err
	}
	switch strings.TrimSpace(out) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, fmt.Errorf("%s: condition must render to true or false, got %q", where, out)
}
