// Command maketakeout writes a synthetic Google Photos Takeout, never real
// photos, for testing Roostack's photo migration into Immich
// (docs/IMMICH-IMPORT.md §2 "Google Photos" and §13 "The test").
//
// It writes the zips as Google names them (takeout-<stamp>-001.zip, …),
// split at about -split bytes each, and a manifest.json saying exactly what
// was generated and what Immich should show afterwards, so a test can check
// the result against Immich's API. See ../README.md.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// takeoutStamp is the export time in the zips' names, fixed so the same
// seed always gives the same files.
const takeoutStamp = "20261019T091234Z"

// zipTime is every zip entry's modified time, for the same reason.
var zipTime = time.Date(2026, 10, 19, 9, 12, 34, 0, time.UTC)

// Google's name limits, as immich-go and GPTH document them: a media
// file's name without its extension keeps at most 47 characters, and a
// sidecar's whole name at most 51 including ".json".
const (
	maxMediaBase   = 47
	maxSidecarStem = 51 - len(".json")
)

// options are the command-line flags.
type options struct {
	out        string
	photos     int
	split      int64
	seed       uint64
	skipPart   int
	lang       string
	big        int
	videosFrom string
}

func main() {
	var o options
	var split string
	flag.StringVar(&o.out, "out", "", "folder to write the zips and manifest.json into (created if missing)")
	flag.IntVar(&o.photos, "photos", 300, "number of ordinary photos, besides the special cases")
	flag.StringVar(&split, "split", "5MB", "largest size of one zip, compressed and approximate: 500KB, 5MB, 2GB or bytes")
	flag.Uint64Var(&o.seed, "seed", 1, "the same seed and flags give the same bytes")
	flag.IntVar(&o.skipPart, "skip-part", 0, "leave part N out, to test the missing-part check (0 writes every part)")
	flag.StringVar(&o.lang, "lang", "en", "the Google account's language: en or de")
	flag.IntVar(&o.big, "big", 0, "extra small photos for a memory and size run, e.g. 20000")
	flag.StringVar(&o.videosFrom, "videos-from", "", "folder of .mp4 and .mov files to include (none by default)")
	flag.Parse()

	var err error
	if o.split, err = parseSize(split); err == nil {
		var m *Manifest
		if m, err = generate(o); err == nil {
			t := m.Totals
			fmt.Printf("Wrote %d parts to %s (%d photos, %d videos, %d edited, %d albums, %d files left out of the import)\n",
				len(m.Parts), o.out, t.Photos, t.Videos, t.Edited, t.Albums, t.NotImported)
			if m.SkippedPart > 0 {
				fmt.Printf("Part %d was left out on purpose (-skip-part).\n", m.SkippedPart)
			}
			return
		}
	}
	fmt.Fprintln(os.Stderr, "maketakeout:", err)
	os.Exit(1)
}

