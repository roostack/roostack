package pkgspec

import (
	"strings"
	"testing"
)

// policyBase follows the package policy; each case changes one thing.
const policyBase = `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about:
  maker: Example and volunteers
  source: https://example.org/app
  licence: AGPL-3.0-only
  icon: app.png
  categories: [documents]
requires: { roostack: "0.6" }
containers:
  web:
    image: example.org/app:1@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
    memory: 300 MB
storage:
  data: { class: critical }
databases:
  main: { type: postgres }
routes:
  web:
    container: web
    port: 80
    subdomain: app
    public:
      login_paths: [/login]
ports:
  ssh: { title: SSH, container: web, port: 22, host_port: 2222 }
`

const otherDigest = "@sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

// Every rule of the package policy (SPEC.md §8.3.4), with a case that
// passes and one that doesn't. want is part of the one problem expected,
// or "" for none.
func TestPolicy(t *testing.T) {
	quadlet := func(section, key string, value any) func(*Package) {
		return func(p *Package) { p.Containers["web"].Quadlet = Quadlet{section: {key: value}} }
	}
	tests := []struct {
		name   string
		change func(*Package)
		want   string
	}{
		{"base passes", func(*Package) {}, ""},

		// Images pinned by digest.
		{"image by tag only", func(p *Package) { p.Containers["web"].Image = "example.org/app:1" }, "containers.web.image: pin the image by digest"},
		{"image digest too short", func(p *Package) { p.Containers["web"].Image = "example.org/app:1@sha256:0123" }, "containers.web.image: pin"},
		{"database image pinned", func(p *Package) { p.Databases["main"].Image = "example.org/pg:16" + otherDigest }, ""},
		{"database image by tag", func(p *Package) { p.Databases["main"].Image = "example.org/pg:16" }, "databases.main.image: pin"},

		// Folders: only storage: (an anonymous volume or a tmpfs holds
		// nothing beyond the container).
		{"anonymous volume", quadlet("Container", "Volume", "/scratch"), ""},
		{"volume with a host path", quadlet("Container", "Volume", "/srv/roostack:/data"), "Container.Volume=/srv/roostack:/data isn't allowed: a folder from the server"},
		{"volume with a specifier", quadlet("Container", "Volume", "%h:/home:ro"), "Container.Volume=%h:/home:ro"},
		{"named volume", quadlet("Container", "Volume", "shared:/data"), "Container.Volume=shared:/data"},
		{"tmpfs mount", quadlet("Container", "Mount", "type=tmpfs,destination=/run/app"), ""},
		{"bind mount", quadlet("Container", "Mount", "type=bind,source=/etc,destination=/etc"), "Container.Mount=type=bind"},
		{"mount without a type", quadlet("Container", "Mount", "source=/etc,destination=/etc"), "Container.Mount="},
		{"rootfs", quadlet("Container", "Rootfs", "/"), "Container.Rootfs=/ isn't allowed"},
		{"environment file", quadlet("Container", "EnvironmentFile", "/etc/roostack/env"), "Container.EnvironmentFile="},

		// Podman itself.
		{"podman args", quadlet("Container", "PodmanArgs", "--log-level=debug"), "Container.PodmanArgs=--log-level=debug isn't allowed"},
		{"global args", quadlet("Container", "GlobalArgs", "--root=/tmp"), "Container.GlobalArgs="},
		{"drop capability", quadlet("Container", "DropCapability", "ALL"), ""},
		{"add capability", quadlet("Container", "AddCapability", "SYS_ADMIN"), "Container.AddCapability=SYS_ADMIN"},
		{"security label disable", quadlet("Container", "SecurityLabelDisable", true), "Container.SecurityLabelDisable=true"},

		// Networks and ports.
		{"network", quadlet("Container", "Network", "host"), "Container.Network=host isn't allowed: Roostack puts each service on its own network"},
		{"pod", quadlet("Container", "Pod", "other.pod"), "Container.Pod="},
		{"publish port", quadlet("Container", "PublishPort", "8080:80"), "Container.PublishPort=8080:80 isn't allowed: use routes:"},
		{"route public by default", func(p *Package) { p.Routes["web"].Exposure = ExposurePublic }, "routes.web.exposure: must be private"},
		{"port public by default", func(p *Package) { p.Ports["ssh"].Exposure = ExposurePublic }, "ports.ssh.exposure: must be private"},

		// Devices.
		{"gpu through devices", func(p *Package) { p.Containers["web"].Devices = []Device{{GPU: "intel"}} }, ""},
		{"add device", quadlet("Container", "AddDevice", "/dev/sda"), "Container.AddDevice=/dev/sda isn't allowed: use devices:"},

		// Users and namespaces.
		{"user by uid", quadlet("Container", "User", "1000:1000"), ""},
		{"user 0", quadlet("Container", "User", "0"), "Container.User=0 isn't allowed"},
		{"user root with group", quadlet("Container", "User", "root:root"), "Container.User=root:root"},
		{"user int 0", quadlet("Container", "User", 0), "Container.User=0"},
		{"keep-id (Forgejo)", quadlet("Container", "UserNS", "keep-id:uid=1000,gid=1000"), ""},
		{"userns host", quadlet("Container", "UserNS", "host"), "Container.UserNS=host"},
		{"uid map", quadlet("Container", "UIDMap", "0:1:1000"), "Container.UIDMap="},
		{"gid map", quadlet("Container", "GIDMap", "0:1:1000"), "Container.GIDMap="},

		// What Roostack writes itself.
		{"image override", quadlet("Container", "Image", "example.org/other:1"), "Container.Image=example.org/other:1 isn't allowed: it would replace the pinned image"},
		{"secret", quadlet("Container", "Secret", "roostack-other-token,type=env,target=T"), "Container.Secret="},
		{"container name", quadlet("Container", "ContainerName", "other"), "Container.ContainerName="},
		{"own label", quadlet("Container", "Label", "org.example.team=photos"), ""},
		{"roostack label", quadlet("Container", "Label", "io.roostack.service=other"), "Container.Label=io.roostack.service=other"},

		// Commands on the server.
		{"shm size (Immich)", quadlet("Container", "ShmSize", "128m"), ""},
		{"service restart", quadlet("Service", "Restart", "always"), ""},
		{"exec start pre", quadlet("Service", "ExecStartPre", "/bin/rm -rf /srv"), "Service.ExecStartPre=/bin/rm -rf /srv isn't allowed: it runs a command on the server"},
		{"exec stop post", quadlet("Service", "ExecStopPost", "/bin/true"), "Service.ExecStopPost="},
		{"exec in unit", quadlet("Unit", "ExecStart", "/bin/true"), "Unit.ExecStart="},
		{"container exec", quadlet("Container", "Exec", "--verbose"), ""},
		{"key case", quadlet("Container", "publishport", "8080:80"), "Container.publishport="},

		// Found by the security review of 2026-10-16 (H2), each checked on
		// a dev VM with Podman 5.4's Quadlet (docs/ASSUMPTIONS.md A12).
		{"user 00", quadlet("Container", "User", "00"), "Container.User=00 isn't allowed"},
		{"user +0 with group", quadlet("Container", "User", "+0:1000"), "Container.User=+0:1000"},
		{"user quoted 0", quadlet("Container", "User", `"0"`), "Container.User="},
		{"user 1000", quadlet("Container", "User", "1000"), ""},
		{"user 10", quadlet("Container", "User", "10"), ""},
		{"group 0", quadlet("Container", "Group", "0"), "Container.Group=0"},
		{"userns ns path", quadlet("Container", "UserNS", "ns:/proc/1/ns/user"), "Container.UserNS=ns:/proc/1/ns/user"},
		{"userns container", quadlet("Container", "UserNS", "container:roostack-other-web"), "Container.UserNS="},
		{"userns auto", quadlet("Container", "UserNS", "auto"), ""},
		{"containers.conf module", quadlet("Container", "ContainersConfModule", "/srv/roostack/services/app/x.conf"), "Container.ContainersConfModule="},
		{"environment host", quadlet("Container", "EnvironmentHost", true), "Container.EnvironmentHost="},
		{"seccomp profile", quadlet("Container", "SeccompProfile", "unconfined"), "Container.SeccompProfile="},
		{"log path", quadlet("Container", "LogOpt", "path=/var/lib/roostack/lab.yaml"), "Container.LogOpt="},
		{"log size", quadlet("Container", "LogOpt", "max-size=10mb"), ""},
		{"health log folder", quadlet("Container", "HealthLogDestination", "/srv/roostack"), "Container.HealthLogDestination="},
		{"health log local", quadlet("Container", "HealthLogDestination", "local"), ""},
		{"annotation", quadlet("Container", "Annotation", "run.oci.keep_original_groups=1"), "Container.Annotation="},
		{"unmask", quadlet("Container", "Unmask", "ALL"), "Container.Unmask="},
		{"security label type", quadlet("Container", "SecurityLabelType", "spc_t"), "Container.SecurityLabelType="},
		{"keep groups", quadlet("Container", "GroupAdd", "keep-groups"), "Container.GroupAdd=keep-groups"},
		{"sub uid map", quadlet("Container", "SubUIDMap", "roostack"), "Container.SubUIDMap="},
		{"auto update", quadlet("Container", "AutoUpdate", "registry"), "Container.AutoUpdate=registry isn't allowed: it would replace the pinned image"},
		{"service name", quadlet("Container", "ServiceName", "roostack-caddy"), "Container.ServiceName=roostack-caddy"},
		{"two mounts on a line", quadlet("Container", "Mount", "type=tmpfs,destination=/t type=bind,source=/srv,destination=/srv"), "Container.Mount="},
		{"tmpfs with a source", quadlet("Container", "Mount", "type=tmpfs,source=/srv,destination=/t"), "Container.Mount="},
		{"two types", quadlet("Container", "Mount", "type=tmpfs,type=bind,destination=/t"), "Container.Mount="},
		{"two labels on a line", quadlet("Container", "Label", "a=b io.roostack.service=other"), "Container.Label="},
		{"quoted label", quadlet("Container", "Label", `"io.roostack.service=other"`), "Container.Label="},
		{"escaped label", quadlet("Container", "Label", `\x69o.roostack.service=other`), "Container.Label="},
		{"quoted own label", quadlet("Container", "Label", `"org.example.description=My app"`), ""},
		{"service environment", quadlet("Service", "Environment", "CONTAINERS_CONF_OVERRIDE=/srv/roostack/x.conf"), "Service.Environment="},
		{"service environment file", quadlet("Service", "EnvironmentFile", "/etc/roostack/env"), "Service.EnvironmentFile="},
		{"service output", quadlet("Service", "StandardOutput", "append:/var/lib/roostack/lab.yaml"), "Service.StandardOutput="},
		{"service input", quadlet("Service", "StandardInput", "file:/var/lib/roostack/lab.yaml"), "Service.StandardInput="},
		{"service user", quadlet("Service", "User", "root"), "Service.User="},
		{"service group", quadlet("Service", "Group", "roostack"), "Service.Group="},
		{"service working directory", quadlet("Service", "WorkingDirectory", "/var/lib/roostack"), "Service.WorkingDirectory="},
		{"service root directory", quadlet("Service", "RootDirectory", "/"), "Service.RootDirectory="},
		{"service state directory", quadlet("Service", "StateDirectory", "app"), "Service.StateDirectory="},
		{"service bind paths", quadlet("Service", "BindPaths", "/var/lib/roostack"), "Service.BindPaths="},
		{"service credential", quadlet("Service", "LoadCredential", "key:/var/lib/roostack/lab.yaml"), "Service.LoadCredential="},
		{"service slice", quadlet("Service", "Slice", "other.slice"), "Service.Slice="},
		{"service timeout", quadlet("Service", "TimeoutStopSec", "60"), ""},
		{"unit conflicts", quadlet("Unit", "Conflicts", "roostack-caddy.service"), "Unit.Conflicts=roostack-caddy.service isn't allowed: it ties the service"},
		{"unit requires", quadlet("Unit", "Requires", "roostack-other-web.service"), "Unit.Requires="},
		{"unit on failure", quadlet("Unit", "OnFailure", "roostack-caddy.service"), "Unit.OnFailure="},
		{"unit failure action", quadlet("Unit", "FailureAction", "exit"), "Unit.FailureAction="},
		{"unit start limit action", quadlet("Unit", "StartLimitAction", "exit"), "Unit.StartLimitAction="},
		{"unit description", quadlet("Unit", "Documentation", "https://example.org/app"), ""},
		{"install alias", quadlet("Install", "Alias", "roostack-caddy.service"), "Install.Alias="},
		{"install wanted by", quadlet("Install", "WantedBy", "multi-user.target"), "Install.WantedBy="},

		// Facts about the project, and what it needs (agreed 2026-10-14).
		{"no maker", func(p *Package) { p.About.Maker = "" }, "about.maker:"},
		{"no licence", func(p *Package) { p.About.Licence = "" }, "about.licence: give"},
		{"no source", func(p *Package) { p.About.Source = "" }, "about.source:"},
		{"closed with a website", func(p *Package) {
			p.About.Licence, p.About.Source, p.About.Website = LicenceClosed, "", "https://example.org"
		}, ""},
		{"closed without a website", func(p *Package) { p.About.Licence, p.About.Source = LicenceClosed, "" }, "about.website:"},
		{"deprecated licence", func(p *Package) { p.About.Licence = "AGPL-3.0" }, "about.licence: AGPL-3.0 doesn't say whether later versions apply; use AGPL-3.0-only or AGPL-3.0-or-later"},
		{"deprecated licence with +", func(p *Package) { p.About.Licence = "MIT OR GPL-2.0+" }, "about.licence: GPL-2.0+ doesn't say"},
		{"no icon", func(p *Package) { p.About.Icon = "" }, "about.icon:"},
		{"no category", func(p *Package) { p.About.Categories = nil }, "about.categories:"},
		{"optional facts left out", func(p *Package) { p.About.Website, p.About.Support, p.About.Keywords = "", "", nil }, ""},
		{"no requires", func(p *Package) { p.Requires.Roostack = "" }, "requires.roostack:"},
		{"container memory", func(p *Package) { p.Containers["web"].Memory = "" }, "containers.web.memory:"},

		// Public-access rules: login paths, or why it's unsafe.
		{"unsafe without login paths", func(p *Package) {
			p.Routes["web"].Public = PublicRules{Unsafe: "An API for the apps at home."}
		}, ""},
		{"no public rules", func(p *Package) { p.Routes["web"].Public = PublicRules{} }, "routes.web.public: name the pages where people sign in"},
		{"private paths only", func(p *Package) {
			p.Routes["web"].Public = PublicRules{PrivatePaths: []PrivatePath{{Path: "/admin", Reason: "Admin."}}}
		}, "routes.web.public:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse([]byte(policyBase))
			if err != nil {
				t.Fatal(err)
			}
			tt.change(p)
			if err := p.Validate(); err != nil {
				t.Fatalf("the case must be a valid package: %v", err)
			}
			problems := p.Policy()
			switch {
			case tt.want == "" && len(problems) > 0:
				t.Errorf("want no problems, got %q", problems)
			case tt.want != "" && (len(problems) != 1 || !strings.Contains(problems[0], tt.want)):
				t.Errorf("want one problem containing %q, got %q", tt.want, problems)
			}
		})
	}
}

