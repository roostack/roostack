package editor

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/pkgspec"
)

// The corpus keeps the editor's checks (static/check.js) in step with
// pkgspec: packages made by changing the test packages one field at a
// time, each with what Parse, Validate, Policy and Risks say about it.
// test/corpus.test.js checks the editor says the same about each
// (SPEC.md §8.3.7, "The package editor"). TestCorpusFile fails when pkgspec
// says something else than the corpus records: run it with
// -update-editor, read the diff, then run the Node test, which fails
// until check.js is changed to match.

// edit changes one place in a package: Path's segments are map keys
// (strings) and list indexes (numbers); YAML is the new value, or with
// Delete the place is removed. A path that doesn't exist yet is made.
type edit struct {
	Path   []any  `json:"path"`
	YAML   string `json:"yaml,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}

// corpusCase is one package and what Go says about it. Decode means Go
// couldn't decode it at all (unknown fields, wrong types); otherwise
// Validate's messages, and when it's valid Policy's and Risks'.
type corpusCase struct {
	Base     string   `json:"base"`
	Edits    []edit   `json:"edits,omitempty"`
	Text     string   `json:"text,omitempty"`
	Decode   bool     `json:"decode,omitempty"`
	Validate []string `json:"validate,omitempty"`
	Policy   []string `json:"policy,omitempty"`
	Risks    []string `json:"risks,omitempty"`
}

// readBase reads one of the test packages (testPackages) as a base for
// the corpus.
func readBase(t *testing.T, name string) *yaml.Node {
	t.Helper()
	_, data := testPackage(t, name)
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	// Without comments: go-yaml writes a removed item's comments back
	// where they no longer parse.
	var strip func(n *yaml.Node)
	strip = func(n *yaml.Node) {
		n.HeadComment, n.LineComment, n.FootComment = "", "", ""
		for _, c := range n.Content {
			strip(c)
		}
	}
	strip(&doc)
	return &doc
}

// applyEdit changes doc (a document node) in place.
func applyEdit(t *testing.T, doc *yaml.Node, e edit) {
	t.Helper()
	var value *yaml.Node
	if !e.Delete {
		var v yaml.Node
		if err := yaml.Unmarshal([]byte(e.YAML), &v); err != nil {
			t.Fatalf("edit %v: %v", e.Path, err)
		}
		if len(v.Content) == 1 {
			value = v.Content[0]
		} else {
			value = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
		}
	}
	node := doc.Content[0]
	for i, seg := range e.Path {
		last := i == len(e.Path)-1
		switch key := seg.(type) {
		case string:
			if node.Kind != yaml.MappingNode {
				t.Fatalf("edit %v: %v isn't a map", e.Path, e.Path[:i])
			}
			found := -1
			for j := 0; j < len(node.Content); j += 2 {
				if node.Content[j].Value == key {
					found = j
				}
			}
			switch {
			case last && e.Delete:
				if found >= 0 {
					node.Content = slices.Delete(node.Content, found, found+2)
				}
				return
			case last:
				if found >= 0 {
					node.Content[found+1] = value
				} else {
					node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
				}
				return
			case found < 0 || node.Content[found+1].Kind != yaml.MappingNode && node.Content[found+1].Kind != yaml.SequenceNode:
				m := &yaml.Node{Kind: yaml.MappingNode}
				if _, list := e.Path[i+1].(int); list {
					m.Kind = yaml.SequenceNode
				}
				if found >= 0 {
					node.Content[found+1] = m
				} else {
					node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, m)
				}
				node = m
			default:
				node = node.Content[found+1]
			}
		case int:
			if node.Kind != yaml.SequenceNode {
				t.Fatalf("edit %v: %v isn't a list", e.Path, e.Path[:i])
			}
			switch {
			case last && e.Delete:
				if key < len(node.Content) {
					node.Content = slices.Delete(node.Content, key, key+1)
				}
				return
			case last && key >= len(node.Content):
				node.Content = append(node.Content, value)
				return
			case last:
				node.Content[key] = value
				return
			default:
				node = node.Content[key]
			}
		}
	}
}

// says is what pkgspec says about a package's text.
func says(data []byte) corpusCase {
	var c corpusCase
	p, err := pkgspec.Parse(data)
	var typeErr *yaml.TypeError
	switch {
	case errors.As(err, &typeErr):
		c.Decode = true
	case err != nil && (strings.HasPrefix(err.Error(), "yaml:") || errors.Is(err, io.EOF)):
		c.Decode = true
	case err != nil:
		c.Validate = strings.Split(err.Error(), "\n")
		slices.Sort(c.Validate)
	default:
		c.Policy, c.Risks = p.Policy(), p.Risks()
		slices.Sort(c.Policy)
		slices.Sort(c.Risks)
	}
	return c
}

// field finds the format's field at a path of a package.
func field(f *pkgspec.Field, path []any) *pkgspec.Field {
	for _, seg := range path {
		switch f.Type {
		case "object":
			var next *pkgspec.Field
			for _, c := range f.Fields {
				if c.Name == seg {
					next = c
				}
			}
			if next == nil {
				return nil
			}
			f = next
		case "list", "map":
			f = f.Item
		default:
			return nil
		}
	}
	return f
}

// leaves lists the paths of a package's values.
func leaves(n *yaml.Node, at []any, out *[][]any) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			leaves(n.Content[i+1], append(slices.Clone(at), n.Content[i].Value), out)
		}
		*out = append(*out, at)
	case yaml.SequenceNode:
		for i, c := range n.Content {
			leaves(c, append(slices.Clone(at), i), out)
		}
		*out = append(*out, at)
	default:
		*out = append(*out, at)
	}
}

// byType are the values each kind of field is tried with.
var byType = map[string][]string{
	"string": {`""`, `"Bad Value!"`, `[a]`, strings.Repeat("x", 70), `/abs/x`, `https://x.example/y`, `X-Real-IP`, `"300"`},
	"int":    {`0`, `70000`, `bogus`, `1.5`, `80`, `"80"`},
	"bool":   {`bogus`, `yes`, `false`, `true`, `1`},
	"any":    {`1`, `1.5`, `"s"`, `true`, `auto`, `null`},
	"list":   {`[]`, `bogus`, `{a: 1}`},
	"map":    {`{}`, `bogus`, `[a]`},
	"object": {`{}`, `bogus`, `[a]`},
}

