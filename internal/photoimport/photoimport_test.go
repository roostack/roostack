package photoimport

import (
	"archive/zip"
	"bytes"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// zipOf makes a zip of name → content.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(files[n]))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func sidecarJSON(title string, taken int64, extra string) string {
	return `{"title":"` + title + `","photoTakenTime":{"timestamp":"` + strconv.FormatInt(taken, 10) + `"}` + extra + `}`
}

func TestScanTakeout(t *testing.T) {
	gp := "Takeout/Google Photos/"
	photo := "jpeg bytes of a photo"
	part1 := zipOf(t, map[string]string{
		gp + "Photos from 2014/IMG_1.JPG":                            photo,
		gp + "Photos from 2014/IMG_1.JPG.supplemental-metadata.json": sidecarJSON("IMG_1.JPG", 1402477950, `,"favorited":true`),
		gp + "Photos from 2014/IMG_2.JPG":                            "another photo",
		gp + "Photos from 2014/IMG_2.JPG.json":                       sidecarJSON("IMG_2.JPG", 1388534400, `,"trashed":true`),
		gp + "Lisbon 2014/IMG_1.JPG":                                 photo, // the album's copy
		gp + "Lisbon 2014/IMG_1.JPG.supplemental-metadata.json":      sidecarJSON("IMG_1.JPG", 1402477950, `,"favorited":true`),
		gp + "Lisbon 2014/metadata.json":                             `{"title":"Lisbon 2014","date":{"timestamp":"1402477950"}}`,
		gp + "Photos from 2015/PXL_20150611.MP":                      "motion",
		gp + ".DS_Store":                                             "clutter",
	})
	part3 := zipOf(t, map[string]string{
		gp + "Photos from 2020/VID_1.mp4":      "a video",
		gp + "Photos from 2020/VID_1.mp4.json": sidecarJSON("VID_1.mp4", 1600000000, `,"googlePhotosOrigin":{"fromPartnerSharing":{}}`),
		gp + "Untitled(2)/Metadaten.json":      `{"title":""}`,
	})
	fsys := fstest.MapFS{
		"takeout-20261019T091234Z-001.zip": {Data: part1},
		"takeout-20261019T091234Z-003.zip": {Data: part3},
		"takeout-20250101T000000Z-001.tgz": {Data: []byte("not read")},
		"notes.txt":                        {Data: []byte("not a part")},
	}
	s, err := ScanFS(fsys, KindGoogle)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Parts) != 2 || s.Parts[0].Number != 1 || s.Parts[1].Number != 3 {
		t.Errorf("parts = %+v", s.Parts)
	}
	if !slices.Equal(s.Missing, []int{2}) {
		t.Errorf("missing = %v, want [2]", s.Missing)
	}
	if s.Exports != 1 || len(s.Tgz) != 1 || len(s.Unreadable) != 0 {
		t.Errorf("exports %d, tgz %v, unreadable %v", s.Exports, s.Tgz, s.Unreadable)
	}
	if s.Photos != 2 || s.Videos != 1 {
		t.Errorf("photos %d, videos %d; want 2 and 1 (an album's copy counts once)", s.Photos, s.Videos)
	}
	if want := int64(len(photo) + len("another photo") + len("a video")); s.Bytes != want {
		t.Errorf("bytes = %d, want %d", s.Bytes, want)
	}
	if s.Albums != 2 || s.Untitled != 1 {
		t.Errorf("albums %d, untitled %d; want 2 and 1", s.Albums, s.Untitled)
	}
	if s.Trashed != 1 || s.Partner != 1 || s.Favourites != 1 {
		t.Errorf("trashed %d, partner %d, favourites %d; want 1 each (sidecars count once)", s.Trashed, s.Partner, s.Favourites)
	}
	if s.Other != 1 || len(s.OtherExamples) != 1 || !strings.HasSuffix(s.OtherExamples[0], ".MP") {
		t.Errorf("other = %d %v, want the .MP file (and not .DS_Store)", s.Other, s.OtherExamples)
	}
	if !s.First.Equal(time.Unix(1388534400, 0)) || !s.Last.Equal(time.Unix(1600000000, 0)) {
		t.Errorf("first %v, last %v", s.First, s.Last)
	}
}

func TestScanTakeoutBrokenZip(t *testing.T) {
	fsys := fstest.MapFS{"takeout-20261019T091234Z-001.zip": {Data: []byte("cut short")}}
	s, err := ScanFS(fsys, KindGoogle)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Unreadable) != 1 {
		t.Errorf("unreadable = %v, want the cut-short zip", s.Unreadable)
	}
}

func TestScanTwoExports(t *testing.T) {
	z := zipOf(t, map[string]string{"Takeout/Google Photos/Photos from 2014/a.jpg": "x"})
	fsys := fstest.MapFS{
		"takeout-20261019T091234Z-001.zip": {Data: z},
		"takeout-20261020T080000Z-001.zip": {Data: z},
	}
	s, _ := ScanFS(fsys, KindGoogle)
	if s.Exports != 2 {
		t.Errorf("exports = %d, want 2", s.Exports)
	}
}

