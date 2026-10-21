package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestNames checks Google's naming rules: the 47-character media name, the
// 51-character sidecar name, and where the "(1)" goes.
func TestNames(t *testing.T) {
	long := "Grandmas_80th_birthday_party_in_the_garden_at_Brisbane_2018.jpg"
	for _, c := range []struct {
		title   string
		style   sidecarStyle
		n       int
		media   string
		sidecar string
	}{
		{"IMG_3479.JPG", styleOld, 0, "IMG_3479.JPG", "IMG_3479.JPG.json"},
		{"IMG_3479.JPG", styleOld, 1, "IMG_3479(1).JPG", "IMG_3479.JPG(1).json"},
		{"IMG_3479.JPG", styleSupplemental, 0, "IMG_3479.JPG", "IMG_3479.JPG.supplemental-metadata.json"},
		{"IMG_3479.JPG", styleSupplemental, 1, "IMG_3479(1).JPG", "IMG_3479.JPG.supplemental-metadata(1).json"},
		{"IMG_3479.JPG", styleSupplemental, 2, "IMG_3479(2).JPG", "IMG_3479.JPG.supplemental-metadata(2).json"},
		{"IMG_20180512_093015_HDR2.jpg", styleSupplemental, 0, "IMG_20180512_093015_HDR2.jpg",
			"IMG_20180512_093015_HDR2.jpg.supplemental-meta.json"},
		{"PXL_20200815_142233456.NIGHT.PORTRAIT.jpg", styleSupplemental, 0, "PXL_20200815_142233456.NIGHT.PORTRAIT.jpg",
			"PXL_20200815_142233456.NIGHT.PORTRAIT.jpg.supp.json"},
		{"PXL_20200815_142233456.NIGHT.PORTRAIT.jpg", styleSupplemental, 1, "PXL_20200815_142233456.NIGHT.PORTRAIT(1).jpg",
			"PXL_20200815_142233456.NIGHT.PORTRAIT.jpg.supp(1).json"},
		{"PXL_20200815_142233456.NIGHT.PORTRAIT.jpg", styleOld, 0, "PXL_20200815_142233456.NIGHT.PORTRAIT.jpg",
			"PXL_20200815_142233456.NIGHT.PORTRAIT.jpg.json"},
		{long, styleOld, 0, "Grandmas_80th_birthday_party_in_the_garden_at_B.jpg",
			"Grandmas_80th_birthday_party_in_the_garden_at_.json"},
		{long, styleSupplemental, 0, "Grandmas_80th_birthday_party_in_the_garden_at_B.jpg",
			"Grandmas_80th_birthday_party_in_the_garden_at_.json"},
		{long, styleOld, 1, "Grandmas_80th_birthday_party_in_the_garden_at_B(1).jpg",
			"Grandmas_80th_birthday_party_in_the_garden_at_(1).json"},
		{"IMG_2041-edited.jpg", styleOld, 0, "IMG_2041-edited.jpg", "IMG_2041-edited.jpg.json"},
		{"Chloé_à_la_plage_avec_toute_la_famille_en_août_2019.jpg", styleOld, 0,
			"Chloé_à_la_plage_avec_toute_la_famille_en_août_.jpg", "Chloé_à_la_plage_avec_toute_la_famille_en_août.json"},
	} {
		if got := mediaName(c.title, c.n); got != c.media {
			t.Errorf("mediaName(%q, %d) = %q, want %q", c.title, c.n, got, c.media)
		}
		if got := sidecarName(c.title, c.style, c.n); got != c.sidecar {
			t.Errorf("sidecarName(%q, %d, %d) = %q, want %q", c.title, c.style, c.n, got, c.sidecar)
		}
		if got := sidecarName(c.title, c.style, 0); utf8.RuneCountInString(got) > 51 {
			t.Errorf("sidecarName(%q) = %q, over 51 characters", c.title, got)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"5MB": 5e6, "500KB": 5e5, "2GB": 2e9, "1.5mb": 15e5, "12345": 12345, "7 MB": 7e6} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "MB", "-3MB", "lots"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) succeeded", in)
		}
	}
}

// readZips opens every part with archive/zip and returns each file's bytes
// by path, and the part it was in.
func readZips(t *testing.T, dir string, parts []*Part) (map[string][]byte, map[string]int) {
	t.Helper()
	files, where := map[string][]byte{}, map[string]int{}
	for _, p := range parts {
		name := filepath.Join(dir, p.Name)
		if !p.Written {
			if _, err := os.Stat(name); !os.IsNotExist(err) {
				t.Errorf("part %d was to be left out, but it's there", p.Number)
			}
			continue
		}
		zr, err := zip.OpenReader(name)
		if err != nil {
			t.Fatalf("part %d: %v", p.Number, err)
		}
		if len(zr.File) != p.Entries {
			t.Errorf("part %d has %d entries, the manifest says %d", p.Number, len(zr.File), p.Entries)
		}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("%s: %v", f.Name, err)
			}
			if _, dup := files[f.Name]; dup {
				t.Errorf("%s is in more than one part", f.Name)
			}
			files[f.Name], where[f.Name] = b, p.Number
		}
		zr.Close()
	}
	return files, where
}

