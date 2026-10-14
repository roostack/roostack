package pkgspec

import (
	"path/filepath"
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/pkgtest"
)

// The package fixtures, copies of registry packages, must always parse, so
// they stay in sync with the code, and follow the package policy, as
// registry packages do.
func TestFixturesParse(t *testing.T) {
	for _, name := range pkgtest.Names {
		t.Run(name, func(t *testing.T) {
			p, err := Load(pkgtest.File(name))
			if err != nil {
				t.Fatal(err)
			}
			if p.Name != name {
				t.Errorf("name = %q, want %q", p.Name, name)
			}
			if problems := p.Policy(); len(problems) > 0 {
				t.Errorf("breaks the package policy: %v", problems)
			}
			if err := p.CheckIcon(filepath.Dir(pkgtest.File(name))); err != nil {
				t.Error(err)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	p, err := Parse([]byte(`
schema: roostack/v1alpha1
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
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name, yaml, want string
	}{
		{"unknown field", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, imagee: y }`, "field imagee not found"},
		{"unknown storage in mount", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, mounts: [{ storage: nope, path: /data }] }`, `unknown storage "nope"`},
		{"bad secret ref", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, secrets: { TOKEN: missing } }`, `"missing" is not a secret setting`},
		{"secret with default", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
settings:
  - { key: token, type: secret, default: abc }
containers:
  web: { image: x }`, "secrets cannot have a default"},
		{"bad depends_on", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, depends_on: [databases.main] }`, `unknown reference "databases.main"`},
		{"sqlite path storage", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
databases:
  main: { type: sqlite, path: data/db.sqlite }`, `unknown storage "data"`},
		{"route to unknown container", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
routes:
  web: { container: api, port: 80, subdomain: app }`, `unknown container "api"`},
		{"port below 1024", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
ports:
  ssh: { title: SSH, container: web, port: 22, host_port: 22 }`, "host_port: must be 1024-65535"},
		{"port protocol", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
ports:
  ssh: { title: DNS, container: web, port: 53, host_port: 5353, protocol: sctp }`, "protocol: must be tcp or udp"},
		{"port without title", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
ports:
  ssh: { container: web, port: 22, host_port: 2222 }`, "title: required"},
		{"quadlet section", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, quadlet: { Bogus: { A: b } } }`, `section "Bogus" not allowed`},
		{"memory unit", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x, memory: 300M }`, `containers.web.memory: "300M" must be a number and MB or GB`},
		{"sqlite memory", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
storage:
  data: { class: critical }
containers:
  web: { image: x }
databases:
  main: { type: sqlite, path: data/db.sqlite, memory: 10 MB }`, "image, memory and quadlet are not allowed"},
		{"database memory", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
containers:
  web: { image: x }
databases:
  main: { type: postgres, memory: lots }`, `databases.main.memory: "lots"`},
		{"http address", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about: { website: "http://example.org" }
containers:
  web: { image: x }`, `about.website: "http://example.org" must be an https:// address`},
		{"licence expression", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about: { licence: "MIT OR" }
containers:
  web: { image: x }`, `about.licence: "MIT OR" isn't an SPDX licence expression`},
		{"closed with a source", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about: { licence: closed, source: "https://example.org/src" }
containers:
  web: { image: x }`, "about.source: a closed-source app has no published source"},
		{"icon in another folder", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about: { icon: ../other/icon.png }
containers:
  web: { image: x }`, "must be the name of a .png file beside the package file"},
		{"icon not a png", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about: { icon: icon.svg }
containers:
  web: { image: x }`, "about.icon:"},
		{"unknown category", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about: { categories: [photos, games] }
containers:
  web: { image: x }`, `about.categories[1]: "games" must be one of photos, media`},
		{"too many categories", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about: { categories: [photos, media, home, network] }
containers:
  web: { image: x }`, "about.categories: at most 3"},
		{"category twice", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about: { categories: [photos, photos] }
containers:
  web: { image: x }`, `about.categories[1]: "photos" is listed twice`},
		{"keyword twice", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
about: { keywords: [git, Git] }
containers:
  web: { image: x }`, `about.keywords[1]: "Git" is listed twice`},
		{"requires a release", `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
requires: { roostack: "v0.6" }
containers:
  web: { image: x }`, `requires.roostack: "v0.6" must be a Roostack release`},
		{"upgrade note without text", `
schema: roostack/v1alpha1
name: app
title: App
version: "2"
upgrade_notes:
  - { version: "2" }
containers:
  web: { image: x }`, "upgrade_notes[0].note: required"},
		{"two notes for a version", `
schema: roostack/v1alpha1
name: app
title: App
version: "2"
upgrade_notes:
  - { version: "2", note: A }
  - { version: "2", note: B }
containers:
  web: { image: x }`, `upgrade_notes[1].version: "2" has two notes`},
		{"wrong schema", `
schema: roostack/v2
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
