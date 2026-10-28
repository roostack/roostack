// Package updates finds what the registry's packages could update to, and
// writes the new revisions (SPEC.md §8.3.8, the update bot; the registry's
// CI runs it as roostack registry updates). Two kinds:
//
//   - a new app version: a newer tag in the image repository of each
//     container whose tag carries the app's version (the app's
//     containers), as a new package version, <version>-roostack.1;
//   - a rebuild: an image's tag now points to another digest (a project
//     rebuilt the same version, often for its base OS, and may have
//     deleted what the package pins), as the next revision of the same
//     version.
//
// The kind of update is pkgspec.UpdateKind's, which labs use too, so what
// the registry's CI calls a patch is what labs' update policies call one.
// It only finds and writes: opening pull requests, testing them and
// merging them is the registry's workflow's (docs/REGISTRY-UPDATES.md).
package updates

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"roostack.cloud/roostack/internal/imagecheck"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/registry"
)

// Registries is what the check asks image registries: imagecheck's calls,
// faked in tests.
type Registries interface {
	Tags(registry, repo string) ([]string, error)
	TagDigest(registry, repo, tag string) (string, error)
	Platforms(registry, repo, digest string) ([]string, error)
}

// Online asks the real registries.
type Online struct{}

func (Online) Tags(r, repo string) ([]string, error) { return imagecheck.Tags(r, repo) }
func (Online) TagDigest(r, repo, tag string) (string, error) {
	return imagecheck.TagDigest(r, repo, tag)
}
func (Online) Platforms(r, repo, d string) ([]string, error) {
	return imagecheck.IndexPlatforms(r, repo, d)
}

// Rules are updates.yaml at the registry's root: what the maintainers say
// about updating each package. Not read by the index's build, so a rule
// changes nothing a lab sees.
type Rules struct {
	Packages map[string]Rule `yaml:"packages"`
}

// Rule is one package's.
type Rule struct {
	// Hold: no updates for it at all, until the rule goes.
	Hold bool `yaml:"hold"`
	// Track limits new app versions to the same major ("minor": Forgejo's
	// long-term-support line) or the same major and minor ("patch").
	// Default: any newer version.
	Track string `yaml:"track"`
	// Ignore are app versions never offered (a known bad release).
	Ignore []string `yaml:"ignore"`
	// Tags overrides how a container's tag is made from the app's version,
	// by container name: {version} is where it goes, and * stands for
	// anything but a hyphen (Plex's tags end in a build hash:
	// "{version}-*", which leaves out its -amd64 and -arm64 variants).
	Tags map[string]string `yaml:"tags"`
	// Rebuilds: false leaves rebuilt images alone. Default true.
	Rebuilds *bool `yaml:"rebuilds"`
	// Why, for whoever reads the rule later.
	Reason string `yaml:"reason"`
}

