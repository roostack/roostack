package watch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/pkgspec"
)

// Bills of materials (docs/RED-BUTTON-PLAN.md §4.1, §10.1.2): the
// registry's CI runs Syft once on each image of each new package version
// and keeps what it found, cut down to each library's name, version, type
// and package URL, as boms/<package>/<version>/<image>.json. The watcher
// asks OSV about those exact versions, and the registry's pull-request
// check compares them with the package's built_with.

// BOMDir is where the registry keeps its bills of materials.
const BOMDir = "boms"

// BOM is one image's bill of materials.
type BOM struct {
	Image     string     `json:"image"`
	Syft      string     `json:"syft,omitempty"`
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is something Syft found in an image: a library, an OS
// package, or a runtime it recognised (pkg:golang/stdlib, pkg:generic/node).
type Artifact struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type"`
	Purl    string `json:"purl"`
}

// BOMFile is where an image's bill of materials is kept, relative to the
// registry repository.
func BOMFile(pkg, version, image string) string {
	name, _, _ := strings.Cut(image, "@")
	return filepath.Join(BOMDir, pkg, version, strings.NewReplacer("/", "_", ":", "_").Replace(name)+".json")
}

// packageImages are a package version's images: its containers', and its
// databases' where it names one (Roostack's own database images belong
// to Roostack, not the package).
func packageImages(p *pkgspec.Package) []string {
	var out []string
	for _, name := range slices.Sorted(mapKeys(p.Containers)) {
		out = append(out, p.Containers[name].Image)
	}
	for _, name := range slices.Sorted(mapKeys(p.Databases)) {
		if img := p.Databases[name].Image; img != "" {
			out = append(out, img)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// readBOMs reads every bill of materials in the repository.
func (r *Repo) readBOMs() error {
	files, _ := filepath.Glob(filepath.Join(r.Dir, BOMDir, "*", "*", "*.json"))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var bom BOM
		if err := json.NewDecoder(bytes.NewReader(data)).Decode(&bom); err != nil {
			return fmt.Errorf("%s: %w", f, err)
		}
		ver := filepath.Dir(f)
		key := filepath.Base(filepath.Dir(ver)) + " " + filepath.Base(ver)
		r.Bills[key] = append(r.Bills[key], bom)
		for _, a := range bom.Artifacts {
			if strings.HasPrefix(a.Purl, "pkg:") && strings.Contains(a.Purl, "@") {
				r.BOMs[key] = append(r.BOMs[key], a.Purl)
			}
		}
	}
	return nil
}

// Image is one image of a package version.
type Image struct{ Package, Version, Image string }

// Images are the images of the registry's package versions; missing
// leaves out those with a bill of materials already, and newest keeps
// only each package's newest version.
func (r *Repo) Images(missing, newest bool) []Image {
	var out []Image
	for _, p := range r.Packages {
		versions := p.Versions
		if newest && len(versions) > 0 {
			versions = versions[len(versions)-1:]
		}
		for _, v := range versions {
			for _, img := range p.Images[v] {
				if missing {
					if _, err := os.Stat(filepath.Join(r.Dir, BOMFile(p.Name, v, img))); err == nil {
						continue
					}
				}
				out = append(out, Image{p.Name, v, img})
			}
		}
	}
	return out
}

// Mismatch is where a package version's bills of materials and its
// built_with disagree: Syft found what a built-with name stands for
// (Found, the package URL it matched) and built_with doesn't name it, or
// built_with names it and Syft found nothing for it.
type Mismatch struct {
	Package, Version, Name, Title string
	Found                         string
}

// CheckBOMs compares each package version that has bills of materials
// with its built_with, by each name's package URLs in the registry's list
// (a URL ending in / or * matches as a prefix, any other exactly, versions
// and qualifiers aside). Names with no package URLs aren't compared.
func (r *Repo) CheckBOMs() []Mismatch {
	var out []Mismatch
	for _, p := range r.Packages {
		for _, v := range p.Versions {
			bills := r.Bills[p.Name+" "+v]
			if len(bills) == 0 {
				continue
			}
			for _, b := range r.BuiltWith {
				if len(b.Purls) == 0 {
					continue
				}
				found := ""
				for _, bom := range bills {
					for _, a := range bom.Artifacts {
						if found == "" && slices.ContainsFunc(b.Purls, func(want string) bool { return purlMatches(want, a.Purl) }) {
							found = a.Purl
						}
					}
				}
				named := slices.Contains(p.Names[v], b.Name)
				switch {
				case found != "" && !named:
					out = append(out, Mismatch{p.Name, v, b.Name, b.Title, found})
				case found == "" && named:
					out = append(out, Mismatch{p.Name, v, b.Name, b.Title, ""})
				}
			}
		}
	}
	return out
}

// purlMatches says whether a package URL from a bill of materials is the
// one a built-with name lists (want): exactly, without its version and
// qualifiers, or by prefix when want ends in / or *.
func purlMatches(want, got string) bool {
	if i := strings.IndexAny(got, "@?#"); i >= 0 {
		got = got[:i]
	}
	if prefix, ok := strings.CutSuffix(want, "*"); ok {
		return strings.HasPrefix(got, prefix)
	}
	if strings.HasSuffix(want, "/") {
		return strings.HasPrefix(got, want)
	}
	return got == want
}

// MismatchReport is the pull-request check's report, in markdown: what
// the bills of materials show that built_with doesn't say, and the other
// way round; empty when they agree.
func MismatchReport(ms []Mismatch) string {
	if len(ms) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Bills of materials and built_with\n\n")
	for _, m := range ms {
		if m.Found != "" {
			fmt.Fprintf(&b, "- **%s %s**: its images have `%s`, but `built_with:` doesn't name `%s` (%s). Add it, or say why not in the pull request.\n",
				m.Package, m.Version, m.Found, m.Name, m.Title)
		} else {
			fmt.Fprintf(&b, "- **%s %s**: `built_with:` names `%s` (%s), but Syft found nothing for it. It may be bundled or built in, where Syft can't see it; check the name is right.\n",
				m.Package, m.Version, m.Name, m.Title)
		}
	}
	return b.String()
}