// parseSize reads a size such as "5MB", in decimal units like the rest of
// Roostack (internal/units).
func parseSize(s string) (int64, error) {
	t := strings.ToUpper(strings.TrimSpace(s))
	mult := 1.0
	for _, u := range []struct {
		suffix string
		mult   float64
	}{{"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3}, {"B", 1}} {
		if strings.HasSuffix(t, u.suffix) {
			t, mult = strings.TrimSpace(strings.TrimSuffix(t, u.suffix)), u.mult
			break
		}
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("split size %q: use bytes, or a number with KB, MB or GB", s)
	}
	return int64(n * mult), nil
}

// language holds what Takeout names differently in the account's
// language. Only the folder names, the album metadata file, the edited
// suffix and the formatted dates change; the sidecar suffix
// ".supplemental-metadata" stays English.
type language struct {
	root       string
	yearFolder string
	albumMeta  string
	edited     string
	untitled   string
	dateLayout string
}

var languages = map[string]language{
	"en": {"Google Photos", "Photos from %d", "metadata.json", "-edited", "Untitled", "2 Jan 2006, 15:04:05 UTC"},
	"de": {"Google Fotos", "Fotos von %d", "Metadaten.json", "-bearbeitet", "Ohne Titel", "02.01.2006, 15:04:05 UTC"},
}

// sidecarStyle is how a sidecar is named: "<name>.json" before 2024, and
// "<name>.supplemental-metadata.json" since.
type sidecarStyle int

const (
	styleOld sidecarStyle = iota
	styleSupplemental
)

// cut keeps the first n characters of s.
func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// counter is Google's mark on the nth duplicate name in a folder ("(1)").
func counter(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("(%d)", n)
}

// mediaName is the name Takeout gives a photo whose original name is
// title, when it's the nth of that name in its folder: the base is cut to
// 47 characters, and the counter goes before the extension
// (IMG_3479(1).JPG).
func mediaName(title string, n int) string {
	ext := path.Ext(title)
	return cut(strings.TrimSuffix(title, ext), maxMediaBase) + counter(n) + ext
}

// sidecarName is the name of that photo's sidecar: the original name, plus
// ".supplemental-metadata" in the current style, cut to 46 characters,
// then the counter, then ".json" (IMG_3479.JPG(1).json,
// IMG_3479.JPG.supplemental-metadata(1).json).
func sidecarName(title string, style sidecarStyle, n int) string {
	stem := title
	if style == styleSupplemental {
		stem += ".supplemental-metadata"
	}
	return cut(stem, maxSidecarStem) + counter(n) + ".json"
}

// Manifest is manifest.json: what was generated, and what Immich should
// show once it's imported.
type Manifest struct {
	Generator   string   `json:"generator"`
	Seed        uint64   `json:"seed"`
	Language    string   `json:"language"`
	Root        string   `json:"root"`
	SplitBytes  int64    `json:"splitBytes"`
	SkippedPart int      `json:"skippedPart,omitempty"`
	Parts       []*Part  `json:"parts"`
	Totals      Totals   `json:"totals"`
	Albums      []*Album `json:"albums"`
	Photos      []*Photo `json:"photos"`
	NotImported []*Extra `json:"notImported"`
}

// Part is one zip.
type Part struct {
	Number  int    `json:"number"`
	Name    string `json:"name"`
	Written bool   `json:"written"`
	Entries int    `json:"entries"`
	Bytes   int64  `json:"bytes"`
}

// FileRef is one file inside the Takeout, and the part it's in.
type FileRef struct {
	Path string `json:"path"`
	Part int    `json:"part"`
}

// Place is a position in decimal degrees.
type Place struct {
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	altitude  float64
}

// Album is one album folder.
type Album struct {
	Folder      string   `json:"folder"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Metadata    *FileRef `json:"metadata"`
	Photos      []int    `json:"photos"`
	created     time.Time
}

// Extra is a file in the Takeout that shouldn't become an asset in Immich.
type Extra struct {
	File   FileRef `json:"file"`
	Reason string  `json:"reason"`
	year   int
	data   []byte
}

// Totals counts the manifest, for a quick check against Immich's
// statistics.
type Totals struct {
	Parts            int `json:"parts"`
	Assets           int `json:"assets"`
	Photos           int `json:"photos"`
	Videos           int `json:"videos"`
	Edited           int `json:"edited"`
	LivePhotos       int `json:"livePhotos"`
	Big              int `json:"big"`
	Albums           int `json:"albums"`
	Favourites       int `json:"favourites"`
	Archived         int `json:"archived"`
	Trashed          int `json:"trashed"`
	Partner          int `json:"partner"`
	WithGPS          int `json:"withGps"`
	WithPeople       int `json:"withPeople"`
	WithDescription  int `json:"withDescription"`
	WithEXIF         int `json:"withExif"`
	EXIFDateDiffers  int `json:"exifDateDiffers"`
	NoSidecar        int `json:"noSidecar"`
	InSkippedPart    int `json:"inSkippedPart"`
	SplitAcrossParts int `json:"splitAcrossParts"`
	MediaFiles       int `json:"mediaFiles"`
	Sidecars         int `json:"sidecars"`
	NotImported      int `json:"notImported"`
}

// Photo is one asset Immich should have: a photo, video, edited copy or
// a Live Photo's video half. Album copies of it are more Files.
type Photo struct {
	ID               int        `json:"id"`
	Kind             string     `json:"kind"`
	Title            string     `json:"title"`
	Files            []*FileRef `json:"files"`
	Sidecars         []*FileRef `json:"sidecars"`
	SHA1             string     `json:"sha1"`
	Size             int        `json:"size"`
	DateTaken        string     `json:"dateTaken"`
	DateSource       string     `json:"dateSource"`
	EXIFDate         string     `json:"exifDate,omitempty"`
	EXIFDateDiffers  bool       `json:"exifDateDiffers,omitempty"`
	GPS              *Place     `json:"gps"`
	GPSSource        string     `json:"gpsSource,omitempty"`
	Description      string     `json:"description"`
	Favourite        bool       `json:"favourite"`
	Archived         bool       `json:"archived"`
	Trashed          bool       `json:"trashed"`
	Partner          bool       `json:"partner"`
	Albums           []string   `json:"albums"`
	People           []string   `json:"people"`
	EditedOf         int        `json:"editedOf,omitempty"`
	LivePhotoOf      int        `json:"livePhotoOf,omitempty"`
	NoSidecar        bool       `json:"noSidecar,omitempty"`
	Big              bool       `json:"big,omitempty"`
	InSkippedPart    bool       `json:"inSkippedPart,omitempty"`
	SplitAcrossParts bool       `json:"splitAcrossParts,omitempty"`

	year           int
	taken, created time.Time
	exifPlace      *Place
	geoData        *Place
	geoDataExif    *Place
	style          sidecarStyle
	views          int
	origin         any
	data, sidecar  []byte
	splitSidecar   bool // a part ends between this photo and its sidecar
	splitMedia     bool // a part ends just before this file
}

// hasSidecar says whether the photo has a sidecar of its own; edited
// copies and Live Photo video halves share another's.
func (p *Photo) hasSidecar() bool {
	return !p.NoSidecar && p.Kind != "edited" && p.Kind != "livePhotoVideo"
}

// places are where photos were taken. Each is snapped to what EXIF's
// degrees, minutes and hundredths of a second can hold, so a place read
// back from a file equals the one in its sidecar.
var places = []Place{
	{"Lisbon", 38.7077, -9.1365, 12.5},
	{"Sydney", -33.8568, 151.2153, 4},
	{"Munich", 48.1374, 11.5755, 519},
	{"Brisbane", -27.4698, 153.0251, 28},
	{"Reykjavik", 64.1466, -21.9426, 15},
	{"Kyoto", 35.0116, 135.7681, 50},
}

func init() {
	for i := range places {
		places[i].Latitude = fromDMS(toDMS(places[i].Latitude))
		places[i].Longitude = fromDMS(toDMS(places[i].Longitude))
	}
}

// dms is a coordinate as EXIF stores it: degrees, minutes, hundredths of
// a second, and whether it's south or west.
type dms struct {
	deg, min, centisec uint32
	negative           bool
}

func toDMS(v float64) dms {
	d := dms{negative: v < 0}
	cs := uint32(math.Round(math.Abs(v) * 3600 * 100))
	d.deg, cs = cs/360000, cs%360000
	d.min, d.centisec = cs/6000, cs%6000
	return d
}

func fromDMS(d dms) float64 {
	v := float64(d.deg) + float64(d.min)/60 + float64(d.centisec)/360000
	if d.negative {
		v = -v
	}
	return v
}

var descriptions = []string{
	"First day of the holidays",
	`Grandma's 80th — "the cake"`,
	"Sunset from the balcony",
	"Café on the corner, best pastel de nata",
	"Snow!",
	"The new garden bed, week 3",
}

// gen plans the Takeout, then lays it out as zip entries.
type gen struct {
	o      options
	lang   language
	root   string
	rng    *rand.Rand
	photos []*Photo
	albums []*Album
	extras []*Extra
	used   map[string]map[string]int
}

// generate writes the Takeout and its manifest into o.out.
func generate(o options) (*Manifest, error) {
	lang, ok := languages[o.lang]
	switch {
	case !ok:
		return nil, fmt.Errorf("language %q: use en or de", o.lang)
	case o.out == "":
		return nil, errors.New("say where to write it with -out")
	case o.photos < 0 || o.big < 0 || o.skipPart < 0:
		return nil, errors.New("-photos, -big and -skip-part can't be negative")
	}
	g := &gen{
		o: o, lang: lang, root: "Takeout/" + lang.root,
		rng:  rand.New(rand.NewPCG(o.seed, 0x7a6b0e7)),
		used: map[string]map[string]int{},
	}
	g.mainPhotos()
	g.specialPhotos()
	if err := g.videos(); err != nil {
		return nil, err
	}
	g.bigPhotos()
	g.makeAlbums()
	for _, p := range g.photos {
		if err := g.encode(p); err != nil {
			return nil, err
		}
	}

	if err := os.MkdirAll(o.out, 0o755); err != nil {
		return nil, err
	}
	// Parts from an earlier run would be mistaken for this one's.
	old, _ := filepath.Glob(filepath.Join(o.out, "takeout-"+takeoutStamp+"-*.zip"))
	for _, f := range old {
		if err := os.Remove(f); err != nil {
			return nil, err
		}
	}
	parts, err := writeParts(o.out, g.layout(), o.split, o.skipPart)
	if err != nil {
		return nil, err
	}
	if o.skipPart > len(parts) {
		return nil, fmt.Errorf("-skip-part %d: there are only %d parts", o.skipPart, len(parts))
	}
	m := g.manifest(parts)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return m, os.WriteFile(filepath.Join(o.out, "manifest.json"), append(b, '\n'), 0o644)
}

// newPhoto adds a photo taken at a random time in year.
func (g *gen) newPhoto(kind, title string, year int) *Photo {
	start := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	taken := start.Add(time.Duration(g.rng.Int64N(365*24*3600)) * time.Second)
	p := &Photo{
		ID: len(g.photos) + 1, Kind: kind, Title: title, year: year,
		taken:   taken,
		created: taken.Add(time.Duration(1+g.rng.Int64N(400*24)) * time.Hour),
		views:   g.rng.IntN(200),
		origin:  map[string]any{"mobileUpload": map[string]any{"deviceType": "ANDROID_PHONE"}},
		Albums:  []string{}, People: []string{},
		Files: []*FileRef{}, Sidecars: []*FileRef{},
		DateSource: "sidecar",
	}
	g.photos = append(g.photos, p)
	return p
}

// withEXIF gives a JPEG its own date (and place), as a camera would.
func (p *Photo) withEXIF(t time.Time, place *Place) {
	p.EXIFDate = t.Format("2006:01:02 15:04:05")
	p.EXIFDateDiffers = !t.Equal(p.taken)
	p.exifPlace = place
}

// mainPhotos makes the ordinary photos, with a mix of everything a sidecar
// can say, chosen by index so the mix is the same at any -photos.
func (g *gen) mainPhotos() {
	for i := range g.o.photos {
		year := 2014 + i%11
		png := i%10 == 9
		p := g.newPhoto("photo", "", year)
		switch {
		case png:
			p.Title = "Screenshot_" + p.taken.Format("20060102-150405") + ".png"
		case i%3 == 0:
			p.Title = fmt.Sprintf("IMG_%04d.JPG", 6000+i)
		case i%3 == 1:
			p.Title = fmt.Sprintf("PXL_%s%03d.jpg", p.taken.Format("20060102_150405"), g.rng.IntN(1000))
		default:
			p.Title = fmt.Sprintf("DSC%05d.JPG", 10000+i)
			p.origin = map[string]any{"webUpload": map[string]any{"computerUpload": map[string]any{}}}
		}
		p.style = sidecarStyle(i % 2)

		place := &places[g.rng.IntN(len(places))]
		if year == 2014 {
			place = &places[0]
		}
		hasEXIF := !png && i%4 < 2
		if hasEXIF {
			t := p.taken
			switch {
			case i%8 == 1:
				t = t.Add(7 * time.Hour) // the camera kept local time
			case i%24 == 5:
				t = t.AddDate(-3, 0, 2) // the camera's clock was wrong
			}
			var ep *Place
			if i%5 == 1 || i%5 == 3 {
				ep = place
			}
			p.withEXIF(t, ep)
		}
		switch i % 5 {
		case 1:
			p.geoData, p.GPS, p.GPSSource = place, place, "sidecar"
			if hasEXIF {
				p.geoDataExif, p.GPSSource = place, "exif and sidecar"
			}
		case 2:
			// A place added in Google Photos: only the sidecar has it.
			p.geoData, p.GPS, p.GPSSource = place, place, "sidecar"
		case 3:
			// geoData empty but the file has a place: Immich keeps the file's.
			if hasEXIF {
				p.geoDataExif, p.GPS, p.GPSSource = place, place, "exif"
			}
		}
		if i%6 == 0 {
			p.Description = descriptions[(i/6)%len(descriptions)]
		}
		switch {
		case i%7 == 2:
			p.People = []string{"Anna"}
		case i%7 == 4:
			p.People = []string{"Anna", "Ben"}
		case i%14 == 6:
			p.People = []string{"Chloé"}
		}
		p.Favourite = i%9 == 0
		p.Archived = i%13 == 5
		p.Trashed = i%17 == 3
		if i%19 == 7 {
			p.Partner = true
			p.origin = map[string]any{"fromPartnerSharing": map[string]any{}}
		}
	}
	if g.o.photos > 0 {
		g.photos[g.o.photos/2].splitSidecar = true
	}
}

// specialPhotos makes the traps of §2: duplicate names, long names,
// truncated sidecars, an edited copy, a photo with no sidecar, a Pixel
// motion photo, and files that aren't photos at all.
func (g *gen) specialPhotos() {
	// Two different photos with one name, in each sidecar style.
	for _, d := range []struct {
		title string
		year  int
		style sidecarStyle
	}{{"IMG_3479.JPG", 2016, styleOld}, {"IMG_3480.JPG", 2017, styleSupplemental}} {
		for range 2 {
			p := g.newPhoto("photo", d.title, d.year)
			p.style = d.style
			p.withEXIF(p.taken, nil)
		}
	}

	// A name over 47 characters, so the file and its sidecar are both cut.
	p := g.newPhoto("photo", "Grandmas_80th_birthday_party_in_the_garden_at_Brisbane_2018.jpg", 2018)
	p.style = styleSupplemental
	p.Description = descriptions[1]
	p.People = []string{"Anna", "Chloé"}
	p.geoData, p.GPS, p.GPSSource = &places[3], &places[3], "sidecar"
	if g.o.photos == 0 {
		p.splitSidecar = true
	}
	// Names whose sidecars are cut to ".supplemental-meta.json" and
	// ".supp.json", the same name in the old style.
	for _, t := range []string{"IMG_20180512_093015_HDR2.jpg", "PXL_20200815_142233456.NIGHT.PORTRAIT.jpg"} {
		p := g.newPhoto("photo", t, 2019)
		p.style = styleSupplemental
		p.withEXIF(p.taken, nil)
	}
	p = g.newPhoto("photo", "PXL_20200815_142233456.NIGHT.PORTRAIT.jpg", 2020)
	p.style = styleOld

	// An edited copy beside its original, sharing the original's sidecar.
	orig := g.newPhoto("photo", "IMG_2041.jpg", 2015)
	orig.style = styleOld
	orig.Favourite = true
	orig.withEXIF(orig.taken, &places[0])
	orig.geoData, orig.geoDataExif, orig.GPS, orig.GPSSource = &places[0], &places[0], &places[0], "exif and sidecar"
	ed := g.newPhoto("edited", "IMG_2041"+g.lang.edited+".jpg", 2015)
	ed.taken, ed.created, ed.EditedOf = orig.taken, orig.created, orig.ID
	ed.GPS, ed.GPSSource, ed.Favourite = orig.GPS, "original's sidecar", orig.Favourite
	ed.DateSource = "original's sidecar"

	// A photo whose sidecar is missing: only its own EXIF date.
	p = g.newPhoto("photo", "IMG_4242.JPG", 2020)
	p.NoSidecar, p.DateSource = true, "exif"
	p.withEXIF(p.taken, nil)

	// A Pixel motion photo: the JPEG is a photo, its ".MP" video part
	// (here just bytes) isn't.
	p = g.newPhoto("photo", "PXL_20210704_101112345.MP.jpg", 2021)
	p.style = styleSupplemental
	g.extra(2021, "PXL_20210704_101112345.MP", g.junk(4000, "\x00\x00\x00\x18ftypmp42"),
		"a Pixel motion photo's video part, not a valid video: left out, or refused")

	// Not a photo Immich can read, with a sidecar like any upload.
	psd := g.junk(6000, "8BPS\x00\x01")
	g.extra(2021, "Poster_draft.psd", psd, "not a real Photoshop file: Immich should refuse it or count it as failed")
	ps := &Photo{Title: "Poster_draft.psd", taken: time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC), style: styleSupplemental,
		origin: map[string]any{"webUpload": map[string]any{"computerUpload": map[string]any{}}}}
	ps.created = ps.taken
	g.extra(2021, sidecarName(ps.Title, styleSupplemental, 0), g.sidecarJSON(ps), "the sidecar of the .psd above")

	// A sidecar whose photo isn't in the Takeout.
	orphan := &Photo{Title: "IMG_9999.JPG", taken: time.Date(2019, 8, 9, 10, 11, 12, 0, time.UTC), style: styleSupplemental,
		origin: map[string]any{"mobileUpload": map[string]any{"deviceType": "ANDROID_PHONE"}}}
	orphan.created = orphan.taken
	g.extra(2019, sidecarName(orphan.Title, styleSupplemental, 0), g.sidecarJSON(orphan), "a sidecar with no photo")

	// Files Takeout puts beside the photos.
	g.extras = append(g.extras, &Extra{File: FileRef{Path: "Takeout/archive_browser.html"}, Reason: "Takeout's index page",
		data: []byte("<!DOCTYPE html>\n<html><head><meta charset=\"utf-8\"><title>Synthetic Takeout</title></head>\n<body><p>A synthetic Google Takeout made by maketakeout, for testing.</p></body></html>\n")})
	for _, n := range []string{"print-subscriptions.json", "shared_album_comments.json"} {
		g.extras = append(g.extras, &Extra{File: FileRef{Path: g.root + "/" + n}, Reason: "not a photo", data: []byte("[]\n")})
	}
}