// special are values tried at particular fields, beyond their type's.
// <c>, <r> and <s> are the package's first container, route and storage.
var special = map[string][]string{
	"about.licence": {`GPL-3.0`, `GPL-2.0+`, `MIT OR Apache-2.0`, `(MIT`, `MIT AND`, `Apache-2.0 WITH LLVM-exception`,
		`closed`, `LicenseRef-x`, `MIT OR GPL-3.0`, `MIT)`, `MIT WITH`, `AGPL-3.0-or-later`},
	"about.website": {`http://x.com`, `"https://"`, `https://user@x.com`, `https:///x`, `"https://x.com:abc"`, `"https://exa mple.com"`,
		`"https://x.com/a b"`, `HTTPS://x.com`, `"https://[::1]:80/"`, `"https://x.com?q=1#f"`, `"https://x%zz.com"`, `"https://x.com:"`},
	"about.releases":            {`"https://x.com/{version}"`, `"https://x.com/{ver}"`, `https://x.com`, `"x/{version}"`},
	"about.icon":                {`logo.svg`, `dir/icon.png`, `icon.png`, `.icon.png`},
	"about.categories":          {`[photos, photos]`, `[a]`, `[photos, media, documents, home]`, `[media]`},
	"about.keywords":            {`[" x"]`, `[a, A]`, `[a, b, c, d, e, f, g, h, i, j, k]`, `["` + strings.Repeat("x", 41) + `"]`, `[""]`},
	"about.maker":               {strings.Repeat("x", 101), strings.Repeat("é", 51), strings.Repeat("é", 50)},
	"requires.roostack":         {`"0.6"`, `"0.6.1"`, `"1"`, `v0.6`, `"0.06"`, `"10.0.0"`},
	"version":                   {`1.0`, `"1.0-roostack.1"`, `"-x"`, `"a b"`, `"1+2"`},
	"name":                      {`Bad`, strings.Repeat("a", 32), `db-x`, `a-`},
	"schema":                    {`roostack/v1alpha2`},
	"containers.*.memory":       {`300 MB`, `1.5 GB`, `0.5 MB`, `2000 GB`, `300MB`, `300 mb`, `1000 GB`, `1 MB`, `"1000.0001 GB"`},
	"containers.*.image":        {`x/y:1@sha256:` + strings.Repeat("a", 64), `x@sha256:abc`, `x:1@sha256:` + strings.Repeat("A", 64)},
	"containers.*.health":       {`{http: {port: 80, path: /}, cmd: x}`, `{http: {port: 0, path: x}}`, `{cmd: x}`, `{http: {port: 80, path: /h}}`},
	"containers.*.depends_on":   {`[databases.nope]`, `[containers.<c>]`, `[x]`, `[databases]`, `[containers.nope]`},
	"containers.*.mounts":       {`[{storage: x, libraries: true, path: /m}]`, `[{libraries: true, path: rel}]`, `[{storage: <s>, path: /d, readonly: true}]`, `[{path: /x}]`},
	"containers.*.devices":      {`[{gpu: nvidia}]`, `[{gpu: intel, when: "{{ .Settings.x }}"}]`, `[{}]`},
	"containers.*.secrets":      {`{A: databases.nope.password}`, `{A: nope}`, `{"bad key": x}`, `{A: databases}`},
	"containers.*.env":          {`{"bad key": x}`, `{"1A": x}`, `{A_1: "{{ .Lab.Domain }}"}`},
	"routes.*.subdomain":        {`Bad`, `-x`, strings.Repeat("a", 64), `ok-1`, `a.b`},
	"routes.*.exposure":         {`public`},
	"routes.*.client_ip_header": {`X-Real-IP`, `X-Forwarded-For`, `Real-IP`, `x-real-ip`, `X-`, `x-forwarded-host`},
	"routes.*.monitor":          {`{path: /api, statuses: [200, 99]}`, `{path: api}`, `{path: /}`, `{statuses: [600]}`, `{path: "/a/"}`},
	"routes.*.public": {`{}`, `{unsafe: x}`, `{login_paths: [/a, /b/, "/a*", "/api/v2/Auth::login", a]}`,
		`{private_paths: [{path: /a, reason: ""}, {path: /a, reason: x}], login_paths: [/l]}`, `{private_paths: [{path: "/a b", reason: " "}], unsafe: x}`},
	"routes.*.max_body_size": {`100MB`},
	"routes.*.container":     {`nope`},
}

