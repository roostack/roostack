package generate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/pkgspec"
)

// Config files a package declares (SPEC.md §8.1, "Config files a package
// declares"): files the app reads its settings from, which Roostack
// writes inside the service's storage from the package's template and the
// service's settings. Their templates may use the service's secrets, but
// generation never has them: each secret is a placeholder, which the
// server running the service fills in as it writes the file
// (reconcile.Plan). So what Roostack shows, and its golden files, never
// hold a secret.

// ConfigFile is a config file as generated: its content with placeholders
// for the secrets in it.
type ConfigFile struct {
	// Name is the package's name for it, Title what it is for people.
	Name, Title string
	// Dir is its storage folder on the server, and Path where it is inside
	// it: the server writes through Dir only, so a symlink the app makes
	// in its own storage can't send the write elsewhere.
	Dir, Path string
	Format    string
	// Block: Content is Roostack's lines at the end of a file the app
	// makes itself, between BlockStart and BlockEnd, which Roostack adds
	// once the file is there and keeps up to date, leaving the rest alone.
	Block bool
	// Content is the file, or the block with its first and last lines,
	// with a placeholder (SecretPlaceholder) for each secret.
	Content string
	// Secrets are the secrets in it, by their Podman names.
	Secrets []ConfigSecret
	// Restarts are the containers that mount its storage (their Quadlet
	// files' names), restarted when it changes.
	Restarts []string
}

// ConfigSecret is a secret in a config file.
type ConfigSecret struct {
	// Name is its Podman secret.
	Name string
	// Title is what the pages show in its place.
	Title string
	// InstallOnly secrets are deleted once the install hooks succeed; the
	// file is then written again with an empty value in their place, so no
	// copy is kept.
	InstallOnly bool
}

// HostPath is where the file is on the server.
func (f ConfigFile) HostPath() string { return filepath.Join(f.Dir, f.Path) }

// The first and last lines of a block, after the format's comment sign.
const (
	BlockStart = "BEGIN Roostack"
	BlockEnd   = "END Roostack"
)

// configHeader starts a whole file, in formats with comments.
const configHeader = "Roostack writes this file from the package and the service's settings, and will replace any change made here: change them on the service's Settings page."

// blockHeader follows BlockStart.
const blockHeader = "Roostack writes the lines down to " + BlockEnd + " from the package and the service's settings, and will replace any change made to them."

// commentSign is how a format's comments start ("" if it has none).
func commentSign(format string) string {
	switch format {
	case pkgspec.FormatPython, pkgspec.FormatYAML:
		return "#"
	}
	return ""
}

// SecretPlaceholder stands for a secret in a config file until it's
// written: private-use characters around its Podman name, which neither a
// template's values nor a secret can hold.
func SecretPlaceholder(name string) string { return "" + name + "" }

// Shown is the file as the pages and `roostack render` show it: each
// secret's placeholder as ‹secret: its title›, an install-only one's
// saying it's there only until setup has finished.
func (f ConfigFile) Shown() string {
	s := f.Content
	for _, sec := range f.Secrets {
		shown := "‹secret: " + sec.Title + "›"
		if sec.InstallOnly {
			shown = "‹secret: " + sec.Title + ", until setup has finished›"
		}
		s = strings.ReplaceAll(s, SecretPlaceholder(sec.Name), shown)
	}
	return s
}

// Fill is the file with the secrets' values in place: quoted as JSON
// writes a string, which Python, YAML, and JSON all read, or as they are
// in a text file. A secret missing from values (an install-only one
// deleted since) is empty.
func (f ConfigFile) Fill(values map[string]string) string {
	s := f.Content
	for _, sec := range f.Secrets {
		s = strings.ReplaceAll(s, SecretPlaceholder(sec.Name), f.quote(values[sec.Name]))
	}
	return s
}

func (f ConfigFile) quote(v string) string {
	if f.Format == pkgspec.FormatText {
		return v
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v) // a string always encodes
	return strings.TrimSuffix(b.String(), "\n")
}

