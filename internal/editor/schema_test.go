package editor

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/pkgspec"
)

// update rewrites the editor's generated files: static/schema.js,
// static/examples.js and testdata/corpus.json.
var update = flag.Bool("update-editor", false, "rewrite the package editor's generated files")

// helpEntry is one field's help (help.yaml).
type helpEntry struct {
	Label   string `yaml:"label" json:"label,omitempty"`
	Help    string `yaml:"help" json:"help,omitempty"`
	Example string `yaml:"example" json:"example,omitempty"`
	Link    string `yaml:"link" json:"link,omitempty"`
	Same    string `yaml:"same" json:"-"`
}

// fieldPaths lists every field of the format as help.yaml names them,
// leaves (true) and the sections around them (false).
func fieldPaths(f *pkgspec.Field, at string, out map[string]bool) {
	join := func(name string) string {
		if at == "" {
			return name
		}
		return at + "." + name
	}
	switch f.Type {
	case "object":
		for _, c := range f.Fields {
			p := join(c.Name)
			switch {
			case c.Type == "object":
				out[p] = false
				fieldPaths(c, p, out)
			case (c.Type == "list" || c.Type == "map") && c.Item.Type == "object":
				out[p] = false
				suffix := "[]"
				if c.Type == "map" {
					suffix = ".*"
				}
				out[p+suffix] = false
				fieldPaths(c.Item, p+suffix, out)
			default:
				out[p] = true
			}
		}
	}
}

// loadHelp reads help.yaml, resolves "same", and checks it covers every
// field and names no field that doesn't exist.
func loadHelp(t *testing.T) map[string]helpEntry {
	t.Helper()
	data, err := os.ReadFile("help.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var help map[string]helpEntry
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&help); err != nil {
		t.Fatalf("help.yaml: %v", err)
	}
	paths := map[string]bool{}
	fieldPaths(pkgspec.Describe().Fields, "", paths)
	for _, p := range slices.Sorted(maps.Keys(help)) {
		e := help[p]
		if _, ok := paths[p]; !ok {
			t.Errorf("help.yaml: %s isn't a field of the package format", p)
		}
		if e.Same != "" {
			same, ok := help[e.Same]
			if !ok || same.Same != "" {
				t.Errorf("help.yaml: %s: same: %s isn't an entry of its own", p, e.Same)
			}
			help[p] = same
		}
	}
	for _, p := range slices.Sorted(maps.Keys(paths)) {
		e, ok := help[p]
		if paths[p] && (!ok || e.Label == "" || e.Help == "" || e.Link == "") {
			t.Errorf("help.yaml: %s needs a label, help and link", p)
		}
	}
	return help
}

// schemaJS is static/schema.js: Describe, the help, and the value tests
// the editor must have.
func schemaJS(t *testing.T) []byte {
	type schema struct {
		pkgspec.Description
		Help       map[string]helpEntry `json:"help"`
		ValueTests []string             `json:"valueTests"`
	}
	b, err := json.MarshalIndent(schema{pkgspec.Describe(), loadHelp(t), pkgspec.ValueTests()}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	out.WriteString("// Generated from internal/pkgspec (Describe) and internal/editor/help.yaml: don't edit.\n" +
		"// go test ./internal/editor -run TestSchemaFile -update-editor\n" +
		"(globalThis.RoostackEditor = globalThis.RoostackEditor || {}).schema = ")
	out.Write(b)
	out.WriteString(";\n")
	return out.Bytes()
}

// testPackages are the test packages (internal/testdata/packages) the
// editor offers to start from, in the order it shows them, and the bases
// the corpus changes. This is their one list: corpus.test.js finds them
// in examples.js.
var testPackages = []string{"vaultwarden", "forgejo", "immich", "pingvin-share-x"}

// testPackage reads a test package's file, the one there is of it.
func testPackage(t *testing.T, name string) (file string, data []byte) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("..", "testdata", "packages", name, "*-roostack.*.yaml"))
	if len(files) != 1 {
		t.Fatalf("%s: want one package file, found %v", name, files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return files[0], data
}

// examplesJS is static/examples.js: the registry packages the editor
// offers to start from, from this repository's copies of them.
func examplesJS(t *testing.T) []byte {
	type example struct {
		Name  string `json:"name"`
		Title string `json:"title"`
		About string `json:"about"`
		File  string `json:"file"`
		Text  string `json:"text"`
	}
	about := map[string]string{
		"vaultwarden": "one container, SQLite",
		"forgejo":     "Postgres, an extra connection, a setup step",
		"immich":      "several containers, a GPU, media",
	}
	var examples []example
	for _, name := range testPackages {
		file, data := testPackage(t, name)
		p, err := pkgspec.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		examples = append(examples, example{name, p.Title, about[name], filepath.Base(file), string(data)})
	}
	b, err := json.MarshalIndent(examples, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return []byte("// Generated from internal/testdata/packages: don't edit.\n" +
		"// go test ./internal/editor -run TestSchemaFile -update-editor\n" +
		"(globalThis.RoostackEditor = globalThis.RoostackEditor || {}).examples = " + string(b) + ";\n")
}

// checkFile compares a generated file with what's committed, or with
// -update-editor writes it.
func checkFile(t *testing.T, path string, want []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("%s is out of date; run: go test ./internal/editor -run %s -update-editor", path, t.Name())
	}
}

// The editor's knowledge of the format is what pkgspec says now.
func TestSchemaFile(t *testing.T) {
	checkFile(t, filepath.Join("static", "schema.js"), schemaJS(t))
	checkFile(t, filepath.Join("static", "examples.js"), examplesJS(t))
}

// The manifest (static/scripts.json) names every script there is, so a
// new one can't be missing from a page, and the files it names are there.
func TestManifest(t *testing.T) {
	scripts, err := fs.Glob(Files(), "*.js")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slices.Sorted(slices.Values(scripts)), slices.Sorted(slices.Values(Scripts))) {
		t.Errorf("scripts.json names %v; static has %v", Scripts, scripts)
	}
	if len(Scripts) == 0 || Scripts[len(Scripts)-1] != "editor.js" {
		t.Errorf("scripts.json: editor.js, the page itself, must load last: %v", Scripts)
	}
	for _, name := range []string{manifest.Stylesheet, manifest.Markup} {
		if _, err := Files().Open(name); err != nil {
			t.Errorf("scripts.json: %v", err)
		}
	}
	vendored, err := os.ReadFile(filepath.Join("static", "VENDORED.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range manifest.Vendored {
		if !slices.Contains(Scripts, name) || !strings.Contains(string(vendored), "| "+name+" |") {
			t.Errorf("scripts.json: %s is vendored, so it must be one of the scripts and in VENDORED.md's table", name)
		}
	}
}

// The single-file editor holds every script and the stylesheet.
func TestStandalone(t *testing.T) {
	page, err := Standalone(Options{Version: "0.9.0", Help: "https://help.roostack.cloud/"}, []byte(":root{}"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-where="file"`, `data-version="0.9.0"`, "/* editor.js */", "RoostackEditor"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("the single-file editor has no %q", want)
		}
	}
	if strings.Contains(string(page), `src="`) {
		t.Error("the single-file editor loads a file; it must hold everything itself")
	}
}