// extra adds a file to a year folder that shouldn't become an asset.
func (g *gen) extra(year int, name string, data []byte, reason string) {
	g.extras = append(g.extras, &Extra{File: FileRef{Path: g.yearFolder(year) + "/" + name}, Reason: reason, year: year, data: data})
}

// junk is n random bytes after a file type's magic.
func (g *gen) junk(n int, magic string) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(g.rng.Uint32())
	}
	copy(b, magic)
	return b
}

// videos copies in the .mp4 and .mov files from -videos-from. The first
// .mov becomes the video half of a Live Photo, beside a JPEG of the same
// name and with no sidecar of its own.
func (g *gen) videos() error {
	if g.o.videosFrom == "" {
		return nil
	}
	ents, err := os.ReadDir(g.o.videosFrom)
	if err != nil {
		return err
	}
	live := false
	for _, e := range ents {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if e.IsDir() || (ext != ".mp4" && ext != ".mov") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(g.o.videosFrom, e.Name()))
		if err != nil {
			return err
		}
		if ext == ".mov" && !live {
			live = true
			still := g.newPhoto("photo", "IMG_5001.JPG", 2023)
			still.style = styleOld
			still.withEXIF(still.taken, nil)
			v := g.newPhoto("livePhotoVideo", "IMG_5001.MOV", 2023)
			v.taken, v.created, v.LivePhotoOf, v.DateSource = still.taken, still.created, still.ID, "the photo's sidecar"
			v.data, v.splitMedia = data, true
			continue
		}
		v := g.newPhoto("video", e.Name(), 2022)
		v.style = styleSupplemental
		v.data = data
	}
	return nil
}

