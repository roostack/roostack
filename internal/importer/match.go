package importer

import (
	"cmp"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"roostack.cloud/roostack/internal/pkgspec"
)

// Matching an old setup to a package by container path (SPEC.md §8.4,
// docs/MIGRATION-PLAN.md §3.3): a Compose file and a package both name
// folders by where they are inside the container, so each of the
// package's storage is filled from the old mount at the same place, or at
// a folder above it (Immich's old /data gives library everything but
// thumbs/ and encoded-video/, which go to the storage mounted there).

// Prefix says where a folder of the old machine is now: Old (a path on the
// old machine, or "." for the Compose file's folder) is Now, a folder in
// the person's files ("" for their top).
type Prefix struct {
	Old string `json:"old"`
	Now string `json:"now"`
}

// Leave, as a choice, leaves a storage, dump or old folder out.
const Leave = "-"

// Choices are what the person chose by hand, over what matching found.
type Choices struct {
	// Storage maps a storage to a folder in their files, or Leave.
	Storage map[string]string `json:"storage,omitempty"`
	// Dumps maps a database to a dump file in their files, or Leave.
	Dumps map[string]string `json:"dumps,omitempty"`
	// Libraries maps an old container path to a media library, or Leave.
	Libraries map[string]string `json:"libraries,omitempty"`
	// Without are critical storage the person chose to start without.
	Without []string `json:"without,omitempty"`
}

// Plan is what goes where.
type Plan struct {
	Containers []Matched  `json:"containers,omitempty"`
	Storage    []Storage  `json:"storage"`
	Databases  []Database `json:"databases,omitempty"`
	Libraries  []Library  `json:"libraries,omitempty"`
	// Left are old folders nothing of the package's takes, and why.
	Left []Left `json:"left,omitempty"`
}

// Matched is an old container and what it is to the package: one of its
// containers (Part), or databases (Database), or neither.
type Matched struct {
	Old      string `json:"old"`
	Image    string `json:"image"`
	Part     string `json:"part,omitempty"`
	Database string `json:"database,omitempty"`
	// Kind is a database's (postgres, redis…), "" for an app.
	Kind string `json:"kind,omitempty"`
}

// How something was matched.
const (
	ByPath = "path" // the old mount at the same place in the container
	ByHand = "hand" // the person chose it
	ByName = "name" // a dump, or a library, by its name
	ByData = "data" // a library holding files the old data names
)

// Why nothing is brought.
const (
	WhyLeft     = "left"      // the person left it out
	WhyNotFound = "not-found" // the old folder isn't in their files
	WhyNotMount = "not-mount" // the old container had no mount there
	WhyNoSetup  = "no-setup"  // no old container to match (choose by hand)
	WhyDBFiles  = "db-files"  // an old database's own files: its dump brings it
	WhyFile     = "file"      // a single file the package doesn't need
	WhyUnused   = "unused"    // nothing of the package's is there
)

// Storage is one of the package's storage and where it's brought from.
type Storage struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Class string `json:"class"`
	Media bool   `json:"media,omitempty"`
	Path  string `json:"path"`
	Part  string `json:"part"`
	// From is a folder in the person's files ("." for their top, "" when
	// nothing is brought), By how it was found, Why nothing is.
	From string `json:"from,omitempty"`
	By   string `json:"by,omitempty"`
	Why  string `json:"why,omitempty"`
	// Old is the old folder it comes from (or would), OldTarget where it
	// was in the old container.
	Old       string `json:"old,omitempty"`
	OldTarget string `json:"old_target,omitempty"`
	// Exclude are folders inside From that aren't copied: other storage
	// is mounted there in the container.
	Exclude []string `json:"exclude,omitempty"`
}

// Database is one of the package's databases and its dump.
type Database struct {
	Name string `json:"name"`
	Type string `json:"type"`
	From string `json:"from,omitempty"`
	By   string `json:"by,omitempty"`
	Why  string `json:"why,omitempty"`
	// Old is the old database container, if matched.
	Old string `json:"old,omitempty"`
}

