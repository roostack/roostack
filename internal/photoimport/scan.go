// Package photoimport migrates photos into Immich from other platforms
// (SPEC.md §8.5, docs/IMMICH-IMPORT.md): Scan has a quick look at an
// export before anything is sent, Command is immich-go's command line for
// the person's choices, and Tally reads immich-go's log as it goes. The
// node runs it (node/photos.go); Roostack counts for itself rather than
// trusting the tool's own summary, which can be wrong (immich-go #1384).
package photoimport

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Kinds of export.
const (
	KindGoogle = "google" // a Google Takeout: zip parts with JSON sidecars
	KindFolder = "folder" // a folder of photos, with XMP sidecars if any
)

// Kinds says which kinds there are, in the order the page offers them.
var Kinds = []string{KindGoogle, KindFolder}

// KindTitle names a kind for people.
func KindTitle(kind string) string {
	switch kind {
	case KindGoogle:
		return "Google Photos (Takeout)"
	case KindFolder:
		return "A folder of photos"
	}
	return kind
}

// media is what immich-go reads as photos and videos (its
// internal/filetypes at v0.32.0), so Roostack's count matches what it
// sends. ".mp" (a Pixel's motion file) it skips as useless.
var media = map[string]string{
	".3gp": "video", ".avi": "video", ".flv": "video", ".insv": "video", ".m2ts": "video", ".m4v": "video", ".mkv": "video",
	".mov": "video", ".mp4": "video", ".mpg": "video", ".mts": "video", ".webm": "video", ".wmv": "video",
	".3fr": "photo", ".ari": "photo", ".arw": "photo", ".avif": "photo", ".bmp": "photo", ".cap": "photo", ".cin": "photo",
	".cr2": "photo", ".cr3": "photo", ".crw": "photo", ".dcr": "photo", ".dng": "photo", ".erf": "photo", ".fff": "photo",
	".gif": "photo", ".heic": "photo", ".heif": "photo", ".hif": "photo", ".iiq": "photo", ".insp": "photo", ".jpe": "photo",
	".jpeg": "photo", ".jpg": "photo", ".jxl": "photo", ".k25": "photo", ".kdc": "photo", ".mrw": "photo", ".nef": "photo",
	".orf": "photo", ".ori": "photo", ".pef": "photo", ".png": "photo", ".psd": "photo", ".raf": "photo", ".raw": "photo",
	".rw2": "photo", ".rwl": "photo", ".sr2": "photo", ".srf": "photo", ".srw": "photo", ".tif": "photo", ".tiff": "photo",
	".webp": "photo", ".x3f": "photo",
}

// mediaKind is "photo", "video" or "" for a file name.
func mediaKind(name string) string { return media[strings.ToLower(path.Ext(name))] }

// partName is a Takeout part's file name: takeout-<when>-<number>.zip,
// with a series number between them in newer exports
// (takeout-20261019T091234Z-3-001.zip).
var partName = regexp.MustCompile(`(?i)^takeout-(\d{8}T\d{6}Z)(?:-(\d+))?-(\d{3,})\.(zip|tgz)$`)

// Part is one of an export's archives.
type Part struct {
	Name   string `json:"name"`
	Number int    `json:"number"`
	Bytes  int64  `json:"bytes"`
}

// Scan is what an export holds, from a quick look: for a Takeout, each
// zip's directory and the small sidecars, never the photos themselves.
type Scan struct {
	Kind  string `json:"kind"`
	Parts []Part `json:"parts,omitempty"`
	// Missing are part numbers absent between the first and the last.
	// A missing last part can't be seen: Google's names don't say how
	// many there are.
	Missing []int `json:"missing,omitempty"`
	// Exports counts Takeouts found (by their time); more than one in a
	// folder trips immich-go (#1473), so the check says so.
	Exports int `json:"exports,omitempty"`
	// Tgz are .tgz parts, which immich-go can't read.
	Tgz []string `json:"tgz,omitempty"`
	// Unreadable are zips that couldn't be opened (cut short in the
	// download, say).
	Unreadable []string `json:"unreadable,omitempty"`

	Photos int64 `json:"photos"`
	Videos int64 `json:"videos"`
	// Bytes is the photos' and videos' size, unpacked: what Immich keeps.
	Bytes int64 `json:"bytes"`
	// First and Last are when the earliest and latest were taken.
	First time.Time `json:"first,omitzero"`
	Last  time.Time `json:"last,omitzero"`

	Albums   int `json:"albums,omitempty"`
	Untitled int `json:"untitled,omitempty"`
	// From the sidecars.
	Trashed    int64 `json:"trashed,omitempty"`
	Partner    int64 `json:"partner,omitempty"`
	Archived   int64 `json:"archived,omitempty"`
	Favourites int64 `json:"favourites,omitempty"`
	// Other are files that aren't photos, videos or sidecars (a .MP
	// motion file, a document), with a few names.
	Other         int64    `json:"other,omitempty"`
	OtherExamples []string `json:"other_examples,omitempty"`
}

