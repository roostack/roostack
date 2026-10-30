package pkgspec

import (
	"strings"
	"testing"
)

// Config files (SPEC.md §8.1): in a storage a container mounts, at a plain
// path inside it, in a known format, a block only where a later line wins.
func TestConfigFileValidation(t *testing.T) {
	base := `
schema: roostack/v1alpha1
name: app
title: App
version: "1"
storage:
  data: { class: critical }
  other: { class: critical }
containers:
  web: { image: x, mounts: [{ storage: data, path: /data }] }
config_files:
`
	for _, tc := range []struct{ name, files, want string }{
		{"fine", `
  a: { title: A, storage: data, path: conf/a.py, format: python, block: true, content: "A = 1" }
  b: { title: B, storage: data, path: b.yaml, format: yaml, content: "b: 1" }`, ""},
		{"unknown storage", `
  a: { title: A, storage: nope, path: a, format: text, content: x }`, `config_files.a.storage: unknown storage "nope"`},
		{"storage nobody mounts", `
  a: { title: A, storage: other, path: a, format: text, content: x }`, `no container mounts "other"`},
		{"out of the storage", `
  a: { title: A, storage: data, path: ../a, format: text, content: x }`, `config_files.a.path: "../a" must be a path inside the storage folder`},
		{"absolute", `
  a: { title: A, storage: data, path: /etc/a, format: text, content: x }`, `config_files.a.path: "/etc/a"`},
		{"dot folder", `
  a: { title: A, storage: data, path: a/./b, format: text, content: x }`, `config_files.a.path: "a/./b"`},
		{"two at one path", `
  a: { title: A, storage: data, path: a, format: text, content: x }
  b: { title: B, storage: data, path: a, format: text, content: x }`, "config_files.b.path: config_files.a is there already"},
		{"unknown format", `
  a: { title: A, storage: data, path: a, format: toml, content: x }`, `config_files.a.format: must be one of python, yaml, json, text; got "toml"`},
		{"a YAML block", `
  a: { title: A, storage: data, path: a, format: yaml, block: true, content: x }`, "config_files.a.block: only for python"},
		{"no title or content", `
  a: { storage: data, path: a, format: text }`, "config_files.a.title: required"},
		{"no content", `
  a: { title: A, storage: data, path: a, format: text, content: " " }`, "config_files.a.content: required"},
		{"bad name", `
  A: { title: A, storage: data, path: a, format: text, content: x }`, "config_files.A: invalid name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(base + tc.files))
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("error = %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