// bigPhotos makes -big small photos with plain sidecars.
func (g *gen) bigPhotos() {
	for i := range g.o.big {
		p := g.newPhoto("photo", fmt.Sprintf("BIG_%06d.jpg", i), 2020+i%5)
		p.style, p.Big = sidecarStyle(i%2), true
	}
}

// makeAlbums puts copies of some photos into album folders, the special
// cases first so they're in albums at any -photos. Trashed and partner
// photos stay out, as they would in Google Photos.
func (g *gen) makeAlbums() {
	pick := func(limit int, want func(p *Photo) bool) []int {
		ids := []int{}
		for _, special := range []bool{true, false} {
			for _, p := range g.photos {
				isSpecial := p.ID > g.o.photos
				if len(ids) < limit && isSpecial == special && p.Kind == "photo" &&
					!p.Trashed && !p.Partner && !p.Big && !p.NoSidecar && want(p) {
					ids = append(ids, p.ID)
				}
			}
		}
		return ids
	}
	add := func(title, desc string, ids []int) {
		a := &Album{Folder: title, Title: title, Description: desc, Photos: ids,
			created: time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC).AddDate(0, 0, len(g.albums))}
		if title == "" {
			a.Folder = g.lang.untitled + "(2)"
		}
		for _, id := range ids {
			g.photos[id-1].Albums = append(g.photos[id-1].Albums, a.Folder)
		}
		g.albums = append(g.albums, a)
	}
	add("Lisbon 2014", "A week in Lisbon", pick(12, func(p *Photo) bool { return p.year == 2014 }))
	add("Family", "", pick(15, func(p *Photo) bool { return len(p.People) > 0 }))
	add("Garden", "Everything that grew", pick(12, func(p *Photo) bool {
		return p.ID%11 == 0 || p.Title == "IMG_3479.JPG"
	}))
	add("", "", pick(3, func(p *Photo) bool { return p.ID%23 == 1 }))
}