// A database container gets the same Quadlet rules, and every problem is
// reported, not just the first.
func TestPolicyReportsEverything(t *testing.T) {
	p, err := Parse([]byte(policyBase))
	if err != nil {
		t.Fatal(err)
	}
	p.Containers["web"].Image = "example.org/app:1"
	p.Containers["web"].Quadlet = Quadlet{"Container": {"Volume": []any{"/data", "/etc:/etc", "/srv:/srv"}}}
	p.Databases["main"].Quadlet = Quadlet{"Container": {"PodmanArgs": "--privileged"}}
	problems := p.Policy()
	want := []string{
		"containers.web.image: pin",
		"containers.web.quadlet: Container.Volume=/etc:/etc",
		"containers.web.quadlet: Container.Volume=/srv:/srv",
		"databases.main.quadlet: Container.PodmanArgs=--privileged",
	}
	if len(problems) != len(want) {
		t.Fatalf("got %d problems, want %d: %q", len(problems), len(want), problems)
	}
	for i, w := range want {
		if !strings.HasPrefix(problems[i], w) {
			t.Errorf("problem %d = %q, want it to start with %q", i, problems[i], w)
		}
	}
}

// A package of the Owner's own may leave out the facts registry packages
// must give; Risks is only what could reach beyond the service.
func TestRisks(t *testing.T) {
	p, err := Parse([]byte(policyBase))
	if err != nil {
		t.Fatal(err)
	}
	p.About = About{}
	p.Requires.Roostack = ""
	p.Containers["web"].Memory = ""
	if len(p.Policy()) == 0 {
		t.Error("Policy should want the missing facts")
	}
	if risks := p.Risks(); len(risks) != 0 {
		t.Errorf("Risks = %q, want none", risks)
	}
	p.Containers["web"].Quadlet = Quadlet{"Container": {"PodmanArgs": "--privileged"}}
	if risks := p.Risks(); len(risks) != 1 || !strings.Contains(risks[0], "PodmanArgs") {
		t.Errorf("Risks = %q, want PodmanArgs", risks)
	}
}

