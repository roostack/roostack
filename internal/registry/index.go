package registry

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"roostack.cloud/roostack/internal/pkgspec"
)

// The files a registry publishes, beside each other at its address
// (SPEC.md §8.3.3). Package files and icons are at the paths the index
// gives.
const (
	KeysFile     = "keys.json"
	KeysSigFile  = "keys.json.sig"
	IndexFile    = "index.json"
	IndexSigFile = "index.json.sig"
)

// The type each signed file says it is, checked after its signature, so
// an index can't pass as a key statement or the other way round.
const (
	TypeKeys  = "roostack-registry-keys"
	TypeIndex = "roostack-registry-index"
)

// Format is the index format this Roostack reads. Fields may be added
// within a format (older Roostacks ignore them); a change that older
// Roostacks would misread is a new format, published at a new address
// (…/v2/) beside this one, so they keep reading theirs.
const Format = 1

// KeyStatement is keys.json: the root key's statement naming the keys
// that may sign the index. Its serial only goes up: a lab refuses one
// lower than it has seen, so an old statement naming a leaked key can't be
// replayed. It has no expiry: the root key is offline.
type KeyStatement struct {
	Type        string    `json:"type"`
	Serial      int64     `json:"serial"`
	Date        time.Time `json:"date"`
	SigningKeys []string  `json:"signing_keys"`
}

// Keys parses the statement's signing keys.
func (s *KeyStatement) Keys() ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, k := range s.SigningKeys {
		pub, err := ParsePublic(k)
		if err != nil {
			return nil, err
		}
		keys = append(keys, pub)
	}
	return keys, nil
}

// ParseKeys reads keys.json, checking it against its signature by one of
// the root keys.
func ParseKeys(data, sig []byte, roots []ed25519.PublicKey) (*KeyStatement, error) {
	if _, err := Verify(data, sig, roots); err != nil {
		return nil, fmt.Errorf("%s: %w", KeysFile, err)
	}
	var s KeyStatement
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", KeysFile, err)
	}
	if s.Type != TypeKeys {
		return nil, fmt.Errorf("%s: type is %q, not %q", KeysFile, s.Type, TypeKeys)
	}
	if s.Serial < 1 {
		return nil, fmt.Errorf("%s: no serial", KeysFile)
	}
	keys, err := s.Keys()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", KeysFile, err)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s names no signing key", KeysFile)
	}
	return &s, nil
}

// Index is index.json: every package in the registry and every version,
// built by the registry's CI from the repository (SPEC.md §8.3.3).
type Index struct {
	Type   string `json:"type"`
	Format int    `json:"format"`
	// Serial is when CI signed it, in seconds since 1970: it only goes up,
	// and a lab refuses an index older than the one it has.
	Serial int64     `json:"serial"`
	Date   time.Time `json:"date"`
	// Commit is the registry commit it was built from, so a technical user
	// can find exactly these files.
	Commit   string              `json:"commit,omitempty"`
	Packages map[string]*Package `json:"packages"`
	// BuiltWith is the registry's list of the names packages may say
	// they're built with (BuiltWithFile), for their titles; older indexes
	// have none.
	BuiltWith []BuiltWith `json:"built_with,omitempty"`
}

// Package is one package in the index: the registry's facts about it, and
// its versions. Title, description and categories are the newest
// version's, so the website and the catalog can list packages without
// fetching every file (SPEC.md §8.3.6).
type Package struct {
	Title       string              `json:"title"`
	Description string              `json:"description"`
	Tier        string              `json:"tier"`
	Maintainers []string            `json:"maintainers"`
	Categories  []string            `json:"categories,omitempty"`
	Icon        *File               `json:"icon,omitempty"`
	Withdrawn   *Withdrawal         `json:"withdrawn,omitempty"`
	Versions    map[string]*Version `json:"versions"`
}

// File is a file the index vouches for by its SHA-256.
type File struct {
	Path   string `json:"path"` // relative to the registry's address
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Version is one version of a package.
type Version struct {
	File
	Schema           string `json:"schema"`
	RequiresRoostack string `json:"requires_roostack,omitempty"`
	// Architectures are the processors CI found a build of every image
	// for; empty when it didn't check.
	Architectures []string    `json:"architectures,omitempty"`
	Withdrawn     *Withdrawal `json:"withdrawn,omitempty"`
}

// Withdrawal levels (SPEC.md §8.3.4).
const (
	LevelNotice  = "notice"
	LevelWarning = "warning"
	LevelStop    = "stop"
)

// Tiers (SPEC.md §8.3.4); a package of your own isn't in the registry.
const (
	TierOfficial  = "official"
	TierCommunity = "community"
)

// Withdrawal is the registry pulling a version or a whole package.
type Withdrawal struct {
	Level  string `json:"level"`
	Reason string `json:"reason"`
	Link   string `json:"link,omitempty"`
}

// Blocks says whether the withdrawal stops the version being installed:
// warning and stop do, a notice doesn't.
func (w *Withdrawal) Blocks() bool { return w != nil && w.Level != LevelNotice }

var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseIndex reads index.json, checking it against its signature by one
// of the signing keys.
func ParseIndex(data, sig []byte, signing []ed25519.PublicKey) (*Index, error) {
	if _, err := Verify(data, sig, signing); err != nil {
		return nil, fmt.Errorf("%s: %w", IndexFile, err)
	}
	var ix Index
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, fmt.Errorf("%s: %w", IndexFile, err)
	}
	if err := ix.Check(); err != nil {
		return nil, fmt.Errorf("%s: %w", IndexFile, err)
	}
	return &ix, nil
}