// encode makes the photo's bytes and its sidecar.
func (g *gen) encode(p *Photo) error {
	seed := g.o.seed*1_000_003 + uint64(p.ID)
	var buf bytes.Buffer
	switch ext := strings.ToLower(path.Ext(p.Title)); {
	case p.data != nil:
	case ext == ".png":
		if err := png.Encode(&buf, makeImage(seed, 240, 320, true)); err != nil {
			return err
		}
		p.data = buf.Bytes()
	default:
		w, h := 480, 360
		if p.Big {
			w, h = 64, 48
		}
		if p.Kind == "edited" {
			seed ^= 0xed17
		}
		if err := jpeg.Encode(&buf, makeImage(seed, w, h, false), &jpeg.Options{Quality: 85}); err != nil {
			return err
		}
		p.data = buf.Bytes()
		if p.EXIFDate != "" {
			p.data = withAPP1(p.data, exifSegment(p.EXIFDate, p.exifPlace))
		}
	}
	sum := sha1.Sum(p.data)
	p.SHA1, p.Size = hex.EncodeToString(sum[:]), len(p.data)
	p.DateTaken = p.taken.UTC().Format(time.RFC3339)
	if p.hasSidecar() {
		p.sidecar = g.sidecarJSON(p)
	}
	return nil
}