// SetBlock is a file's text with the block in place of its old one, or
// at its end after a blank line. ok is false if the old block has lost
// its last line, which a person editing the file may do: Roostack then
// leaves the file alone rather than guess where its lines end.
func (f ConfigFile) SetBlock(text, block string) (out string, ok bool) {
	c := commentSign(f.Format)
	lines := strings.SplitAfter(text, "\n")
	for i, l := range lines {
		if !strings.HasPrefix(l, c+" "+BlockStart) {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == c+" "+BlockEnd {
				return strings.Join(lines[:i], "") + block + strings.Join(lines[j+1:], ""), true
			}
		}
		return text, false
	}
	switch {
	case text == "":
	case strings.HasSuffix(text, "\n"):
		text += "\n"
	default:
		text += "\n\n"
	}
	return text + block, true
}

// configFiles renders the package's config files, each with a placeholder
// for each secret: .Secrets.<setting> and, while single sign-on is on,
// .SSO.ClientSecret.
func (g *gen) configFiles() error {
	for _, name := range slices.Sorted(maps.Keys(g.pkg.ConfigFiles)) {
		f := g.pkg.ConfigFiles[name]
		at := "config_files." + name + ".content"
		ctx := *g.ctx
		ctx.SSO.fileSecret = SSOSecretName(g.name)
		ctx.Secrets = map[string]string{}
		titles := map[string]ConfigSecret{SSOSecretName(g.name): {Name: SSOSecretName(g.name), Title: "Single sign-on's client secret"}}
		for _, s := range g.pkg.Settings {
			if s.Type == pkgspec.SettingSecret && !s.FromHook {
				n := secretName(g.name, s.Key)
				ctx.Secrets[s.Key] = SecretPlaceholder(n)
				titles[n] = ConfigSecret{Name: n, Title: s.Title, InstallOnly: s.InstallOnly}
			}
		}
		text, err := ctx.render(at, f.Content)
		if err != nil {
			return err
		}
		text = strings.TrimRight(text, "\n") + "\n"
		if c := commentSign(f.Format); f.Block {
			text = c + " " + BlockStart + ": " + blockHeader + "\n" + text + c + " " + BlockEnd + "\n"
		} else if c != "" {
			text = c + " " + configHeader + "\n" + text
		}
		cf := ConfigFile{Name: name, Title: f.Title, Dir: g.storagePath(f.Storage), Path: f.Path, Format: f.Format,
			Block: f.Block, Content: text}
		for _, n := range slices.Sorted(maps.Keys(titles)) {
			if strings.Contains(text, SecretPlaceholder(n)) {
				cf.Secrets = append(cf.Secrets, titles[n])
			}
		}
		for _, c := range slices.Sorted(maps.Keys(g.pkg.Containers)) {
			if !g.included[c] {
				continue
			}
			for _, m := range g.pkg.Containers[c].Mounts {
				if m.Storage == f.Storage {
					cf.Restarts = append(cf.Restarts, containerUnitName(g.name, c)+".container")
					break
				}
			}
		}
		g.out.ConfigFiles = append(g.out.ConfigFiles, cf)
	}
	return nil
}

// checkConfigFile parses a YAML or JSON config file as it would be
// written with example secrets, so a template that writes a broken file
// shows when the package is validated (Check). Python and text files
// aren't checked: Go has no Python parser.
func checkConfigFile(f ConfigFile) error {
	values := map[string]string{}
	for _, s := range f.Secrets {
		values[s.Name] = "example-secret"
	}
	text := f.Fill(values)
	var v any
	switch f.Format {
	case pkgspec.FormatYAML:
		if err := yaml.Unmarshal([]byte(text), &v); err != nil {
			return fmt.Errorf("config_files.%s: once rendered, it isn't valid YAML: %w", f.Name, err)
		}
	case pkgspec.FormatJSON:
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			return fmt.Errorf("config_files.%s: once rendered, it isn't valid JSON: %w", f.Name, err)
		}
	}
	return nil
}
