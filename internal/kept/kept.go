// Package kept is data Roostack set aside rather than deleting it (SPEC.md
// §8.4, "Kept copies"): before a restore, a move, bringing data in or a
// put back, and what one of those that didn't finish left. Each kept
// folder is named after the folder it's a copy of, why, and when:
// <folder>.<why>-<time>, e.g. immich.before-import-20261018-162033. The
// folders set aside at one moment for one reason make one Copy.
package kept

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Why says why a copy is kept. It's also the folder name's marker.
type Why string

const (
	// BeforeRestore is the data a restore replaced.
	BeforeRestore Why = "before-restore"
	// FailedRestore is what a restore that didn't finish had put in
	// place, set aside when the data from before went back.
	FailedRestore Why = "failed-restore"
	// BeforeMove is what a server had before a service moved in.
	BeforeMove Why = "before-move"
	// MovedAway is a service's old copy on the server it moved away from
	// (its note says where to: "to-nuc").
	MovedAway Why = "moved"
	// FailedMove is what a move that didn't finish copied to the server
	// it was moving to.
	FailedMove Why = "failed-move"
	// BeforeImport is a service's data from before data was brought in.
	BeforeImport Why = "before-import"
	// FailedImport is what was brought in, set aside when the switch to it
	// didn't work.
	FailedImport Why = "failed-import"
	// BeforePutBack is what a put back replaced.
	BeforePutBack Why = "before-put-back"
)

// Label is why, for people (the Owner's wording, 2026-10-23).
func (w Why) Label() string {
	switch w {
	case BeforeRestore:
		return "From before a restore"
	case FailedRestore:
		return "From a restore that didn't finish"
	case BeforeMove, MovedAway:
		return "From before a move"
	case FailedMove:
		return "From a move that didn't finish"
	case BeforeImport:
		return "From before bringing data in"
	case FailedImport:
		return "From bringing data in that didn't finish"
	case BeforePutBack:
		return "From before a put back"
	}
	return "Kept aside"
}

// Unfinished says the copy is what something that didn't finish left.
func (w Why) Unfinished() bool {
	return w == FailedRestore || w == FailedMove || w == FailedImport
}

// CanGoBack says whether a copy kept for this reason can be put back in
// place of its service's data (on the server it runs on): not what
// something that didn't finish left, nor the old copy of a service that
// moved away.
func (w Why) CanGoBack() bool { return !w.Unfinished() && w != MovedAway }

// TimeLayout is a kept folder's time.
const TimeLayout = "20060102-150405"

// Suffix is what a folder kept for why at t gets after its name. note
// says more for MovedAway: where it went ("to-nuc"), in lower-case
// letters, digits and dashes.
func Suffix(why Why, note string, t time.Time) string {
	if why == MovedAway {
		return "." + string(why) + "-" + note + "-" + t.Format(TimeLayout)
	}
	return "." + string(why) + "-" + t.Format(TimeLayout)
}

// name matches a kept folder's name: what it's a copy of, why (with a
// move's note) and when.
var name = regexp.MustCompile(`^(.+)\.(before-restore|failed-restore|before-move|failed-move|before-import|failed-import|before-put-back|moved-[a-z0-9-]*)-(\d{8}-\d{6})$`)

// Folder is one kept folder.
type Folder struct {
	// Path is the folder; Of is the folder it's a copy of.
	Path, Of string
	// Service is the service whose data it is: Of's name, for a whole
	// folder (see Copy).
	Service string
	Why     Why
	Note    string
	Time    time.Time
	Whole   bool
}

// Parse reads a kept folder's path; false if it isn't one.
func Parse(path string) (Folder, bool) {
	path = filepath.Clean(path)
	m := name.FindStringSubmatch(filepath.Base(path))
	if m == nil {
		return Folder{}, false
	}
	t, err := time.ParseInLocation(TimeLayout, m[3], time.Local)
	if err != nil {
		return Folder{}, false
	}
	f := Folder{Path: path, Of: filepath.Join(filepath.Dir(path), m[1]), Service: m[1], Why: Why(m[2]), Time: t}
	if note, ok := strings.CutPrefix(m[2], "moved-"); ok {
		f.Why, f.Note = MovedAway, note
	}
	return f, true
}

// Copy is the data set aside for one service at one moment, for one
// reason: one folder or more.
type Copy struct {
	Service string    `json:"service"`
	Why     Why       `json:"why"`
	Note    string    `json:"note,omitempty"`
	Time    time.Time `json:"time"`
	// Paths are its folders.
	Paths []string `json:"paths"`
	// Whole: its folders are the service's whole data folders (its
	// services and media folders), as a move, bringing data in, a put back
	// or a restore from another server sets them aside. Otherwise they're
	// folders inside them, set aside by a restore on the service's own
	// server, whose databases come back from the dumps among them.
	Whole bool `json:"whole"`
	// Size is how much space it takes, in bytes (-1 if unknown).
	Size int64 `json:"size"`
}

// Has says whether path is one of the copy's folders.
func (c Copy) Has(path string) bool { return slices.Contains(c.Paths, filepath.Clean(path)) }

// Find lists the copies in bases (the services and media folders: whole
// folders, named after their service) and, for each service in inside,
// those of the folders it lists (the ones its backups hold, which a
// restore on its own server sets aside one by one). Newest first; Size is
// -1.
func Find(bases []string, inside map[string][]string) ([]Copy, error) {
	var found []Folder
	for _, base := range bases {
		entries, err := os.ReadDir(base)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			f, ok := Parse(filepath.Join(base, e.Name()))
			if ok && e.IsDir() && !strings.Contains(f.Service, ".") {
				f.Whole = true
				found = append(found, f)
			}
		}
	}
	for svc, dirs := range inside {
		for _, dir := range dirs {
			matches, err := filepath.Glob(dir + ".*")
			if err != nil {
				return nil, err
			}
			for _, m := range matches {
				if f, ok := Parse(m); ok && f.Of == filepath.Clean(dir) {
					if st, err := os.Stat(m); err == nil && st.IsDir() {
						f.Service = svc
						found = append(found, f)
					}
				}
			}
		}
	}
	return Group(found), nil
}

// Group gathers kept folders into copies (those of one service set aside
// at one moment for one reason), newest first. Size is -1.
func Group(folders []Folder) []Copy {
	var out []Copy
	for _, f := range folders {
		c := Copy{Service: f.Service, Why: f.Why, Note: f.Note, Time: f.Time, Whole: f.Whole, Size: -1}
		i := slices.IndexFunc(out, func(o Copy) bool {
			return o.Service == c.Service && o.Why == c.Why && o.Note == c.Note && o.Time.Equal(c.Time) && o.Whole == c.Whole
		})
		if i < 0 {
			out = append(out, c)
			i = len(out) - 1
		}
		out[i].Paths = append(out[i].Paths, f.Path)
	}
	for i := range out {
		slices.Sort(out[i].Paths)
	}
	slices.SortStableFunc(out, func(a, b Copy) int {
		if c := b.Time.Compare(a.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Paths[0], b.Paths[0])
	})
	return out
}