// makeImage draws a picture that's different for every seed: a noisy
// gradient like a photo, or flat blocks like a screenshot.
func makeImage(seed uint64, w, h int, flat bool) image.Image {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	rc := func() color.RGBA {
		return color.RGBA{uint8(r.IntN(256)), uint8(r.IntN(256)), uint8(r.IntN(256)), 255}
	}
	a, b := rc(), rc()
	if flat {
		for y := range h {
			for x := range w {
				img.SetRGBA(x, y, a)
			}
		}
		for range 6 {
			c := rc()
			x0, y0 := r.IntN(w), r.IntN(h)
			x1, y1 := x0+1+r.IntN(w-x0), y0+1+r.IntN(h-y0)
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					img.SetRGBA(x, y, c)
				}
			}
		}
		return img
	}
	mix := func(p, q uint8, t float64, n int) uint8 {
		v := float64(p)*(1-t) + float64(q)*t + float64(n)
		return uint8(max(0, min(255, v)))
	}
	for y := range h {
		for x := range w {
			t := float64(x+y) / float64(w+h)
			img.SetRGBA(x, y, color.RGBA{
				mix(a.R, b.R, t, r.IntN(33)-16), mix(a.G, b.G, t, r.IntN(33)-16), mix(a.B, b.B, t, r.IntN(33)-16), 255,
			})
		}
	}
	return img
}

// Google's sidecar, field for field.
type (
	gTime struct {
		Timestamp string `json:"timestamp"`
		Formatted string `json:"formatted"`
	}
	gGeo struct {
		Latitude      gFloat `json:"latitude"`
		Longitude     gFloat `json:"longitude"`
		Altitude      gFloat `json:"altitude"`
		LatitudeSpan  gFloat `json:"latitudeSpan"`
		LongitudeSpan gFloat `json:"longitudeSpan"`
	}
	gPerson struct {
		Name string `json:"name"`
	}
	gSidecar struct {
		Title              string    `json:"title"`
		Description        string    `json:"description"`
		ImageViews         string    `json:"imageViews"`
		CreationTime       gTime     `json:"creationTime"`
		PhotoTakenTime     gTime     `json:"photoTakenTime"`
		GeoData            gGeo      `json:"geoData"`
		GeoDataExif        gGeo      `json:"geoDataExif"`
		People             []gPerson `json:"people,omitempty"`
		URL                string    `json:"url"`
		GooglePhotosOrigin any       `json:"googlePhotosOrigin"`
		Favorited          bool      `json:"favorited,omitempty"`
		Archived           bool      `json:"archived,omitempty"`
		Trashed            bool      `json:"trashed,omitempty"`
	}
	gAlbum struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Access      string `json:"access"`
		Date        gTime  `json:"date"`
	}
)

// gFloat is written as Google writes numbers: always with a decimal point
// (0.0, not 0).
type gFloat float64

func (f gFloat) MarshalJSON() ([]byte, error) {
	s := strconv.FormatFloat(float64(f), 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return []byte(s), nil
}

func (g *gen) gtime(t time.Time) gTime {
	return gTime{strconv.FormatInt(t.Unix(), 10), t.UTC().Format(g.lang.dateLayout)}
}

func ggeo(p *Place) gGeo {
	if p == nil {
		return gGeo{}
	}
	return gGeo{Latitude: gFloat(p.Latitude), Longitude: gFloat(p.Longitude), Altitude: gFloat(p.altitude)}
}

// marshal writes JSON as Google does: two-space indents, no HTML escapes.
func marshal(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(err) // only fixed types are marshalled
	}
	return buf.Bytes()
}

// sidecarJSON is the photo's sidecar.
func (g *gen) sidecarJSON(p *Photo) []byte {
	s := gSidecar{
		Title: p.Title, Description: p.Description, ImageViews: strconv.Itoa(p.views),
		CreationTime: g.gtime(p.created), PhotoTakenTime: g.gtime(p.taken),
		GeoData: ggeo(p.geoData), GeoDataExif: ggeo(p.geoDataExif),
		URL:                fmt.Sprintf("https://photos.google.com/photo/AF1QipSynthetic%08x%06d", g.o.seed, p.ID),
		GooglePhotosOrigin: p.origin,
		Favorited:          p.Favourite, Archived: p.Archived, Trashed: p.Trashed,
	}
	for _, n := range p.People {
		s.People = append(s.People, gPerson{n})
	}
	return marshal(s)
}

// EXIF, written by hand: a little-endian TIFF block with IFD0 pointing at
// an Exif IFD (the dates) and, for some photos, a GPS IFD.
const (
	tByte      = 1
	tASCII     = 2
	tLong      = 4
	tRational  = 5
	tUndefined = 7
)

type ifdEntry struct {
	tag, typ uint16
	count    uint32
	value    []byte
}

func ascii(tag uint16, s string) ifdEntry {
	return ifdEntry{tag, tASCII, uint32(len(s) + 1), append([]byte(s), 0)}
}

func long(tag uint16, v uint32) ifdEntry {
	return ifdEntry{tag, tLong, 1, binary.LittleEndian.AppendUint32(nil, v)}
}

