package pkgspec

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

// Config file formats (SPEC.md §8.1, "Config files a package declares"):
// how a secret is quoted in the file, whether it starts with a comment
// saying Roostack writes it, how its rendered example is checked, and
// whether it can be a block.
const (
	FormatPython = "python"
	FormatYAML   = "yaml"
	FormatJSON   = "json"
	FormatText   = "text"
)

// ConfigFormats are the formats a config file can have.
var ConfigFormats = []string{FormatPython, FormatYAML, FormatJSON, FormatText}

// BlockFormats are the formats a block can be written in: those where a
// later line wins, so Roostack's lines at the end override the app's own.
// YAML, JSON, and INI refuse a key given twice.
var BlockFormats = []string{FormatPython}

// ConfigFile is a file the app reads its settings from, which Roostack
// writes from the package's template and the service's settings, inside
// one of the service's storage entries, and writes again when they change,
// restarting the containers that mount that storage (SPEC.md §8.1).
type ConfigFile struct {
	// Title says what it is, for people ("Seafile's web settings").
	Title string `yaml:"title"`
	// Storage is the storage entry it's in, and Path where inside it, e.g.
	// seafile/conf/seahub_settings.py.
	Storage string `yaml:"storage"`
	Path    string `yaml:"path"`
	Format  string `yaml:"format"`
	// Block makes Content Roostack's lines at the end of a file the app
	// makes itself (Seafile's seahub_settings.py, which holds its own
	// SECRET_KEY): Roostack adds them once the app has made the file on
	// its first start, and leaves the rest of the file alone.
	Block bool `yaml:"block"`
	// Content is a template, like env's, which can also use the service's
	// secrets ({{ .Secrets.<setting> }}, {{ .SSO.ClientSecret }}): Roostack
	// fills them in as it writes the file, so they never appear in what
	// it shows.
	Content string `yaml:"content"`
}

// configPathRe is a path inside a storage folder: plain names joined by
// /, none starting with a dot, so none is .. or a hidden file.
var configPathRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*(/[A-Za-z0-9_][A-Za-z0-9._-]*)*$`)

// maxConfigTitle keeps a config file's title to a few words, as a page
// names it beside its path.
const maxConfigTitle = 60

func (p *Package) validateConfigFiles(v *validator) {
	at := map[string]string{} // storage/path → the config file there
	for _, name := range slices.Sorted(maps.Keys(p.ConfigFiles)) {
		f, where := p.ConfigFiles[name], "config_files."+name
		if !nameRe.MatchString(name) {
			v.addf("%s: invalid name", where)
		}
		if f == nil {
			v.addf("%s: title, storage, path, format, and content are required", where)
			continue
		}
		if strings.TrimSpace(f.Title) == "" || len([]rune(f.Title)) > maxConfigTitle || strings.ContainsAny(f.Title, "\n\r") {
			v.addf("%s.title: required, one line of at most %d characters", where, maxConfigTitle)
		}
		switch {
		case p.Storage[f.Storage] == nil:
			v.addf("%s.storage: unknown storage %q", where, f.Storage)
		case !p.mountsStorage(f.Storage):
			v.addf("%s.storage: no container mounts %q, so the app couldn't read the file", where, f.Storage)
		}
		if !configPathRe.MatchString(f.Path) {
			v.addf("%s.path: %q must be a path inside the storage folder, like conf/settings.yaml: letters, digits, and - _ . /, each part starting with a letter, digit, or _", where, f.Path)
		} else if other, dup := at[f.Storage+"/"+f.Path]; dup {
			v.addf("%s.path: config_files.%s is there already", where, other)
		} else {
			at[f.Storage+"/"+f.Path] = name
		}
		if !slices.Contains(ConfigFormats, f.Format) {
			v.addf("%s.format: must be one of %s; got %q", where, strings.Join(ConfigFormats, ", "), f.Format)
		} else if f.Block && !slices.Contains(BlockFormats, f.Format) {
			v.addf("%s.block: only for %s, where a later line wins (a %s file refuses a key given twice)", where, strings.Join(BlockFormats, ", "), f.Format)
		}
		if strings.TrimSpace(f.Content) == "" {
			v.addf("%s.content: required", where)
		}
	}
}

// mountsStorage says whether a container mounts the storage entry.
func (p *Package) mountsStorage(storage string) bool {
	for _, c := range p.Containers {
		if c == nil {
			continue
		}
		for _, m := range c.Mounts {
			if m.Storage == storage {
				return true
			}
		}
	}
	return false
}