// structural are edits made once per package: new entries, and lines of
// Quadlet.
func structural(first map[string]string) [][]edit {
	set := func(yamlText string, path ...any) []edit { return []edit{{Path: path, YAML: yamlText}} }
	var out [][]edit
	add := func(e []edit) { out = append(out, e) }
	c, r, s := first["containers"], first["routes"], first["storage"]

	// Containers and their names.
	for _, n := range []string{"network", "db-x", "Bad", "ok"} {
		add(set(`{image: x}`, "containers", n))
	}
	add(set(`null`, "containers", "empty"))
	add(set(`{}`, "containers", "noimage"))
	add(set(`{image: x, foo: 1}`, "containers", "unknown"))
	add(set(`{}`, "containers"))

	// Storage and databases.
	for _, v := range []string{`{class: hot}`, `{class: critical, location: disk}`, `{title: "` + strings.Repeat("x", 61) + `", class: critical}`,
		`{title: "a\nb", class: critical}`, `null`, `{class: replaceable, location: media}`, `{title: "` + strings.Repeat("é", 60) + `", class: critical}`} {
		add(set(v, "storage", "x"))
	}
	add(set(`{class: critical}`, "storage", "Bad"))
	for _, v := range []string{`{type: sqlite, path: nope/db}`, `{type: sqlite, path: /abs}`, `{type: sqlite, path: ` + s + `/../x}`,
		`{type: sqlite, path: ` + s + `/db, image: i}`, `{type: sqlite, path: ` + s + `}`, `{type: sqlite, path: ` + s + `/}`,
		`{type: postgres, path: p}`, `{type: nosql}`, `{type: redis, memory: 10 KB}`, `{type: postgres, image: "pg:17"}`,
		`{type: mariadb, backup: false}`, `null`, `{type: sqlite, path: ` + s + `/db, memory: 1 GB}`,
		`{type: postgres, quadlet: {Container: {ShmSize: 128m}}}`} {
		add(set(v, "databases", "x"))
	}
	add(set(`{type: postgres}`, "databases", "Bad"))
	add(append(set(`{type: mysql}`, "databases", "m"), set(`databases.m.url`, "containers", c, "secrets", "DB_URL")...))
	add(append(set(`{type: postgres}`, "databases", "p"), set(`databases.p.url`, "containers", c, "secrets", "DB_URL")...))
	add(append(set(`{type: redis}`, "databases", "q"), set(`databases.q.password`, "containers", c, "secrets", "DB_PW")...))
	add(append(set(`{type: sqlite, path: `+s+`/x.db}`, "databases", "l"), set(`[databases.l]`, "containers", c, "depends_on")...))

	// Settings.
	for _, v := range []string{`{key: bad-key, type: string}`, `{key: a, type: bool, default: "x"}`, `{key: a, type: bool, default: auto}`,
		`{key: b, type: int, default: 1.5}`, `{key: b, type: int, default: 3}`, `{key: c, type: secret, default: x}`, `{key: d, type: weird}`,
		`{key: e, type: string, generate: true}`, `{key: f, type: secret, generate: true, install_only: true}`,
		`{key: g, type: bool, required: true}`, `{key: h, type: bool, min_length: 3}`, `{key: i, type: string, reveal: true}`,
		`{key: j, type: bool, install_value: true}`, `{key: j, type: string, install_value: 3}`, `{key: k, type: secret, install_value: x}`,
		`{key: l, type: string, default: 5}`, `{key: m, type: string, install_only: true, install_value: x}`, `null`, `{}`,
		`{key: n, type: secret, reveal: true, install_only: true}`, `{key: o, type: secret, min_length: 8}`} {
		add(set(v, "settings", 99))
	}
	add(set(`{key: s1, type: secret}`, "settings", 99))
	add(append(set(`{key: s1, type: secret, install_only: true}`, "settings", 99), set(`s1`, "containers", c, "secrets", "S1")...))
	add(append(set(`{key: s1, type: string}`, "settings", 99), set(`s1`, "containers", c, "secrets", "S1")...))
	add([]edit{{Path: []any{"settings", 99}, YAML: `{key: s1, type: secret}`}, {Path: []any{"containers", c, "env", "S1"}, YAML: `s1`},
		{Path: []any{"containers", c, "secrets", "S1"}, YAML: `s1`}})

	// A key the app makes (from_hook), and the hooks that save it.
	for _, v := range []string{`{key: k1, type: string, from_hook: true}`, `{key: k1, type: secret, from_hook: true}`,
		`{key: k1, type: secret, from_hook: true, generate: true}`, `{key: k1, type: secret, from_hook: true, reveal: true}`,
		`{key: k1, type: secret, from_hook: true, min_length: 8}`} {
		add(set(v, "settings", 99))
	}
	saver := func(on string) string {
		return `{on: ` + on + `, name: connect, title: Connect, container: ` + c + `, command: [x], saves: k1}`
	}
	add(append(set(`{key: k1, type: secret, from_hook: true}`, "settings", 99), set(saver("action"), "hooks", 99)...))
	add(append(append(set(`{key: k1, type: secret, from_hook: true}`, "settings", 99), set(saver("action"), "hooks", 98)...),
		set(`k1`, "containers", c, "secrets", "K1")...))
	add(append(append(set(`{key: k1, type: secret, from_hook: true}`, "settings", 99), set(saver("action"), "hooks", 98)...),
		set(`{on: install, container: `+c+`, command: [x], saves: k1, secrets: {K: k1}}`, "hooks", 99)...))
	add(append(set(`{key: k1, type: secret}`, "settings", 99), set(saver("action"), "hooks", 99)...))
	add(set(`{on: install, container: `+c+`, command: [x], saves: nope}`, "hooks", 99))

	// sso hooks, and the client secret in hooks.
	for _, v := range []string{`{on: sso, container: ` + c + `, command: [x]}`, `{on: sso, name: a, container: ` + c + `, command: [x]}`,
		`{on: sso, container: ` + c + `, command: [x], inputs: [{key: a, type: string}]}`,
		`{on: sso, container: ` + c + `, command: [x], secrets: {S: sso.client_secret}, env: {A: "{{ .SSO.ClientID }}"}}`,
		`{on: install, container: ` + c + `, command: [x], secrets: {S: sso.client_secret}}`,
		`{on: sso, container: ` + c + `, command: [x], saves: x}`} {
		add(set(v, "hooks", 99))
	}
	add(append(set(`{forward_auth: {route: `+r+`}}`, "sso"), set(`{on: sso, container: `+c+`, command: [x], secrets: {S: sso.client_secret}}`, "hooks", 99)...))

	// Hooks.
	for _, v := range []string{
		`{on: action, name: Bad, container: nope}`, `{on: x, command: [a]}`,
		`{on: install, container: ` + c + `, command: [x], inputs: [{key: a, type: string}]}`,
		`{on: action, name: a, title: A, container: ` + c + `, command: [x], inputs: [{key: p, type: secret}, {key: p, type: bool, default: 1}, {key: q, type: string, generate: true}], secrets: {A: inputs.p, B: inputs.nope, C: nope, "bad key": inputs.p}, env: {"bad key": x, A: y}}`,
		`{on: action, name: a, title: A, container: ` + c + `, command: [x], inputs: [{key: s, type: string}], secrets: {A: inputs.s}}`,
		`null`, `{}`, `{on: action, name: a, container: ` + c + `, command: [x]}`,
	} {
		add(set(v, "hooks", 99))
	}
	add(append(set(`{on: action, name: a, title: A, container: `+c+`, command: [x]}`, "hooks", 98), set(`{on: action, name: a, title: B, container: `+c+`, command: [y]}`, "hooks", 99)...))

	// Scheduled hooks (SPEC.md §8.1).
	for _, v := range []string{
		`{on: schedule, name: gc, title: Free up space, container: ` + c + `, every: week, timeout: 2h, command: [x]}`,
		`{on: schedule, name: gc, title: Free up space, container: ` + c + `, every: day, command: [x], env: {A: "{{ .Settings.x }}"}}`,
		`{on: schedule, name: Bad, container: ` + c + `, every: hourly, command: [x]}`,
		`{on: schedule, name: gc, title: T, container: ` + c + `, every: month, timeout: 7h, command: [x]}`,
		`{on: schedule, name: gc, title: T, container: ` + c + `, every: month, timeout: 30s, command: [x]}`,
		`{on: schedule, name: gc, title: T, container: ` + c + `, every: month, timeout: 1h30m, command: [x]}`,
		`{on: schedule, name: gc, title: T, container: ` + c + `, every: day, timeout: soon, command: [x]}`,
		`{on: schedule, name: gc, title: T, container: ` + c + `, every: day, command: [x], inputs: [{key: a, type: string}]}`,
		`{on: schedule, name: gc, title: T, container: ` + c + `, every: day, command: [x], saves: x}`,
		`{on: action, name: a, title: A, container: ` + c + `, command: [x], every: day, timeout: 1h}`,
	} {
		add(set(v, "hooks", 99))
	}
	add(append(set(`{on: schedule, name: gc, title: A, container: `+c+`, every: day, command: [x]}`, "hooks", 98), set(`{on: schedule, name: gc, title: B, container: `+c+`, every: week, command: [y]}`, "hooks", 99)...))

	// Links and ports.
	for _, v := range []string{`{title: "", route: nope, path: "//x"}`, `{title: T, route: ` + r + `, path: admin}`, `{title: T, route: ` + r + `, path: /ok}`} {
		add(set(v, "links", 99))
	}
	for _, v := range []string{`{title: T, container: ` + c + `, port: 22, host_port: 80}`, `{title: T, container: ` + c + `, port: 22, host_port: 2222, protocol: sctp}`,
		`{title: T, container: ` + c + `, port: 22, host_port: 2222, exposure: public}`, `{title: T, container: ` + c + `, port: 22, host_port: 2222, protocol: udp, enabled: true}`,
		`null`, `{}`, `{title: T, container: ` + c + `, port: 0, host_port: 70000, exposure: x}`} {
		add(set(v, "ports", "x"))
	}

	// Single sign-on (sso.go): each of validateSSO's checks, and values the
	// decoder refuses.
	for _, v := range []string{`{}`, `null`, `bogus`, `{oidc: {route: ` + r + `, redirect_paths: [/l]}, forward_auth: {route: ` + r + `}}`,
		`{oidc: {route: nope}}`, `{oidc: {route: ` + r + `, redirect_paths: [login, "/a//b", /ok/, "/a b", /a/b.c:~d]}}`,
		`{oidc: {route: ` + r + `, redirect_uris: ["https://x.com/cb", "app.x:/cb", "http://x", "Bad:/x", "app.immich:///oauth-callback", "x:/a b"]}}`,
		`{forward_auth: {route: ` + r + `, bypass_paths: [{path: /api, reason: x}, {path: /api, reason: " "}, {path: "/a*", reason: y}, {path: /b/, reason: z}]}}`,
		`{forward_auth: {route: nope}}`, `{forward_auth: {route: ` + r + `}}`,
		`{oidc: {route: ` + r + `, redirect_paths: [/l]}, env: {nope: {A: x}, ` + c + `: {"bad key": y, OK: "{{ .SSO.ClientID }}"}}}`,
		`{oidc: {route: ` + r + `, redirect_paths: [/l]}, secrets: {` + c + `: {S: sso.client_secret, T: other, "bad key": sso.client_secret}, nope: {S: sso.client_secret}}}`,
		`{forward_auth: {route: ` + r + `}, secrets: {` + c + `: {S: sso.client_secret}}}`,
		`{oidc: {route: ` + r + `, redirect_paths: [/l]}, note: "The phone apps still use the app's own accounts."}`,
		`{oidc: {route: ` + r + `, foo: 1}}`, `{oidc: bogus}`, `{env: {` + c + `: bogus}}`, `{forward_auth: {bypass_paths: bogus}}`, `{forward_auth: {}}`,
		// Templated redirect paths, escapes, and renamed groups.
		`{oidc: {route: ` + r + `, redirect_paths: ["/u/{{ pathescape .SSO.ProviderName }}/cb", "x/{{ .SSO.ClientID }}", "/a%20b", "/a%2", "/a%zz"]}}`,
		`{oidc: {route: ` + r + `, redirect_paths: [/l], groups: {users: user, admins: admin}}}`,
		`{oidc: {route: ` + r + `, redirect_paths: [/l], groups: {users: User, admins: admin}}}`,
		`{oidc: {route: ` + r + `, redirect_paths: [/l], groups: {users: same, admins: same}}}`,
		`{oidc: {route: ` + r + `, redirect_paths: [/l], groups: {users: user}}}`, `{oidc: {route: ` + r + `, redirect_paths: [/l], groups: bogus}}`,
		// A secret inside a value.
		`{oidc: {route: ` + r + `, redirect_paths: [/l]}, env: {` + c + `: {A: x}}, secrets: {` + c + `: {D: sso.client_secret}}, secret_env: {` + c +
			`: {A: "{{ .SSO.ClientSecret }}", B: plain, D: "{{ json .SSO.ClientSecret }}", E: '{"s": {{ json .SSO.ClientSecret }}}', "bad key": "{{ .SSO.ClientSecret }}"}, nope: {C: "{{ .SSO.ClientSecret }}"}}}`,
		`{forward_auth: {route: ` + r + `}, secret_env: {` + c + `: {A: "{{ .SSO.ClientSecret }}"}}}`, `{secret_env: {` + c + `: bogus}}`,
	} {
		add(set(v, "sso"))
	}

	// Upgrade notes.
	for _, v := range []string{`[{version: "", note: x}]`, `[{version: 1.0, note: ""}]`, `[{version: "1.0", note: a}, {version: "1.0", note: b}]`, `[{version: "1.0", note: " "}]`} {
		add(set(v, "upgrade_notes"))
	}

	// Quadlet lines: every allowed key, and the refused ones with values
	// the rules treat differently.
	q := func(section, key, value string) { add(set(value, "containers", c, "quadlet", section, key)) }
	d := pkgspec.Describe()
	for _, section := range slices.Sorted(maps.Keys(d.Quadlet.Allowed)) {
		for _, key := range d.Quadlet.Allowed[section] {
			q(section, key, `x`)
		}
	}
	for _, kv := range [][3]string{
		{"Container", "Volume", `/data`}, {"Container", "Volume", `/srv:/data`}, {"Container", "Volume", `vol:/data`}, {"Container", "Volume", `":/data"`},
		{"Container", "Mount", `type=tmpfs,destination=/x`}, {"Container", "Mount", `type=bind,source=/x,destination=/y`},
		{"Container", "Mount", `type=tmpfs,src=/x`}, {"Container", "Mount", `"type=tmpfs,destination=/x type=bind"`}, {"Container", "Mount", `type=tmpfs,type=tmpfs`},
		{"Container", "User", `root`}, {"Container", "User", `"0"`}, {"Container", "User", `"1000:1000"`}, {"Container", "User", `"+0"`},
		{"Container", "User", `"00:1"`}, {"Container", "Group", `ROOT`}, {"Container", "User", `"'0'"`},
		{"Container", "UserNS", `keep-id`}, {"Container", "UserNS", `host`}, {"Container", "UserNS", `"keep-id:uid=1"`}, {"Container", "UserNS", `nomap`},
		{"Container", "Label", `io.roostack.x=1`}, {"Container", "Label", `"a=1 io.roostack.y=2"`}, {"Container", "Label", `a=1`},
		{"Container", "Label", `"a=1 'IO.ROOSTACK.z=3'"`}, {"Container", "Label", `"a=\\x69"`},
		{"Container", "LogOpt", `path=/x`}, {"Container", "LogOpt", `max-size=1m`}, {"Container", "LogOpt", `"tag=\\x"`},
		{"Container", "HealthLogDestination", `local`}, {"Container", "HealthLogDestination", `/tmp`},
		{"Container", "GroupAdd", `keep-groups`}, {"Container", "GroupAdd", `video`},
		{"Container", "PodmanArgs", `x`}, {"Container", "Network", `host`}, {"Container", "volume", `/x`}, {"Container", "Bad Key", `x`},
		{"Container", "SecurityLabelDisable", `true`}, {"Container", "Image", `x`}, {"Container", "Secret", `x`}, {"Container", "AddDevice", `/dev/x`},
		{"Container", "Unknown", `x`}, {"Container", "PublishPort", `"80:80"`},
		{"Service", "ExecStartPre", `x`}, {"Service", "Restart", `always`}, {"Service", "Environment", `A=1`}, {"Service", "restart", `x`},
		{"Service", "WorkingDirectory", `/x`}, {"Service", "LoadCredential", `x`}, {"Service", "Type", `oneshot`}, {"Service", "DelegateSubgroup", `x`},
		{"Unit", "After", `x`}, {"Unit", "Documentation", `x`}, {"Unit", "SuccessAction", `reboot`}, {"Unit", "ExecCondition", `x`},
		{"Install", "WantedBy", `x`}, {"Bogus", "X", `y`},
		{"Container", "Environment", `[A=1, B=2]`}, {"Container", "Environment", `[[a]]`}, {"Container", "Environment", `"x\\"`},
		{"Container", "Environment", `"x\\ "`}, {"Container", "Environment", `"a\nb"`}, {"Container", "PidsLimit", `5`},
		{"Container", "ReadOnly", `true`}, {"Container", "StopTimeout", `1.5`}, {"Container", "PidsLimit", `1e7`},
		{"Container", "PidsLimit", `10000000.0`}, {"Container", "Tmpfs", `null`}, {"Container", "Tmpfs", `{a: 1}`},
		{"Container", "Volume", `[/a, "/b:/c"]`}, {"Container", "PidsLimit", `0x10`}, {"Container", "PidsLimit", `0.00001`},
	} {
		q(kv[0], kv[1], kv[2])
	}
	add(set(`null`, "containers", c, "quadlet", "Container"))
	add(set(`x`, "containers", c, "quadlet", "Container"))
	return out
}