// ReadRules reads updates.yaml; none is no rules.
func ReadRules(dir string) (Rules, error) {
	var r Rules
	data, err := os.ReadFile(filepath.Join(dir, "updates.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return r, fmt.Errorf("updates.yaml: %w", err)
	}
	for name, rule := range r.Packages {
		if !slices.Contains([]string{"", "minor", "patch"}, rule.Track) {
			return r, fmt.Errorf("updates.yaml: %s: track is minor or patch (or left out for any newer version)", name)
		}
		for c, t := range rule.Tags {
			if strings.Count(t, pkgspec.ReleasesPlaceholder) != 1 {
				return r, fmt.Errorf("updates.yaml: %s: tags.%s needs %s exactly once", name, c, pkgspec.ReleasesPlaceholder)
			}
		}
	}
	return r, nil
}

// Update is one package's: from its newest version to a new one.
type Update struct {
	Package string `json:"package"`
	Title   string `json:"title"`
	Tier    string `json:"tier"`
	From    string `json:"from"`
	To      string `json:"to"`
	// Kind is pkgspec.UpdateKind(From, To): patch, minor, major or other
	// for a new app version, revision for a rebuild.
	Kind   string        `json:"kind"`
	Images []ImageChange `json:"images"`
	// Releases is the project's notes for the new app version, if the
	// package links them.
	Releases string `json:"releases,omitempty"`
	// File is the new revision's path in the repository, once written.
	File string `json:"file,omitempty"`
	// data is the new file's contents.
	data []byte
}

// ImageChange is one image's new reference.
type ImageChange struct {
	Container string `json:"container"`
	From      string `json:"from"`
	To        string `json:"to"`
	// Rebuilt: the same tag, pointing to a new digest.
	Rebuilt bool `json:"rebuilt,omitempty"`
}

// Skipped is a package the check couldn't decide about, and why: worth a
// maintainer's look, but not an update.
type Skipped struct {
	Package string `json:"package"`
	Reason  string `json:"reason"`
}

// Report is what one run found.
type Report struct {
	Updates []Update  `json:"updates"`
	Skipped []Skipped `json:"skipped"`
}

// Options says what to check.
type Options struct {
	Repo string
	// Only names packages to check; all when empty.
	Only []string
	// Write: put each update's new revision in the repository.
	Write bool
	// Now dates the comment saying when digests were looked up.
	Now time.Time
}

// Check looks at each package's newest version and finds its update, if
// it has one.
func Check(o Options, regs Registries) (*Report, error) {
	rules, err := ReadRules(o.Repo)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(o.Repo, "packages"))
	if err != nil {
		return nil, err
	}
	rep := &Report{Updates: []Update{}, Skipped: []Skipped{}}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || (len(o.Only) > 0 && !slices.Contains(o.Only, name)) {
			continue
		}
		rule := rules.Packages[name]
		if rule.Hold {
			rep.Skipped = append(rep.Skipped, Skipped{name, "held in updates.yaml: " + cmp.Or(rule.Reason, "no reason given")})
			continue
		}
		u, err := checkPackage(filepath.Join(o.Repo, "packages", name), name, rule, regs, o.Now)
		if err != nil {
			rep.Skipped = append(rep.Skipped, Skipped{name, err.Error()})
			continue
		}
		if u == nil {
			continue
		}
		if o.Write {
			u.File = filepath.Join("packages", name, u.To+".yaml")
			if err := os.WriteFile(filepath.Join(o.Repo, u.File), u.data, 0o644); err != nil {
				return nil, err
			}
		}
		rep.Updates = append(rep.Updates, *u)
	}
	return rep, nil
}

// image is one image a package pins: a container's, or a database's.
type image struct {
	name string // container name, or database.<name>
	ref  imagecheck.Ref
	raw  string
}

func checkPackage(dir, name string, rule Rule, regs Registries, now time.Time) (*Update, error) {
	path, err := newestFile(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := pkgspec.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	tier := readTier(dir)
	var images []image
	for _, c := range sortedKeys(p.Containers) {
		if err := addImage(&images, c, p.Containers[c].Image); err != nil {
			return nil, err
		}
	}
	for _, d := range sortedKeys(p.Databases) {
		if p.Databases[d].Image != "" {
			if err := addImage(&images, "database."+d, p.Databases[d].Image); err != nil {
				return nil, err
			}
		}
	}
	app := pkgspec.Upstream(p.Version)
	u := &Update{Package: name, Title: p.Title, Tier: tier, From: p.Version}

	// A new app version, for the containers whose tags carry it.
	newApp, changes, err := newVersion(images, app, rule, regs)
	if err != nil {
		return nil, err
	}
	// Rebuilds, of every image not already changing.
	if rule.Rebuilds == nil || *rule.Rebuilds {
		for _, img := range images {
			if img.ref.Tag == "" || img.ref.Digest == "" || slices.ContainsFunc(changes, func(c ImageChange) bool { return c.Container == img.name }) {
				continue
			}
			d, err := regs.TagDigest(img.ref.Registry, img.ref.Repo, img.ref.Tag)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", img.name, err)
			}
			if d == img.ref.Digest {
				continue
			}
			if err := samePlatforms(regs, img, d); err != nil {
				return nil, err
			}
			to := img.ref
			to.Digest = d
			changes = append(changes, ImageChange{Container: img.name, From: img.raw, To: to.String(), Rebuilt: true})
		}
	}
	if len(changes) == 0 {
		return nil, nil
	}
	if newApp != "" {
		u.To = newApp + "-roostack.1"
		u.Releases = p.About.ReleasesURL(newApp)
	} else {
		rev, err := revision(p.Version)
		if err != nil {
			return nil, err
		}
		u.To = app + "-roostack." + strconv.Itoa(rev+1)
	}
	u.Kind = pkgspec.UpdateKind(u.From, u.To)
	u.Images = changes
	if u.data, err = rewrite(data, p.Version, u.To, changes, now); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, u.To+".yaml")); err == nil {
		return nil, fmt.Errorf("%s.yaml is already there, but isn't the newest version", u.To)
	}
	return u, nil
}

