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
containers:
  web:
    image: example.org/app:1@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
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