// Media is all the photos and videos.
func (s *Scan) Media() int64 { return s.Photos + s.Videos }

// ScanFS has a quick look at an export in fsys (the person's folder,
// mounted read-only).
func ScanFS(fsys fs.FS, kind string) (*Scan, error) {
	switch kind {
	case KindGoogle:
		return scanTakeout(fsys)
	case KindFolder:
		return scanFolder(fsys)
	}
	return nil, fmt.Errorf("no kind of export called %q", kind)
}

// sidecar is what Roostack reads from a Takeout's JSON sidecar.
type sidecar struct {
	Title          string `json:"title"`
	PhotoTakenTime *struct {
		Timestamp string `json:"timestamp"`
	} `json:"photoTakenTime"`
	Trashed            bool            `json:"trashed"`
	Archived           bool            `json:"archived"`
	Favorited          bool            `json:"favorited"`
	GooglePhotosOrigin json.RawMessage `json:"googlePhotosOrigin"`
	// An album's metadata has a title and no photoTakenTime.
	Date *struct {
		Timestamp string `json:"timestamp"`
	} `json:"date"`
}

// maxSidecar is the largest JSON file read as a sidecar: Google's are a
// few kB; anything bigger isn't one.
const maxSidecar = 256 << 10

// scanTakeout reads every Takeout part in the folder (not below it: the
// guide says to put them all in one folder).
func scanTakeout(fsys fs.FS) (*Scan, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	s := &Scan{Kind: KindGoogle}
	exports := map[string]bool{}
	var other []string
	for _, e := range entries {
		name := e.Name()
		lower := strings.ToLower(name)
		if e.IsDir() || !(strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".tgz") || strings.HasSuffix(lower, ".tar.gz")) {
			continue
		}
		if !strings.HasSuffix(lower, ".zip") {
			s.Tgz = append(s.Tgz, name)
			continue
		}
		info, err := e.Info()
		if err != nil {
			s.Unreadable = append(s.Unreadable, name)
			continue
		}
		p := Part{Name: name, Bytes: info.Size()}
		if m := partName.FindStringSubmatch(name); m != nil {
			p.Number, _ = strconv.Atoi(m[3])
			exports[m[1]+"-"+m[2]] = true
		}
		s.Parts = append(s.Parts, p)
	}
	sort.Slice(s.Parts, func(i, j int) bool {
		if s.Parts[i].Number != s.Parts[j].Number {
			return s.Parts[i].Number < s.Parts[j].Number
		}
		return s.Parts[i].Name < s.Parts[j].Name
	})
	s.Exports = len(exports)
	s.Missing = missingParts(s.Parts)

	seen := map[string]bool{}  // media by name, size and checksum: albums hold copies
	jsons := map[string]bool{} // sidecars the same way
	albums := map[string]bool{}
	for _, p := range s.Parts {
		if err := scanZip(fsys, p.Name, s, seen, jsons, albums, &other); err != nil {
			s.Unreadable = append(s.Unreadable, p.Name)
		}
	}
	s.OtherExamples = firstFew(other)
	return s, nil
}

// missingParts are the numbers absent between the lowest and highest.
func missingParts(parts []Part) []int {
	have := map[int]bool{}
	hi := 0
	for _, p := range parts {
		if p.Number > 0 {
			have[p.Number] = true
			hi = max(hi, p.Number)
		}
	}
	var missing []int
	for n := 1; n <= hi; n++ {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	return missing
}

// readerAt is a file that can be read from anywhere, as zip needs.
type readerAt interface {
	io.ReaderAt
	Stat() (fs.FileInfo, error)
}

// scanZip reads one part's directory, and its JSON files.
func scanZip(fsys fs.FS, name string, s *Scan, seen, jsons, albums map[string]bool, other *[]string) error {
	f, err := fsys.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	ra, ok := f.(readerAt)
	if !ok {
		return errors.New("can't read it from the middle")
	}
	info, err := ra.Stat()
	if err != nil {
		return err
	}
	z, err := zip.NewReader(ra, info.Size())
	if err != nil {
		return err
	}
	for _, zf := range z.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		base := path.Base(zf.Name)
		key := fmt.Sprintf("%s|%d|%08x", base, zf.UncompressedSize64, zf.CRC32)
		switch {
		case mediaKind(base) == "photo" || mediaKind(base) == "video":
			if seen[key] {
				continue
			}
			seen[key] = true
			if mediaKind(base) == "video" {
				s.Videos++
			} else {
				s.Photos++
			}
			s.Bytes += int64(zf.UncompressedSize64)
		case strings.EqualFold(path.Ext(base), ".json"):
			if zf.UncompressedSize64 > maxSidecar {
				continue
			}
			readSidecar(zf, key, s, jsons, albums)
		case isBanned(base):
		default:
			s.Other++
			*other = append(*other, zf.Name)
		}
	}
	return nil
}

