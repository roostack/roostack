package pkgspec

import (
	"path/filepath"
	"strings"
	"testing"
)

// The reference examples must always parse, so they stay in sync with the code.
func TestExamplesParse(t *testing.T) {
	for _, name := range []string{"vaultwarden", "immich"} {
		t.Run(name, func(t *testing.T) {
			p, err := Load(filepath.Join("..", "..", "docs", "examples", name+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if p.Name != name {
				t.Errorf("name = %q, want %q", p.Name, name)
			}
			// The examples use placeholder digests, which count as pinned.
			if w := p.Warnings(); len(w) > 0 {
				t.Errorf("unexpected warnings: %v", w)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	p, err := Parse([]byte(`
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: example/app:1 }
storage:
  data: { class: critical }
routes:
  web: { container: web, port: 80, subdomain: app }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Storage["data"].Location; got != LocationService {
		t.Errorf("storage location = %q, want %q", got, LocationService)
	}
	if got := p.Routes["web"].Exposure; got != ExposurePrivate {
		t.Errorf("route exposure = %q, want %q", got, ExposurePrivate)
	}
	if w := p.Warnings(); len(w) != 1 {
		t.Errorf("want 1 unpinned-image warning, got %v", w)
	}
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"unknown field", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, imagee: y }`, "field imagee not found"},
		{"unknown storage in mount", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, mounts: [{ storage: nope, path: /data }] }`, `unknown storage "nope"`},
		{"bad secret ref", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, secrets: { TOKEN: missing } }`, `"missing" is not a secret setting`},
		{"secret with default", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
settings:
  - { key: token, type: secret, default: abc }
containers:
  web: { image: x }`, "secrets cannot have a default"},
		{"bad depends_on", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, depends_on: [databases.main] }`, `unknown reference "databases.main"`},
		{"sqlite path storage", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
databases:
  main: { type: sqlite, path: data/db.sqlite }`, `unknown storage "data"`},
		{"route to unknown container", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
routes:
  web: { container: api, port: 80, subdomain: app }`, `unknown container "api"`},
		{"port below 1024", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
ports:
  ssh: { title: SSH, container: web, port: 22, host_port: 22 }`, "host_port: must be 1024-65535"},
		{"port protocol", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
ports:
  ssh: { title: DNS, container: web, port: 53, host_port: 5353, protocol: sctp }`, "protocol: must be tcp or udp"},
		{"port without title", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
ports:
  ssh: { container: web, port: 22, host_port: 2222 }`, "title: required"},
		{"quadlet section", `
schema: mylab/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, quadlet: { Bogus: { A: b } } }`, `section "Bogus" not allowed`},
		{"wrong schema", `
schema: mylab/v2
name: app
title: App
version: "1"
containers:
  web: { image: x }`, "schema: must be"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