// Library is an old media folder and the library that holds its files,
// shown inside the service where the old folder was.
type Library struct {
	OldTarget string `json:"old_target"`
	Old       string `json:"old"`
	Library   string `json:"library,omitempty"`
	By        string `json:"by,omitempty"`
	// Found of Sampled file names the old data names were in the library.
	Found   int `json:"found,omitempty"`
	Sampled int `json:"sampled,omitempty"`
}

// Left is an old folder nothing takes.
type Left struct {
	Container string `json:"container"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	Why       string `json:"why"`
}

// Libraries finds which of the service's server's media libraries holds
// the files an old container had at target: by file names its old data
// names there, else by name. "" when none does.
type Libraries interface {
	Find(target, source string) (library string, by string, found, sampled int)
}

// Match works out the plan: the package, the old setup (nil without one),
// where its folders are now, the person's choices, their files, and the
// server's libraries (nil for a package that reads none).
func Match(p *pkgspec.Package, setup *Setup, prefixes []Prefix, ch Choices, files fs.FS, libs Libraries) Plan {
	var plan Plan
	matched := map[string]*Container{} // package container → old one
	used := map[*Container]map[int]bool{}
	if setup != nil {
		for i := range setup.Containers {
			oc := &setup.Containers[i]
			used[oc] = map[int]bool{}
			m := Matched{Old: oc.Name, Image: oc.Image, Kind: DatabaseKind(oc.Image)}
			if m.Kind != "" {
				m.Database = matchDatabase(p, m.Kind, plan.Containers)
			} else if part := matchPart(p, oc, matched); part != "" {
				m.Part = part
				matched[part] = oc
			}
			plan.Containers = append(plan.Containers, m)
		}
	}

	// Each storage, where its first container mounts it: what's backed
	// up first, as it's what matters most.
	critical := func(name string) int {
		if p.Storage[name].Class == pkgspec.ClassCritical {
			return 0
		}
		return 1
	}
	for _, name := range slices.SortedFunc(maps.Keys(p.Storage), func(a, b string) int {
		return cmp.Or(cmp.Compare(critical(a), critical(b)), cmp.Compare(a, b))
	}) {
		st := p.Storage[name]
		s := Storage{Name: name, Title: cmp.Or(st.Title, name), Class: st.Class, Media: st.Location == pkgspec.LocationMedia}
		s.Part, s.Path = mountOf(p, name)
		if s.Path == "" {
			continue // mounted nowhere: nothing to bring
		}
		if oc := matched[s.Part]; oc != nil {
			if i, ok := covering(oc.Mounts, s.Path); ok {
				used[oc][i] = true
				m := oc.Mounts[i]
				s.Old = joinOld(m.Source, strings.TrimPrefix(s.Path, m.Target))
				s.OldTarget = s.Path
			}
		}
		switch c, chosen := ch.Storage[name]; {
		case chosen && c == Leave:
			s.Why = WhyLeft
		case chosen && validRel(c):
			s.From, s.By = c, ByHand
		case s.Old != "":
			if rel, ok := Translate(s.Old, prefixes); ok && isDir(files, rel) {
				s.From, s.By = rel, ByPath
			} else {
				s.Why = WhyNotFound
			}
		case matched[s.Part] != nil:
			s.Why = WhyNotMount
		default:
			s.Why = WhyNoSetup
		}
		plan.Storage = append(plan.Storage, s)
	}
	// What's inside a storage's folder but mounted from another storage in
	// the container isn't copied into it, and nor is another storage's
	// folder inside its own.
	for i := range plan.Storage {
		a := &plan.Storage[i]
		if a.From == "" {
			continue
		}
		for _, b := range plan.Storage {
			if b.Name == a.Name {
				continue
			}
			if b.Part == a.Part && strings.HasPrefix(b.Path, a.Path+"/") {
				a.Exclude = append(a.Exclude, strings.TrimPrefix(b.Path, a.Path+"/"))
			}
			if rest, ok := inside(b.From, a.From); ok {
				a.Exclude = append(a.Exclude, rest)
			}
		}
		slices.Sort(a.Exclude)
		a.Exclude = slices.Compact(a.Exclude)
	}

	// A dump for each database its backups dump.
	for _, name := range slices.Sorted(maps.Keys(p.Databases)) {
		d := p.Databases[name]
		if !d.BackedUp() || (d.Type != pkgspec.DBPostgres && !pkgspec.MySQLFamily(d.Type)) {
			continue // SQLite comes with its folder; Redis is a queue
		}
		db := Database{Name: name, Type: d.Type}
		for _, m := range plan.Containers {
			if m.Database == name {
				db.Old = m.Old
			}
		}
		switch c, chosen := ch.Dumps[name]; {
		case chosen && c == Leave:
			db.Why = WhyLeft
		case chosen && validRel(c):
			db.From, db.By = c, ByHand
		default:
			if f := findDump(files, name, p.Name, d.Type); f != "" {
				db.From, db.By = f, ByName
			} else {
				db.Why = WhyNotFound
			}
		}
		plan.Databases = append(plan.Databases, db)
	}

	// Media an app only read, shown where it was (SPEC.md §8.4).
	if libs != nil {
		for _, part := range slices.Sorted(maps.Keys(matched)) {
			if !readsLibraries(p, part) {
				continue
			}
			oc := matched[part]
			for i, m := range oc.Mounts {
				if used[oc][i] || systemTarget(m.Target) || overStorage(p, part, m.Target) {
					continue
				}
				used[oc][i] = true
				l := Library{OldTarget: m.Target, Old: m.Source}
				if c, chosen := ch.Libraries[m.Target]; chosen {
					if c != Leave {
						l.Library, l.By = c, ByHand
					}
				} else {
					l.Library, l.By, l.Found, l.Sampled = libs.Find(m.Target, m.Source)
				}
				plan.Libraries = append(plan.Libraries, l)
			}
		}
	}

	// What's left of the old setup, and why it isn't brought.
	if setup != nil {
		for i := range setup.Containers {
			oc := &setup.Containers[i]
			kind := DatabaseKind(oc.Image)
			for j, m := range oc.Mounts {
				if used[oc][j] {
					continue
				}
				why := WhyUnused
				switch {
				case kind != "":
					why = WhyDBFiles
				case strings.HasPrefix(m.Target, "/etc/") || m.Target == "/var/run/docker.sock":
					why = WhyFile
				}
				plan.Left = append(plan.Left, Left{Container: oc.Name, Source: m.Source, Target: m.Target, Why: why})
			}
		}
	}
	return plan
}

// matchPart finds the package container an old app container is: the same
// image repository, else the same name (immich-server is server).
func matchPart(p *pkgspec.Package, oc *Container, taken map[string]*Container) string {
	names := slices.Sorted(maps.Keys(p.Containers))
	for _, n := range names {
		if taken[n] == nil && Repository(p.Containers[n].Image) == Repository(oc.Image) {
			return n
		}
	}
	for _, n := range names {
		for _, have := range []string{oc.Service, oc.Name} {
			h := strings.ToLower(have)
			if taken[n] == nil && h != "" && (h == n || strings.HasSuffix(h, "-"+n) || strings.HasSuffix(h, "_"+n)) {
				return n
			}
		}
	}
	return ""
}

// matchDatabase finds the package database an old database container is:
// the first of its kind not yet matched.
func matchDatabase(p *pkgspec.Package, kind string, so []Matched) string {
	for _, n := range slices.Sorted(maps.Keys(p.Databases)) {
		t := p.Databases[n].Type
		if t != kind && !(pkgspec.MySQLFamily(t) && pkgspec.MySQLFamily(kind)) {
			continue
		}
		if !slices.ContainsFunc(so, func(m Matched) bool { return m.Database == n }) {
			return n
		}
	}
	return ""
}

// mountOf is the first container mounting a storage, and where.
func mountOf(p *pkgspec.Package, storage string) (string, string) {
	for _, n := range slices.Sorted(maps.Keys(p.Containers)) {
		for _, m := range p.Containers[n].Mounts {
			if m.Storage == storage {
				return n, m.Path
			}
		}
	}
	return "", ""
}

// readsLibraries says whether a package container mounts media libraries.
func readsLibraries(p *pkgspec.Package, part string) bool {
	c := p.Containers[part]
	return c != nil && slices.ContainsFunc(c.Mounts, func(m pkgspec.Mount) bool { return m.Libraries })
}

// overStorage says whether an old mount is at or above one of the
// container's storage (so it's part of a storage's, not media).
func overStorage(p *pkgspec.Package, part, target string) bool {
	for _, m := range p.Containers[part].Mounts {
		if m.Storage != "" && within(m.Path, target) {
			return true
		}
	}
	return false
}

// systemTarget is a container path that's the system's, not data.
func systemTarget(t string) bool {
	return strings.HasPrefix(t, "/etc/") || strings.HasPrefix(t, "/dev/") || strings.HasPrefix(t, "/sys/") ||
		strings.HasPrefix(t, "/proc/") || t == "/var/run/docker.sock" || t == "/run/docker.sock"
}

// covering finds the old mount at path, or at the deepest folder above it.
func covering(mounts []Mount, p string) (int, bool) {
	best, at := -1, ""
	for i, m := range mounts {
		t := strings.TrimSuffix(m.Target, "/")
		if t == "" {
			t = "/"
		}
		if within(p, t) && len(t) > len(at) {
			best, at = i, t
		}
	}
	return best, best >= 0
}

// within says whether path p is dir or inside it.
func within(p, dir string) bool {
	return p == dir || dir == "/" || strings.HasPrefix(p, dir+"/")
}

// joinOld adds a container path's rest to an old folder.
func joinOld(source, rest string) string {
	if rest == "" {
		return source
	}
	return strings.TrimSuffix(source, "/") + rest
}

// Translate finds where an old machine's path is in the person's files,
// by the longest prefix line covering it.
func Translate(old string, prefixes []Prefix) (string, bool) {
	o := cleanOld(old)
	best, found := -1, false
	for i, pr := range prefixes {
		po := cleanOld(pr.Old)
		if (po == o || po == "." && !path.IsAbs(o) || po == "/" && path.IsAbs(o) || strings.HasPrefix(o, po+"/")) &&
			(!found || len(po) > len(cleanOld(prefixes[best].Old))) {
			best, found = i, true
		}
	}
	if !found {
		return "", false
	}
	now := cmp.Or(prefixes[best].Now, ".")
	if !validRel(now) {
		return "", false
	}
	po := cleanOld(prefixes[best].Old)
	rest := strings.TrimPrefix(strings.TrimPrefix(o, po), "/")
	if po == "." {
		rest = o
	}
	rel := path.Join(now, rest)
	if rel == "" {
		rel = "."
	}
	return rel, validRel(rel)
}

// inside says whether folder b of the person's files is inside folder a,
// and where in it.
func inside(b, a string) (string, bool) {
	switch {
	case b == "" || a == "" || b == a:
		return "", false
	case a == ".":
		return b, true
	case strings.HasPrefix(b, a+"/"):
		return strings.TrimPrefix(b, a+"/"), true
	}
	return "", false
}

// cleanOld cleans an old machine's path; a Compose file's relative one
// stays relative ("library" from "./library").
func cleanOld(p string) string { return path.Clean(strings.TrimSpace(p)) }

// validRel says whether a folder in the person's files is a plain
// relative path ("." for their top).
func validRel(rel string) bool { return fs.ValidPath(rel) }

// isDir says whether rel is a folder in files.
func isDir(files fs.FS, rel string) bool {
	if files == nil {
		return false
	}
	fi, err := fs.Stat(files, rel)
	return err == nil && fi.IsDir()
}

// findDump looks for a database's dump by its name, at the top of the
// person's files or one folder down: <database>.dump, <database>.sql,
// <package>.dump…, else the only dump there is.
func findDump(files fs.FS, db, pkg, kind string) string {
	if files == nil {
		return ""
	}
	exts := []string{".sql"}
	if kind == pkgspec.DBPostgres {
		exts = []string{".dump", ".pgdump", ".sql", ".backup"}
	}
	var names []string
	for _, base := range []string{db, pkg} {
		for _, e := range exts {
			names = append(names, base+e)
		}
	}
	var all []string
	for _, dir := range append([]string{"."}, subfolders(files, ".")...) {
		entries, _ := fs.ReadDir(files, dir)
		for _, e := range entries {
			if e.Type().IsRegular() && slices.ContainsFunc(exts, func(x string) bool { return strings.HasSuffix(e.Name(), x) }) {
				all = append(all, path.Join(dir, e.Name()))
			}
		}
	}
	for _, n := range names {
		for _, f := range all {
			if path.Base(f) == n {
				return f
			}
		}
	}
	if len(all) == 1 {
		return all[0]
	}
	return ""
}

// subfolders are the folders in dir (not hidden ones).
func subfolders(files fs.FS, dir string) []string {
	entries, _ := fs.ReadDir(files, dir)
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && e.Name() != "lost+found" {
			out = append(out, path.Join(dir, e.Name()))
		}
	}
	return out
}

// GuessPrefixes suggests where the old folders the package needs are now:
// for each one no line covers, a folder in the person's files (up to three
// down) with the same name, preferring the one whose folders above match
// most. A line covers every folder under it.
func GuessPrefixes(p *pkgspec.Package, setup *Setup, have []Prefix, files fs.FS) []Prefix {
	if setup == nil || files == nil {
		return nil
	}
	plan := Match(p, setup, have, Choices{}, files, nil)
	// Folders higher up first, so one line covers those below.
	slices.SortStableFunc(plan.Storage, func(a, b Storage) int { return cmp.Compare(len(a.Old), len(b.Old)) })
	var out []Prefix
	for _, s := range plan.Storage {
		if s.Old == "" || s.From != "" {
			continue
		}
		if rel, ok := Translate(s.Old, append(slices.Clone(have), out...)); ok && isDir(files, rel) {
			continue
		}
		if pr, ok := guessOne(cleanOld(s.Old), files); ok && !slices.Contains(out, pr) {
			out = append(out, pr)
		}
	}
	return out
}

// guessOne finds a folder named as old's last part, the one matching most
// of its parts from the end.
func guessOne(old string, files fs.FS) (Prefix, bool) {
	parts := strings.Split(strings.Trim(old, "/"), "/")
	want := parts[len(parts)-1]
	best, score := "", 0
	level := []string{"."}
	for depth := 0; depth < 3 && len(level) > 0; depth++ {
		var next []string
		for _, dir := range level {
			for _, sub := range subfolders(files, dir) {
				if len(next) < 2000 {
					next = append(next, sub)
				}
				if path.Base(sub) != want {
					continue
				}
				got := strings.Split(sub, "/")
				n := 0
				for n < len(got) && n < len(parts) && got[len(got)-1-n] == parts[len(parts)-1-n] {
					n++
				}
				if n > score {
					best, score = sub, n
				}
			}
		}
		level = next
	}
	if best == "" {
		return Prefix{}, false
	}
	// The line is for the folders the two are in: the old machine's folder
	// above it (/opt/immich) is the one above the folder found.
	oldDir := path.Dir(old)
	if !path.IsAbs(old) && oldDir == "" {
		oldDir = "."
	}
	return Prefix{Old: oldDir, Now: path.Dir(best)}, true
}