func addImage(images *[]image, name, raw string) error {
	ref, err := imagecheck.ParseRef(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	*images = append(*images, image{name, ref, raw})
	return nil
}

// newVersion finds the newest app version the rule allows that every app
// container has a tag for, and the containers' new references; "" when
// there's none newer.
func newVersion(images []image, app string, rule Rule, regs Registries) (string, []ImageChange, error) {
	type appImage struct {
		image
		pattern *regexp.Regexp
		tags    map[string]string // app version → tag
	}
	var apps []appImage
	for _, img := range images {
		tmpl, ok := rule.Tags[img.name]
		if !ok {
			i := strings.Index(img.ref.Tag, app)
			if i < 0 {
				continue // a sidecar: only rebuilds
			}
			tmpl = img.ref.Tag[:i] + pkgspec.ReleasesPlaceholder + img.ref.Tag[i+len(app):]
		}
		apps = append(apps, appImage{image: img, pattern: tagPattern(tmpl, app)})
	}
	if len(apps) == 0 {
		return "", nil, errors.New("no image's tag carries the app's version, so new versions can't be found (updates.yaml's tags can say how)")
	}
	// Each app image's tags, by the version they carry.
	for i := range apps {
		tags, err := regs.Tags(apps[i].ref.Registry, apps[i].ref.Repo)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", apps[i].name, err)
		}
		apps[i].tags = map[string]string{}
		for _, t := range tags {
			if m := apps[i].pattern.FindStringSubmatch(t); m != nil {
				// With a wildcard, several tags may carry one version: the
				// newest-sorting one wins, so it's the same each run.
				if old, ok := apps[i].tags[m[1]]; !ok || t > old {
					apps[i].tags[m[1]] = t
				}
			}
		}
	}
	// The newest version newer than app, allowed by the rule, that every
	// app image has.
	best := ""
	for v := range apps[0].tags {
		if pkgspec.CompareVersions(v, app) <= 0 || slices.Contains(rule.Ignore, v) || !tracks(rule.Track, app, v) {
			continue
		}
		if slices.ContainsFunc(apps[1:], func(a appImage) bool { _, ok := a.tags[v]; return !ok }) {
			continue
		}
		if best == "" || pkgspec.CompareVersions(v, best) > 0 {
			best = v
		}
	}
	if best == "" {
		return "", nil, nil
	}
	var changes []ImageChange
	for _, a := range apps {
		tag := a.tags[best]
		d, err := regs.TagDigest(a.ref.Registry, a.ref.Repo, tag)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", a.name, err)
		}
		if err := samePlatforms(regs, a.image, d); err != nil {
			return "", nil, err
		}
		to := a.ref
		to.Tag, to.Digest = tag, d
		changes = append(changes, ImageChange{Container: a.name, From: a.raw, To: to.String()})
	}
	return best, changes, nil
}

// tagPattern matches tags made from tmpl (* is anything but a hyphen),
// capturing the version: numbers
// joined by dots, as many as app has, so a pre-release ("3.4.0-rc.1") or
// another variant ("3.4.0-cuda") never matches the app's own pattern. A
// version that's one number is a date (PhotoPrism's 261007), so it needs
// as many digits: PhotoPrism's old tags (20220121) are older, not newer.
func tagPattern(tmpl, app string) *regexp.Regexp {
	before, after, _ := strings.Cut(tmpl, pkgspec.ReleasesPlaceholder)
	quote := func(s string) string { return strings.ReplaceAll(regexp.QuoteMeta(s), `\*`, `[^-]*`) }
	parts := strings.Count(app, ".") + 1
	version := `(\d+` + strings.Repeat(`\.\d+`, parts-1) + `)`
	if parts == 1 {
		version = `(\d{` + strconv.Itoa(len(app)) + `})`
	}
	return regexp.MustCompile("^" + quote(before) + version + quote(after) + "$")
}