func rationals(tag uint16, pairs ...uint32) ifdEntry {
	var b []byte
	for _, v := range pairs {
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return ifdEntry{tag, tRational, uint32(len(pairs) / 2), b}
}

// ifdSize is how many bytes an IFD and its out-of-line values take.
func ifdSize(es []ifdEntry) uint32 {
	n := uint32(2 + 12*len(es) + 4)
	for _, e := range es {
		if l := uint32(len(e.value)); l > 4 {
			n += l + l%2
		}
	}
	return n
}

// ifdBytes writes an IFD that starts at offset base in the TIFF block.
func ifdBytes(es []ifdEntry, base uint32) []byte {
	slices.SortFunc(es, func(a, b ifdEntry) int { return int(a.tag) - int(b.tag) })
	le := binary.LittleEndian
	out := le.AppendUint16(nil, uint16(len(es)))
	var data []byte
	dataAt := base + uint32(2+12*len(es)+4)
	for _, e := range es {
		out = le.AppendUint16(out, e.tag)
		out = le.AppendUint16(out, e.typ)
		out = le.AppendUint32(out, e.count)
		if len(e.value) <= 4 {
			v := make([]byte, 4)
			copy(v, e.value)
			out = append(out, v...)
			continue
		}
		out = le.AppendUint32(out, dataAt+uint32(len(data)))
		data = append(data, e.value...)
		if len(data)%2 == 1 {
			data = append(data, 0)
		}
	}
	out = le.AppendUint32(out, 0) // no next IFD
	return append(out, data...)
}

// exifSegment is a JPEG APP1 segment with the date taken
// ("2006:01:02 15:04:05", no time zone, as most cameras write it) and,
// if place isn't nil, its position.
func exifSegment(date string, place *Place) []byte {
	ifd0 := []ifdEntry{ascii(0x010f, "Roostack test"), ascii(0x0110, "maketakeout"), long(0x8769, 0)}
	exif := []ifdEntry{
		{0x9000, tUndefined, 4, []byte("0232")},
		ascii(0x9003, date), // DateTimeOriginal
		ascii(0x9004, date), // DateTimeDigitized
	}
	var gps []ifdEntry
	if place != nil {
		ifd0 = append(ifd0, long(0x8825, 0))
		lat, lon := toDMS(place.Latitude), toDMS(place.Longitude)
		latRef, lonRef := "N", "E"
		if lat.negative {
			latRef = "S"
		}
		if lon.negative {
			lonRef = "W"
		}
		gps = []ifdEntry{
			{0x0000, tByte, 4, []byte{2, 3, 0, 0}},
			ascii(0x0001, latRef),
			rationals(0x0002, lat.deg, 1, lat.min, 1, lat.centisec, 100),
			ascii(0x0003, lonRef),
			rationals(0x0004, lon.deg, 1, lon.min, 1, lon.centisec, 100),
		}
	}
	exifAt := 8 + ifdSize(ifd0)
	gpsAt := exifAt + ifdSize(exif)
	for i := range ifd0 {
		switch ifd0[i].tag {
		case 0x8769:
			ifd0[i] = long(0x8769, exifAt)
		case 0x8825:
			ifd0[i] = long(0x8825, gpsAt)
		}
	}
	tiff := []byte("II*\x00\x08\x00\x00\x00")
	tiff = append(tiff, ifdBytes(ifd0, 8)...)
	tiff = append(tiff, ifdBytes(exif, exifAt)...)
	if gps != nil {
		tiff = append(tiff, ifdBytes(gps, gpsAt)...)
	}
	seg := []byte{0xff, 0xe1}
	seg = binary.BigEndian.AppendUint16(seg, uint16(2+6+len(tiff)))
	seg = append(seg, "Exif\x00\x00"...)
	return append(seg, tiff...)
}

// withAPP1 puts a segment straight after a JPEG's start-of-image marker.
func withAPP1(jpg, seg []byte) []byte {
	out := append([]byte{}, jpg[:2]...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// yearFolder is the folder of a year's photos.
func (g *gen) yearFolder(year int) string {
	return g.root + "/" + fmt.Sprintf(g.lang.yearFolder, year)
}

// alloc counts a name in a folder: 0 the first time, then 1, 2… for the
// "(1)" Google adds to duplicates.
func (g *gen) alloc(folder, title string) int {
	if g.used[folder] == nil {
		g.used[folder] = map[string]int{}
	}
	n := g.used[folder][title]
	g.used[folder][title]++
	return n
}

// entry is one file to write into the zips.
type entry struct {
	path        string
	data        []byte
	ref         *FileRef
	breakBefore bool
}

// layout orders the files as a Takeout does: the index, then the year
// folders (each photo followed by its sidecar), then the album folders.
func (g *gen) layout() []entry {
	var es []entry
	for _, x := range g.extras {
		if x.year == 0 {
			es = append(es, entry{x.File.Path, x.data, &x.File, false})
		}
	}
	var years []int
	for _, p := range g.photos {
		years = append(years, p.year)
	}
	for _, x := range g.extras {
		if x.year != 0 {
			years = append(years, x.year)
		}
	}
	slices.Sort(years)
	for _, y := range slices.Compact(years) {
		folder := g.yearFolder(y)
		for _, p := range g.photos {
			if p.year == y {
				es = append(es, g.place(folder, p, p.splitMedia, p.splitSidecar)...)
			}
		}
		for _, x := range g.extras {
			if x.year == y {
				es = append(es, entry{x.File.Path, x.data, &x.File, false})
			}
		}
	}
	for _, a := range g.albums {
		folder := g.root + "/" + a.Folder
		a.Metadata = &FileRef{Path: folder + "/" + g.lang.albumMeta}
		meta := marshal(gAlbum{a.Title, a.Description, "protected", g.gtime(a.created)})
		es = append(es, entry{a.Metadata.Path, meta, a.Metadata, false})
		for _, id := range a.Photos {
			es = append(es, g.place(folder, g.photos[id-1], false, false)...)
		}
	}
	return es
}

// place names a photo (and its sidecar) in a folder.
func (g *gen) place(folder string, p *Photo, breakMedia, breakSidecar bool) []entry {
	n := g.alloc(folder, p.Title)
	f := &FileRef{Path: folder + "/" + mediaName(p.Title, n)}
	p.Files = append(p.Files, f)
	es := []entry{{f.Path, p.data, f, breakMedia}}
	if p.hasSidecar() {
		s := &FileRef{Path: folder + "/" + sidecarName(p.Title, p.style, n)}
		p.Sidecars = append(p.Sidecars, s)
		es = append(es, entry{s.Path, p.sidecar, s, breakSidecar})
	}
	return es
}

// countWriter counts what's written through it.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n += int64(n)
	return n, err
}

// partName is Google's name for part n.
func partName(n int) string {
	return fmt.Sprintf("takeout-%s-%03d.zip", takeoutStamp, n)
}

// writeParts writes the entries into zips of about split bytes each,
// starting a new one early where an entry asks to (so a photo and its
// sidecar land in different parts). Part skip is laid out but not written.
func writeParts(dir string, es []entry, split int64, skip int) (parts []*Part, err error) {
	var (
		f  *os.File
		cw *countWriter
		zw *zip.Writer
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	closePart := func() error {
		if err := zw.Close(); err != nil {
			return err
		}
		parts[len(parts)-1].Bytes = cw.n
		if f != nil {
			err := f.Close()
			f = nil
			return err
		}
		return nil
	}
	openPart := func() error {
		p := &Part{Number: len(parts) + 1, Written: len(parts)+1 != skip}
		p.Name = partName(p.Number)
		parts = append(parts, p)
		var w io.Writer = io.Discard
		if p.Written {
			var err error
			if f, err = os.Create(filepath.Join(dir, p.Name)); err != nil {
				return err
			}
			w = f
		}
		cw = &countWriter{w: w}
		zw = zip.NewWriter(cw)
		return nil
	}
	// The zip writer holds back the end of the last entry until the next
	// starts, and writes the central directory (a header per entry) only
	// at the end, so count both as already written: a part may come out a
	// little small, but not over split.
	const overhead = 128 // an entry's local and central headers, roughly
	var pending, directory int64
	for _, e := range es {
		if zw == nil {
			if err := openPart(); err != nil {
				return nil, err
			}
		} else {
			if err := zw.Flush(); err != nil {
				return nil, err
			}
			cur := parts[len(parts)-1]
			if cur.Entries > 0 && (e.breakBefore || cw.n+pending+directory+int64(len(e.data)+len(e.path)*2+overhead) > split) {
				if err := closePart(); err != nil {
					return nil, err
				}
				if err := openPart(); err != nil {
					return nil, err
				}
				directory = 0
			}
		}
		w, err := zw.CreateHeader(&zip.FileHeader{Name: e.path, Method: zip.Deflate, Modified: zipTime})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(e.data); err != nil {
			return nil, err
		}
		e.ref.Part = len(parts)
		pending = int64(len(e.data))
		directory += int64(len(e.path) + overhead/2)
		parts[len(parts)-1].Entries++
	}
	if zw != nil {
		if err := closePart(); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// manifest fills in what's known once the parts are written, and counts.
func (g *gen) manifest(parts []*Part) *Manifest {
	m := &Manifest{
		Generator: "maketakeout", Seed: g.o.seed, Language: g.o.lang, Root: g.root,
		SplitBytes: g.o.split, SkippedPart: g.o.skipPart, Parts: parts,
		Albums: g.albums, Photos: g.photos, NotImported: g.extras,
	}
	t := &m.Totals
	t.Parts, t.Albums, t.NotImported = len(parts), len(g.albums), len(g.extras)
	byID := func(id int) *Photo { return g.photos[id-1] }
	for _, p := range g.photos {
		for i, f := range p.Files {
			if f.Part == g.o.skipPart || (i < len(p.Sidecars) && p.Sidecars[i].Part == g.o.skipPart) {
				p.InSkippedPart = true
			}
			if i < len(p.Sidecars) && p.Sidecars[i].Part != f.Part {
				p.SplitAcrossParts = true
			}
		}
		if p.LivePhotoOf != 0 && byID(p.LivePhotoOf).Files[0].Part != p.Files[0].Part {
			p.SplitAcrossParts = true
		}
		t.Assets++
		switch p.Kind {
		case "photo":
			t.Photos++
		case "video":
			t.Videos++
		case "edited":
			t.Edited++
		case "livePhotoVideo":
			t.LivePhotos++
		}
		for _, c := range []struct {
			on bool
			n  *int
		}{
			{p.Big, &t.Big}, {p.Favourite, &t.Favourites}, {p.Archived, &t.Archived}, {p.Trashed, &t.Trashed},
			{p.Partner, &t.Partner}, {p.GPS != nil, &t.WithGPS}, {len(p.People) > 0, &t.WithPeople},
			{p.Description != "", &t.WithDescription}, {p.EXIFDate != "", &t.WithEXIF},
			{p.EXIFDateDiffers, &t.EXIFDateDiffers}, {p.NoSidecar, &t.NoSidecar},
			{p.InSkippedPart, &t.InSkippedPart}, {p.SplitAcrossParts, &t.SplitAcrossParts},
		} {
			if c.on {
				*c.n++
			}
		}
		t.MediaFiles += len(p.Files)
		t.Sidecars += len(p.Sidecars)
	}
	return m
}