// exifDate finds DateTimeOriginal in a JPEG's APP1 segment, as a reader of
// EXIF would: IFD0, then the Exif IFD it points to.
func exifDate(t *testing.T, jpg []byte) string {
	t.Helper()
	i := bytes.Index(jpg, []byte("Exif\x00\x00"))
	if i < 0 {
		return ""
	}
	tiff := jpg[i+6:]
	le := binary.LittleEndian
	find := func(ifd uint32, tag uint16) (uint16, uint32, []byte) {
		n := uint32(le.Uint16(tiff[ifd:]))
		for k := range n {
			e := tiff[ifd+2+12*k:]
			if le.Uint16(e) == tag {
				return le.Uint16(e[2:]), le.Uint32(e[4:]), e[8:12]
			}
		}
		return 0, 0, nil
	}
	_, _, ptr := find(le.Uint32(tiff[4:]), 0x8769)
	if ptr == nil {
		t.Fatal("no Exif IFD pointer")
	}
	_, count, v := find(le.Uint32(ptr), 0x9003)
	if v == nil {
		t.Fatal("no DateTimeOriginal")
	}
	off := le.Uint32(v)
	return strings.TrimRight(string(tiff[off:off+count]), "\x00")
}

// TestGenerate makes a small Takeout with a part left out, opens every zip,
// and checks it against the manifest.
func TestGenerate(t *testing.T) {
	for _, lang := range []string{"en", "de"} {
		t.Run(lang, func(t *testing.T) {
			dir := t.TempDir()
			m, err := generate(options{out: dir, photos: 40, split: 400_000, seed: 7, skipPart: 2, lang: lang, big: 10})
			if err != nil {
				t.Fatal(err)
			}
			var onDisk Manifest
			b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
			if err != nil || json.Unmarshal(b, &onDisk) != nil {
				t.Fatalf("manifest.json: %v", err)
			}
			if len(onDisk.Photos) != len(m.Photos) || len(m.Parts) < 3 {
				t.Fatalf("%d photos in manifest.json, %d generated; %d parts", len(onDisk.Photos), len(m.Photos), len(m.Parts))
			}
			files, where := readZips(t, dir, m.Parts)

			check := func(f *FileRef) []byte {
				t.Helper()
				if f.Part == m.SkippedPart {
					if _, ok := files[f.Path]; ok {
						t.Errorf("%s should be in the part left out", f.Path)
					}
					return nil
				}
				if where[f.Path] != f.Part {
					t.Errorf("%s: in part %d, the manifest says %d", f.Path, where[f.Path], f.Part)
				}
				return files[f.Path]
			}
			for _, p := range m.Photos {
				for _, f := range p.Files {
					data := check(f)
					if data == nil {
						continue
					}
					if sum := sha1.Sum(data); hex.EncodeToString(sum[:]) != p.SHA1 {
						t.Errorf("%s: checksum differs from the manifest", f.Path)
					}
					if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
						t.Errorf("%s: %v", f.Path, err)
					}
					if p.EXIFDate != "" {
						if got := exifDate(t, data); got != p.EXIFDate {
							t.Errorf("%s: EXIF date %q, want %q", f.Path, got, p.EXIFDate)
						}
					} else if bytes.Contains(data[:64], []byte("Exif")) {
						t.Errorf("%s: has EXIF, the manifest says none", f.Path)
					}
				}
				if p.hasSidecar() && len(p.Sidecars) != len(p.Files) {
					t.Errorf("%s: %d files but %d sidecars", p.Title, len(p.Files), len(p.Sidecars))
				}
				for _, s := range p.Sidecars {
					data := check(s)
					if data == nil {
						continue
					}
					var sc map[string]any
					if err := json.Unmarshal(data, &sc); err != nil {
						t.Fatalf("%s: %v", s.Path, err)
					}
					if sc["title"] != p.Title {
						t.Errorf("%s: title %v, want %q", s.Path, sc["title"], p.Title)
					}
					for _, flag := range []struct {
						key string
						on  bool
					}{{"favorited", p.Favourite}, {"archived", p.Archived}, {"trashed", p.Trashed}} {
						if _, ok := sc[flag.key]; ok != flag.on {
							t.Errorf("%s: %q present %v, want %v", s.Path, flag.key, ok, flag.on)
						}
					}
				}
			}
			for _, a := range m.Albums {
				check(a.Metadata)
			}
			for _, x := range m.NotImported {
				check(&x.File)
			}
			if m.Totals.SplitAcrossParts == 0 || m.Totals.InSkippedPart == 0 {
				t.Errorf("want a photo split from its sidecar and some in the part left out: %+v", m.Totals)
			}
			root := languages[lang].root
			for p := range files {
				if p != "Takeout/archive_browser.html" && !strings.HasPrefix(p, "Takeout/"+root+"/") {
					t.Errorf("%s isn't under Takeout/%s", p, root)
				}
			}
		})
	}
}

// TestDeterministic checks that a seed always gives the same bytes.
func TestDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	for _, dir := range []string{a, b} {
		if _, err := generate(options{out: dir, photos: 20, split: 300_000, seed: 3, lang: "en"}); err != nil {
			t.Fatal(err)
		}
	}
	names, _ := filepath.Glob(filepath.Join(a, "*"))
	if len(names) < 3 {
		t.Fatalf("only %d files", len(names))
	}
	for _, n := range names {
		x, _ := os.ReadFile(n)
		y, err := os.ReadFile(filepath.Join(b, filepath.Base(n)))
		if err != nil || !bytes.Equal(x, y) {
			t.Errorf("%s differs between two runs with the same seed", filepath.Base(n))
		}
	}
}
