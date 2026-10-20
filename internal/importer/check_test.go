package importer

import (
	"slices"
	"strings"
	"testing"
)

// pgList is what pg_restore --list printed for the rehearsal's Immich dump
// (shortened).
const pgList = `;
; Archive created at 2026-10-20 09:14:51 UTC
;     dbname: immich
;     TOC Entries: 512
;     Compression: gzip
;     Dump Version: 1.14-0
;     Format: CUSTOM
;     Integer: 4 bytes
;     Offset: 8 bytes
;     Dumped from database version: 14.19 (Debian 14.19-1.pgdg120+1)
;     Dumped by pg_dump version: 14.19 (Debian 14.19-1.pgdg120+1)
;
; Selected TOC Entries:
;
4; 3079 16386 EXTENSION - cube
4120; 0 0 COMMENT - EXTENSION cube
5; 3079 16472 EXTENSION - earthdistance
6; 3079 16490 EXTENSION - vchord
7; 3079 16489 EXTENSION - vector
268; 1259 16700 TABLE public asset postgres
`

func TestParsePgList(t *testing.T) {
	d, err := ParsePgList(pgList)
	if err != nil || d.Version != "14.19" || d.Major != 14 || !slices.Equal(d.Extensions, []string{"cube", "earthdistance", "vchord", "vector"}) {
		t.Errorf("%+v %v", d, err)
	}
	if _, err := ParsePgList(strings.Replace(pgList, "CUSTOM", "TAR", 1)); err == nil {
		t.Error("a tar dump accepted")
	}
	if d, _ := ParsePgList(strings.Replace(pgList, "14.19 (Debian", "9.6.24 (Debian", 1)); d.Major != 9.6 {
		t.Errorf("9.6 is %v", d.Major)
	}
	if PgMajor("postgres (PostgreSQL) 16.4 (Debian 16.4-1.pgdg120+2)") != 16 || PgMajor("nothing") != 0 {
		t.Error("PgMajor")
	}
}

func TestCompareImages(t *testing.T) {
	for _, tc := range [][3]string{
		{"ghcr.io/immich-app/immich-server:v3.3.3", "ghcr.io/immich-app/immich-server:v3.3.3@sha256:x", SameImage},
		{"ghcr.io/immich-app/immich-server:v1.131.3", "ghcr.io/immich-app/immich-server:v3.3.3", OlderImage},
		{"ghcr.io/immich-app/immich-server:v3.4.0", "ghcr.io/immich-app/immich-server:v3.3.3", NewerImage},
		{"lscr.io/linuxserver/plex:latest", "docker.io/plexinc/pms-docker:1.43.4.10903-e5521bd8c", OtherImage},
		{"plexinc/pms-docker:1.43.4.10903-e5521bd8c", "docker.io/plexinc/pms-docker:1.43.4.10903-e5521bd8c", SameImage},
		{"plexinc/pms-docker:latest", "docker.io/plexinc/pms-docker:1.43.4.10903-e5521bd8c", SomeImage},
	} {
		if got := CompareImages(tc[0], tc[1]); got != tc[2] {
			t.Errorf("CompareImages(%s, %s) = %s, want %s", tc[0], tc[1], got, tc[2])
		}
	}
	if TagVersion("plexinc/pms-docker:1.43.4.10903-e5521bd8c") != "1.43.4.10903" || TagVersion("x") != "latest" {
		t.Error("TagVersion")
	}
}

func TestEnvDiff(t *testing.T) {
	old := map[string]string{"DB_PASSWORD": Masked, "IMMICH_LOG_LEVEL": "verbose", "PATH": "/bin", "NODE_ENV": "production", "TZ": "UTC"}
	got := EnvDiff(old, []string{"DB_PASSWORD", "TZ"}, map[string]string{"NODE_ENV": "production"})
	if !slices.Equal(got, []string{"IMMICH_LOG_LEVEL=verbose"}) {
		t.Errorf("%v", got)
	}
}

func TestCountAndSamplePaths(t *testing.T) {
	text := strings.Repeat("x", 3<<20) + `'/usr/src/app/upload/library/a.jpg'` + strings.Repeat("y", 1<<20-10) +
		"\x00/usr/src/app/upload/library/b.jpg\x00/movies/Film (2001)/film.mkv\x00/movies/Film (2001)/film.mkv\x00"
	counts, err := CountPaths(strings.NewReader(text), []string{"/usr/src/app/upload/", "/movies/", "/tv/"})
	if err != nil || counts["/usr/src/app/upload/"] != 2 || counts["/movies/"] != 2 || counts["/tv/"] != 0 {
		t.Errorf("%v %v", counts, err)
	}
	got, err := SamplePaths(strings.NewReader(text), "/movies/", 10)
	if err != nil || !slices.Equal(got, []string{"Film (2001)/film.mkv"}) {
		t.Errorf("%q %v", got, err)
	}
	got, _ = SamplePaths(strings.NewReader(text), "/usr/src/app/upload/", 10)
	if !slices.Equal(got, []string{"library/a.jpg", "library/b.jpg"}) {
		t.Errorf("%q", got)
	}
}