func TestScanFolder(t *testing.T) {
	fsys := fstest.MapFS{
		"2019/Holiday/a.jpg":     {Data: []byte("12345")},
		"2019/Holiday/a.jpg.xmp": {Data: []byte("<x/>")},
		"2019/b.MOV":             {Data: []byte("123")},
		"2019/@eaDir/a.jpg":      {Data: []byte("a Synology thumbnail")},
		".hidden/c.jpg":          {Data: []byte("x")},
		"notes.pdf":              {Data: []byte("x")},
	}
	s, err := ScanFS(fsys, KindFolder)
	if err != nil {
		t.Fatal(err)
	}
	if s.Photos != 1 || s.Videos != 1 || s.Bytes != 8 || s.Other != 1 {
		t.Errorf("scan = %+v", s)
	}
}

func TestCommandGoogle(t *testing.T) {
	args, err := Command(Run{Kind: KindGoogle, Choices: DefaultChoices(), Server: "http://roostack-immich-server:2283",
		Source: "/export", Parts: []string{"takeout-1.zip", "takeout-2.zip"}, Log: "/work/run.log", DryRun: true, Tasks: 64})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	for _, want := range []string{"upload from-google-photos", "--server=http://roostack-immich-server:2283", "--no-ui",
		"--log-type=JSON", "--log-file=/work/run.log", "--pause-immich-jobs=false", "--on-errors=continue",
		"--concurrent-tasks=20", "--dry-run", "--include-trashed=false", "--include-partner=false",
		"--include-untitled-albums=false", "--people-tag=true", "--manage-burst=Stack", "--include-unmatched=true",
		"-- /export/takeout-1.zip /export/takeout-2.zip"} {
		if !strings.Contains(line, want) {
			t.Errorf("command lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "api-key") {
		t.Errorf("the key must come from %s, not the command line:\n%s", KeyEnv, line)
	}
}

func TestCommandFolder(t *testing.T) {
	c := DefaultChoices()
	c.FolderAlbums, c.Stacks = false, false
	args, err := Command(Run{Kind: KindFolder, Choices: c, Server: "http://s:2283", Source: "/export", Log: "/work/l", Tasks: 0})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	for _, want := range []string{"upload from-folder", "--folder-as-album=NONE", "--manage-raw-jpeg=NoStack", "--concurrent-tasks=1", "-- /export"} {
		if !strings.Contains(line, want) {
			t.Errorf("command lacks %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "--dry-run") {
		t.Error("not a dry run")
	}
	if _, err := Command(Run{Kind: KindGoogle}); err == nil {
		t.Error("a Takeout without zips should be refused")
	}
	if _, err := Command(Run{Kind: "flickr"}); err == nil {
		t.Error("an unknown kind should be refused")
	}
}

func TestTally(t *testing.T) {
	log := strings.Join([]string{
		"immich-go banner, not JSON",
		`{"time":"t","level":"INFO","msg":"discovered image","file":"a.zip:IMG_1.JPG"}`,
		`{"time":"t","level":"INFO","msg":"discovered image","file":"a.zip:IMG_2.JPG"}`,
		`{"time":"t","level":"INFO","msg":"discovered video","file":"a.zip:VID_1.mp4"}`,
		`{"time":"t","level":"INFO","msg":"uploaded successfully","file":"a.zip:IMG_1.JPG","size":1000}`,
		`{"time":"t","level":"INFO","msg":"server has duplicate","file":"a.zip:IMG_2.JPG"}`,
		`{"time":"t","level":"ERROR","msg":"server error","file":"a.zip:VID_1.mp4","error":"413 too large"}`,
		`{"time":"t","level":"WARN","msg":"discarded filtered","file":"a.zip:IMG_3.JPG","reason":"trashed"}`,
		`{"time":"t","level":"WARN","msg":"discovered unsupported file","file":"a.zip:x.MP"}`,
		`{"time":"t","level":"WARN","msg":"discovered unsupported file","file":"a.zip:Takeout/Google Photos/Untitled(2)/metadata.json"}`,
		`{"time":"t","level":"WARN","msg":"missing metadata","file":"a.zip:IMG_4.JPG"}`,
		`{"time":"t","level":"INFO","msg":"added to album","file":"a.zip:IMG_1.JPG","album":"Lisbon"}`,
	}, "\n")
	var tl Tally
	if err := tl.Read(strings.NewReader(log)); err != nil {
		t.Fatal(err)
	}
	if tl.Found != 3 || tl.Sent != 1 || tl.Had != 1 || tl.Failed != 1 || tl.LeftOut != 1 || tl.Unsupported != 1 ||
		tl.NoDate != 1 || tl.InAlbums != 1 || tl.SentBytes != 1000 || tl.Lines != 12 || tl.Clutter != 1 {
		t.Errorf("tally = %+v", tl)
	}
	if tl.Done() != 4 {
		t.Errorf("done = %d, want 4", tl.Done())
	}
	if len(tl.Errors) != 1 || tl.Errors[0].Why != "413 too large" {
		t.Errorf("errors = %+v", tl.Errors)
	}
	if len(tl.Skipped) != 1 || tl.Skipped[0].Why != "trashed" {
		t.Errorf("left out = %+v", tl.Skipped)
	}
	if len(tl.Unreadable) != 1 || tl.Unreadable[0].File != "a.zip:x.MP" {
		t.Errorf("unreadable = %+v (Takeout's own JSON files aren't photos)", tl.Unreadable)
	}
}