// Check checks everything a lab relies on: names and versions that can
// name files, paths in the registry's layout, hashes, tiers and levels.
func (ix *Index) Check() error {
	if ix.Type != TypeIndex {
		return fmt.Errorf("type is %q, not %q", ix.Type, TypeIndex)
	}
	if ix.Format != Format {
		return fmt.Errorf("format %d; this Roostack reads format %d", ix.Format, Format)
	}
	if ix.Serial < 1 || ix.Date.IsZero() {
		return errors.New("no serial or date")
	}
	var errs []error
	if err := checkBuiltWith(ix.BuiltWith); err != nil {
		errs = append(errs, fmt.Errorf("built_with: %w", err))
	}
	for name, p := range ix.Packages {
		if !pkgspec.ValidName(name) || p == nil {
			errs = append(errs, fmt.Errorf("package %q: not a valid package name", name))
			continue
		}
		if p.Tier != TierOfficial && p.Tier != TierCommunity {
			errs = append(errs, fmt.Errorf("%s: tier %q", name, p.Tier))
		}
		if err := p.Withdrawn.check(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
		if p.Icon != nil {
			if err := p.Icon.check("packages/" + name + "/icon.png"); err != nil {
				errs = append(errs, fmt.Errorf("%s: icon: %w", name, err))
			}
		}
		for version, v := range p.Versions {
			at := name + " " + version
			if !pkgspec.ValidVersion(version) || v == nil {
				errs = append(errs, fmt.Errorf("%s: not a valid version", at))
				continue
			}
			if err := v.check("packages/" + name + "/" + version + ".yaml"); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", at, err))
			}
			if err := v.Withdrawn.check(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", at, err))
			}
		}
	}
	return errors.Join(errs...)
}

func (f File) check(path string) error {
	if f.Path != path {
		return fmt.Errorf("path %q, not %q", f.Path, path)
	}
	if !sha256Re.MatchString(f.SHA256) {
		return errors.New("no SHA-256")
	}
	if f.Size < 1 || f.Size > maxFile {
		return fmt.Errorf("size %d", f.Size)
	}
	return nil
}

func (w *Withdrawal) check() error {
	if w == nil {
		return nil
	}
	if !slices.Contains([]string{LevelNotice, LevelWarning, LevelStop}, w.Level) {
		return fmt.Errorf("withdrawal level %q", w.Level)
	}
	if w.Reason == "" {
		return errors.New("withdrawal without a reason")
	}
	return nil
}

// Offer is the version of a package a lab offers to install: the newest
// that isn't withdrawn (a notice doesn't count), that this Roostack can
// run, and that has a build for one of the lab's processors (arches, as
// GOARCH says them). In a lab mixing processors it may run on only some
// of its servers: installing checks the one chosen (SPEC.md §8.3.3).
// False when there's none.
func (p *Package) Offer(roostack string, arches ...string) (string, *Version, bool) {
	if p.Withdrawn.Blocks() {
		return "", nil, false
	}
	best := ""
	for version, v := range p.Versions {
		if v.Withdrawn.Blocks() || !pkgspec.RunsOn(v.RequiresRoostack, roostack) {
			continue
		}
		if !v.RunsOnAny(arches) {
			continue
		}
		if best == "" || pkgspec.CompareVersions(version, best) > 0 {
			best = version
		}
	}
	if best == "" {
		return "", nil, false
	}
	return best, p.Versions[best], true
}

// RunsOnAny says whether a version has a build for one of arches, or
// doesn't say which it has (CI didn't check).
func (v *Version) RunsOnAny(arches []string) bool {
	if len(v.Architectures) == 0 {
		return true
	}
	for _, a := range arches {
		if slices.Contains(v.Architectures, a) {
			return true
		}
	}
	return false
}

// Architectures are the processors a package's version has a build of
// every image for (Version.Architectures), to check against the server it
// runs on. Nil when the index doesn't say: CI didn't check, or the index
// doesn't have that version.
func (ix *Index) Architectures(name, version string) []string {
	if ix == nil || ix.Packages[name] == nil || ix.Packages[name].Versions[version] == nil {
		return nil
	}
	return ix.Packages[name].Versions[version].Architectures
}

// NeedsRoostack is the Roostack release a package needs when no version
// of it runs on this one (roostack) but its newest version would run on a
// newer Roostack: it isn't withdrawn and has a build for one of arches.
// The catalog shows such a package as needing a newer Roostack rather than
// leaving it out (SPEC.md §8.3.6). "" when the package is offered, or
// can't be for another reason (withdrawn, no build for the lab's
// processors).
func (p *Package) NeedsRoostack(roostack string, arches ...string) string {
	if _, _, ok := p.Offer(roostack, arches...); ok || p.Withdrawn.Blocks() {
		return ""
	}
	best := ""
	for version, v := range p.Versions {
		if v.Withdrawn.Blocks() || !v.RunsOnAny(arches) {
			continue
		}
		if best == "" || pkgspec.CompareVersions(version, best) > 0 {
			best = version
		}
	}
	if best == "" {
		return ""
	}
	return p.Versions[best].RequiresRoostack
}

// Delegate writes a key statement naming the signing keys, and its
// signature by the root keys: the second step of the key ceremony
// (docs/REGISTRY-KEYS.md).
func Delegate(serial int64, date time.Time, signing []ed25519.PublicKey, roots ...ed25519.PrivateKey) (data, sig []byte, err error) {
	if serial < 1 || len(signing) == 0 || len(roots) == 0 {
		return nil, nil, errors.New("a key statement needs a serial of 1 or more, a signing key and a root key")
	}
	s := KeyStatement{Type: TypeKeys, Serial: serial, Date: date.UTC().Truncate(time.Second)}
	for _, k := range signing {
		s.SigningKeys = append(s.SigningKeys, EncodePublic(k))
	}
	data, err = json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	data = append(data, '\n')
	return data, Sign(data, roots...), nil
}