// tracks says whether going from app to v is within the rule's track.
func tracks(track, app, v string) bool {
	switch track {
	case "minor":
		k := pkgspec.UpdateKind(app, v)
		return k == pkgspec.UpdatePatch || k == pkgspec.UpdateMinor
	case "patch":
		return pkgspec.UpdateKind(app, v) == pkgspec.UpdatePatch
	}
	return true
}

// samePlatforms refuses a new digest that loses amd64 or arm64, which the
// pinned one had (or, if it's gone, any image without amd64).
func samePlatforms(regs Registries, img image, d string) error {
	now, err := regs.Platforms(img.ref.Registry, img.ref.Repo, d)
	if err != nil {
		return fmt.Errorf("%s: %w", img.name, err)
	}
	want := []string{"linux/amd64"}
	if img.ref.Digest != "" {
		if was, err := regs.Platforms(img.ref.Registry, img.ref.Repo, img.ref.Digest); err == nil {
			want = slices.DeleteFunc(was, func(p string) bool { return p != "linux/amd64" && p != "linux/arm64" })
		}
	}
	for _, p := range want {
		if !slices.Contains(now, p) {
			return fmt.Errorf("%s: the new image has no build for %s, which the pinned one has", img.name, p)
		}
	}
	return nil
}

// rewrite makes the new revision from the newest one's text, so its
// comments stay: the version, each image, and the date digests were
// looked up, if the file says one. The result must parse as a package.
func rewrite(data []byte, from, to string, changes []ImageChange, now time.Time) ([]byte, error) {
	s := string(data)
	versionLine := regexp.MustCompile(`(?m)^version:(\s*)` + regexp.QuoteMeta(from) + `\b`)
	if len(versionLine.FindAllString(s, -1)) != 1 {
		return nil, fmt.Errorf("the version line (version: %s) isn't there once", from)
	}
	s = versionLine.ReplaceAllString(s, "version:${1}"+to)
	for _, c := range changes {
		if !strings.Contains(s, c.From) {
			return nil, fmt.Errorf("%s isn't in the file as written", c.From)
		}
		s = strings.ReplaceAll(s, c.From, c.To)
	}
	if !now.IsZero() {
		s = lookedUp.ReplaceAllString(s, "${1}"+now.UTC().Format("2006-01-02"))
	}
	p, err := pkgspec.Parse([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("the new revision doesn't check: %w", err)
	}
	if p.Version != to {
		return nil, fmt.Errorf("the new revision says version %s, not %s", p.Version, to)
	}
	return []byte(s), nil
}

var lookedUp = regexp.MustCompile(`(digests looked up )\d{4}-\d{2}-\d{2}`)

// newestFile is a package folder's newest version file.
func newestFile(dir string) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return "", err
	}
	files = slices.DeleteFunc(files, func(f string) bool { return filepath.Base(f) == "registry.yaml" })
	if len(files) == 0 {
		return "", errors.New("no version files")
	}
	version := func(f string) string { return strings.TrimSuffix(filepath.Base(f), ".yaml") }
	return slices.MaxFunc(files, func(a, b string) int { return pkgspec.CompareVersions(version(a), version(b)) }), nil
}

func revision(version string) (int, error) {
	_, rev, ok := strings.Cut(version, "-roostack.")
	n, err := strconv.Atoi(rev)
	if !ok || err != nil {
		return 0, fmt.Errorf("version %s has no -roostack.<revision>", version)
	}
	return n, nil
}

// readTier is registry.yaml's tier, or "" when it can't be read (the
// index's build reports that; here it only decides what may merge).
func readTier(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "registry.yaml"))
	if err != nil {
		return ""
	}
	var f registry.RegistryFacts
	if yaml.Unmarshal(data, &f) != nil {
		return ""
	}
	return f.Tier
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