// hand are whole packages, for what edits can't make.
var hand = []string{
	// Anchors, aliases and merge keys.
	`schema: roostack/v1alpha1
name: anchors
title: Anchors
version: "1"
x-common: &common
  image: x/y:1
  memory: 50 MB
containers:
  app:
    <<: *common
    env: {A: "1"}
  worker:
    <<: *common
    image: other
`,
	// An empty document, and one that isn't a map.
	``,
	`just text`,
	`[a, b]`,
	// A duplicate key.
	"name: a\nname: b\n",
	// YAML 1.1 booleans into bools.
	`schema: roostack/v1alpha1
name: bools
title: Bools
version: "1"
containers:
  app: {image: x}
settings:
  - {key: a, type: bool, default: yes, generate: on, required: Off}
`,
	// Numbers written in other bases.
	`schema: roostack/v1alpha1
name: numbers
title: Numbers
version: "1"
containers:
  app: {image: x}
routes:
  web: {container: app, port: 0x50, subdomain: web}
  web2: {container: app, port: 0o120, subdomain: web2}
  web3: {container: app, port: 1_000, subdomain: web3}
`,
}

func corpus(t *testing.T) []corpusCase {
	var cases []corpusCase
	for _, base := range slices.Sorted(slices.Values(testPackages)) {
		doc := readBase(t, base)
		first := map[string]string{}
		root := doc.Content[0]
		for i := 0; i < len(root.Content); i += 2 {
			if v := root.Content[i+1]; v.Kind == yaml.MappingNode && len(v.Content) > 0 {
				first[root.Content[i].Value] = v.Content[0].Value
			}
		}
		try := func(edits []edit) {
			d := readBase(t, base)
			for _, e := range edits {
				applyEdit(t, d, e)
			}
			data, err := yaml.Marshal(d)
			if err != nil {
				t.Fatalf("%s %v: %v", base, edits, err)
			}
			c := says(data)
			c.Base, c.Edits = base, edits
			cases = append(cases, c)
		}

		var paths [][]any
		leaves(root, nil, &paths)
		D := pkgspec.Describe()
		for _, p := range paths {
			if len(p) == 0 {
				continue
			}
			f := field(D.Fields, p)
			if f == nil {
				continue
			}
			try([]edit{{Path: p, Delete: true}})
			for _, v := range byType[f.Type] {
				try([]edit{{Path: p, YAML: v}})
			}
			if f.Type == "object" {
				try([]edit{{Path: append(slices.Clone(p), "foo"), YAML: "1"}})
			}
			if f.Type == "list" {
				try([]edit{{Path: append(slices.Clone(p), 99), YAML: `"Bad Value!"`}})
				try([]edit{{Path: append(slices.Clone(p), 99), YAML: `null`}})
			}
			if f.Type == "map" && f.Item.Type == "string" {
				try([]edit{{Path: append(slices.Clone(p), "bad key"), YAML: "x"}})
			}
			pattern := make([]string, len(p))
			for i, seg := range p {
				pattern[i] = fmt.Sprint(seg)
				if i == 1 && slices.Contains([]string{"containers", "storage", "databases", "routes", "ports"}, pattern[0]) {
					pattern[i] = "*"
				}
			}
			for _, v := range special[strings.Join(pattern, ".")] {
				v = strings.NewReplacer("<c>", first["containers"], "<r>", first["routes"], "<s>", first["storage"]).Replace(v)
				try([]edit{{Path: p, YAML: v}})
			}
		}
		for _, edits := range structural(first) {
			try(edits)
		}
	}
	for _, text := range hand {
		c := says([]byte(text))
		c.Base, c.Text = "", text
		cases = append(cases, c)
	}
	return cases
}

// The corpus is what pkgspec says now.
func TestCorpusFile(t *testing.T) {
	cases := corpus(t)
	var b strings.Builder
	b.WriteString("[\n")
	for i, c := range cases {
		line, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		if i < len(cases)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	checkFile(t, filepath.Join("testdata", "corpus.json"), []byte(b.String()))
}