// readSidecar counts what a sidecar says, once per sidecar (albums hold
// copies of them too), and an album's metadata once per album folder.
func readSidecar(zf *zip.File, key string, s *Scan, jsons, albums map[string]bool) {
	rc, err := zf.Open()
	if err != nil {
		return
	}
	defer rc.Close()
	var sc sidecar
	if err := json.NewDecoder(io.LimitReader(rc, maxSidecar)).Decode(&sc); err != nil {
		return
	}
	if sc.PhotoTakenTime == nil {
		// An album's metadata (its name differs by language).
		dir := path.Dir(zf.Name)
		if sc.Date != nil || sc.Title != "" || strings.HasPrefix(strings.ToLower(path.Base(zf.Name)), "metad") {
			if !albums[dir] {
				albums[dir] = true
				s.Albums++
				if strings.TrimSpace(sc.Title) == "" {
					s.Untitled++
				}
			}
		}
		return
	}
	if jsons[key] {
		return
	}
	jsons[key] = true
	if sec, err := strconv.ParseInt(sc.PhotoTakenTime.Timestamp, 10, 64); err == nil && sec > 0 {
		t := time.Unix(sec, 0).UTC()
		if s.First.IsZero() || t.Before(s.First) {
			s.First = t
		}
		if t.After(s.Last) {
			s.Last = t
		}
	}
	if sc.Trashed {
		s.Trashed++
	}
	if sc.Archived {
		s.Archived++
	}
	if sc.Favorited {
		s.Favourites++
	}
	if strings.Contains(string(sc.GooglePhotosOrigin), "fromPartnerSharing") {
		s.Partner++
	}
}

// isBanned is a file immich-go leaves out without a word (its default ban
// list): a system's clutter, not the person's.
func isBanned(name string) bool {
	lower := strings.ToLower(name)
	return lower == ".ds_store" || lower == "thumbs.db" || lower == "desktop.ini" || strings.HasPrefix(lower, "._") ||
		lower == "archive_browser.html" || lower == "print-subscriptions.json" || lower == "shared_album_comments.json" ||
		lower == "user-generated-memory-titles.json"
}

// maxWalk stops a folder's look after this many entries: it's a quick
// look, and the dry run counts the rest.
const maxWalk = 2_000_000

// scanFolder counts a folder of photos, all the way down, skipping the
// clutter of NASes and computers (@eaDir, .DS_Store).
func scanFolder(fsys fs.FS) (*Scan, error) {
	s := &Scan{Kind: KindFolder}
	var other []string
	n := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == "." {
				return err
			}
			return nil // a folder Roostack can't read: the dry run says which
		}
		if n++; n > maxWalk {
			return fs.SkipAll
		}
		base := d.Name()
		if d.IsDir() {
			if p != "." && (base == "@eaDir" || base == "#recycle" || strings.HasPrefix(base, ".")) {
				return fs.SkipDir
			}
			return nil
		}
		switch kind := mediaKind(base); {
		case kind != "":
			if kind == "video" {
				s.Videos++
			} else {
				s.Photos++
			}
			if info, err := d.Info(); err == nil {
				s.Bytes += info.Size()
			}
		case strings.EqualFold(path.Ext(base), ".xmp") || strings.EqualFold(path.Ext(base), ".json") || isBanned(base):
		default:
			s.Other++
			other = append(other, p)
		}
		return nil
	})
	s.OtherExamples = firstFew(other)
	return s, err
}

// firstFew keeps a few names, sorted, for the page.
func firstFew(names []string) []string {
	slices.Sort(names)
	if len(names) > 5 {
		names = names[:5]
	}
	return names
}