// A key or value that isn't read back as written is refused by Validate
// (so Parse), and by the policy too, which doesn't rely on Validate
// having run (REVIEW-2026-10-16 H2).
func TestQuadletSyntax(t *testing.T) {
	tests := []struct {
		name, key string
		value     any
	}{
		{"space after the key", "Volume ", "/var/lib/roostack:/lab"},
		{"space before the key", " Volume", "/var/lib/roostack:/lab"},
		{"= in the key", "Volume=/:/host", "rw"},
		{"newline in the key", "ShmSize=1m\nVolume", "/:/host"},
		{"empty key", "", "x"},
		{"dash in the key", "X-Volume", "/:/host"},
		{"newline in the value", "ShmSize", "1m\nVolume=/:/host"},
		{"newline in a list", "Tmpfs", []any{"/run", "/tmp\nVolume=/:/host"}},
		{"backslash at the end", "ShmSize", `1m\`},
		{"backslash and spaces at the end", "ShmSize", "1m\\  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := Parse([]byte(policyBase))
			if err != nil {
				t.Fatal(err)
			}
			p.Containers["web"].Quadlet = Quadlet{"Container": {tt.key: tt.value}}
			if err := p.Validate(); err == nil {
				t.Error("Validate passed it")
			}
			if risks := p.Risks(); len(risks) != 1 || !strings.Contains(risks[0], "isn't allowed") {
				t.Errorf("Risks = %q, want one problem", risks)
			}
		})
	}
}
