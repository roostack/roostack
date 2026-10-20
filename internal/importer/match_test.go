package importer

import (
	"os"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

func immich(t *testing.T) *pkgspec.Package {
	t.Helper()
	p, err := pkgspec.Load(pkgtest.File("immich"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func plex(t *testing.T) *pkgspec.Package {
	t.Helper()
	p, err := pkgspec.Load("testdata/plex.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func setupFrom(t *testing.T, file string) *Setup {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ParseInspect(data)
	if err != nil {
		t.Fatal(err)
	}
	return &s
}

// docker inspect gives each container's image, Compose service, mounts
// and environment, with secrets' values masked.
func TestParseInspect(t *testing.T) {
	s := setupFrom(t, "testdata/immich-inspect.json")
	if s.Project != "immich" || len(s.Containers) != 4 {
		t.Fatalf("setup %+v", s)
	}
	srv := s.Containers[0]
	if srv.Name != "immich_server" || srv.Service != "immich-server" || len(srv.Mounts) != 2 ||
		srv.Mounts[0] != (Mount{Source: "/opt/immich/library", Target: "/data"}) || !srv.Mounts[1].ReadOnly {
		t.Errorf("server %+v", srv)
	}
	if srv.Env["DB_PASSWORD"] != Masked || srv.Env["IMMICH_LOG_LEVEL"] != "verbose" {
		t.Errorf("env %v", srv.Env)
	}
	if ml := s.Containers[1]; ml.Mounts[0].Volume != "immich_model-cache" {
		t.Errorf("a named volume: %+v", ml.Mounts)
	}
	if p := setupFrom(t, "testdata/plex-inspect.json"); !p.Containers[0].NVIDIA || p.Containers[0].Env["PLEX_CLAIM"] != "claim-xyz" {
		t.Errorf("plex %+v", p.Containers[0])
	}
	for _, bad := range []string{"", "{}", "[]", "not json", `[{"Name": "/x", "Config": {}}]`} {
		if _, err := ParseInspect([]byte(bad)); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// Several outputs pasted one after another.
	data, _ := os.ReadFile("testdata/plex-inspect.json")
	if two, err := ParseInspect(append(append(data, '\n'), data...)); err != nil || len(two.Containers) != 2 {
		t.Errorf("two pasted: %v %v", two, err)
	}
}

func TestRepositoryAndTag(t *testing.T) {
	for image, want := range map[string]string{
		"postgres":                "docker.io/library/postgres",
		"postgres:16":             "docker.io/library/postgres",
		"plexinc/pms-docker:1.43": "docker.io/plexinc/pms-docker",
		"docker.io/plexinc/pms-docker@sha256:abc":          "docker.io/plexinc/pms-docker",
		"ghcr.io/immich-app/immich-server:v2.1.0@sha256:x": "ghcr.io/immich-app/immich-server",
		"localhost:5000/app:1":                             "localhost:5000/app",
	} {
		if got := Repository(image); got != want {
			t.Errorf("Repository(%q) = %q, want %q", image, got, want)
		}
	}
	if Tag("ghcr.io/immich-app/immich-server:v2.1.0@sha256:x") != "v2.1.0" || Tag("localhost:5000/app") != "" {
		t.Error("tags")
	}
}

// Immich's old /data fills library, and its thumbs/ and encoded-video/
// the storage mounted there; the old model cache isn't in the files.
func TestMatchImmich(t *testing.T) {
	files := fstest.MapFS{
		"immich/library/upload/a.jpg":        {Data: []byte("x")},
		"immich/library/thumbs/a.webp":       {Data: []byte("x")},
		"immich/library/encoded-video/a.mp4": {Data: []byte("x")},
		"immich/main.dump":                   {Data: []byte("PGDMP")},
	}
	p, setup := immich(t), setupFrom(t, "testdata/immich-inspect.json")
	guess := GuessPrefixes(p, setup, nil, files)
	if len(guess) != 1 || guess[0] != (Prefix{Old: "/opt/immich", Now: "immich"}) {
		t.Fatalf("guessed %+v", guess)
	}
	plan := Match(p, setup, guess, Choices{}, files, nil)
	byName := map[string]Storage{}
	for _, s := range plan.Storage {
		byName[s.Name] = s
	}
	if s := byName["library"]; s.From != "immich/library" || s.By != ByPath || !slices.Equal(s.Exclude, []string{"encoded-video", "thumbs"}) ||
		s.Old != "/opt/immich/library" || s.Path != "/data" {
		t.Errorf("library %+v", s)
	}
	if s := byName["thumbs"]; s.From != "immich/library/thumbs" || s.Old != "/opt/immich/library/thumbs" || len(s.Exclude) != 0 {
		t.Errorf("thumbs %+v", s)
	}
	if s := byName["model-cache"]; s.From != "" || s.Why != WhyNotFound || s.Old != "/var/lib/docker/volumes/immich_model-cache/_data" {
		t.Errorf("model-cache %+v", s)
	}
	if len(plan.Databases) != 1 || plan.Databases[0].From != "immich/main.dump" || plan.Databases[0].Old != "immich_postgres" {
		t.Errorf("databases %+v", plan.Databases)
	}
	parts := map[string]string{}
	for _, m := range plan.Containers {
		parts[m.Old] = m.Part + m.Database
	}
	if parts["immich_server"] != "server" || parts["immich_machine_learning"] != "machine-learning" || parts["immich_postgres"] != "main" ||
		parts["immich_redis"] != "cache" {
		t.Errorf("containers %v", parts)
	}
	why := map[string]string{}
	for _, l := range plan.Left {
		why[l.Target] = l.Why
	}
	if why["/var/lib/postgresql/data"] != WhyDBFiles || why["/etc/localtime"] != WhyFile || len(plan.Left) != 2 {
		t.Errorf("left %+v", plan.Left)
	}

	// Chosen by hand: an old Immich at /usr/src/app/upload, as its
	// library; the thumbnails left out.
	plan = Match(p, nil, nil, Choices{Storage: map[string]string{"library": "immich/library", "thumbs": Leave}}, files, nil)
	byName = map[string]Storage{}
	for _, s := range plan.Storage {
		byName[s.Name] = s
	}
	if s := byName["library"]; s.By != ByHand || !slices.Equal(s.Exclude, []string{"encoded-video", "thumbs"}) {
		t.Errorf("by hand %+v", s)
	}
	if byName["thumbs"].Why != WhyLeft || byName["encoded"].Why != WhyNoSetup {
		t.Errorf("left out %+v, unmatched %+v", byName["thumbs"], byName["encoded"])
	}
	// A folder that isn't there isn't taken.
	plan = Match(p, setup, []Prefix{{Old: "/opt", Now: "elsewhere"}}, Choices{}, files, nil)
	if plan.Storage[1].Why != WhyNotFound && plan.Storage[0].Why != WhyNotFound {
		t.Errorf("not there %+v", plan.Storage)
	}
}

// libsByName finds a library named as the old folder.
type libsByName map[string]string

func (l libsByName) Find(target, source string) (string, string, int, int) {
	return l[target], ByName, 0, 0
}

// Plex's config is matched by its path; its media mounts become
// libraries shown where they were.
func TestMatchPlex(t *testing.T) {
	files := fstest.MapFS{"plex/config/Library/Preferences.xml": {Data: []byte("<x/>")}}
	p, setup := plex(t), setupFrom(t, "testdata/plex-inspect.json")
	plan := Match(p, setup, []Prefix{{Old: "/opt/plex", Now: "plex"}}, Choices{}, files, libsByName{"/movies": "films"})
	if s := plan.Storage[0]; s.Name != "config" || s.From != "plex/config" {
		t.Errorf("config %+v", s)
	}
	if s := plan.Storage[1]; s.Name != "transcode" || s.Why != WhyNotFound {
		t.Errorf("transcode %+v", s)
	}
	if len(plan.Libraries) != 2 || plan.Libraries[0] != (Library{OldTarget: "/movies", Old: "/mnt/media/films", Library: "films", By: ByName}) ||
		plan.Libraries[1].Library != "" {
		t.Errorf("libraries %+v", plan.Libraries)
	}
	plan = Match(p, setup, nil, Choices{Libraries: map[string]string{"/tv": "tv", "/movies": Leave}}, files, libsByName{})
	if plan.Libraries[0].Library != "" || plan.Libraries[1].Library != "tv" || plan.Libraries[1].By != ByHand {
		t.Errorf("chosen %+v", plan.Libraries)
	}
}

func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		old      string
		prefixes []Prefix
		want     string
		ok       bool
	}{
		{"/opt/immich/library", []Prefix{{"/opt/immich", "immich"}}, "immich/library", true},
		{"/opt/immich", []Prefix{{"/opt/immich", ""}}, ".", true},
		{"/opt/immich/library", []Prefix{{"/opt", "a"}, {"/opt/immich", "b"}}, "b/library", true},
		{"./library", []Prefix{{".", "compose"}}, "compose/library", true},
		{"/srv/x", []Prefix{{"/opt", "a"}}, "", false},
		{"/opt/x", []Prefix{{"/opt", "../etc"}}, "", false},
		{"/anything", []Prefix{{"/", "root"}}, "root/anything", true},
	} {
		got, ok := Translate(tc.old, tc.prefixes)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Translate(%q, %v) = %q %v, want %q %v", tc.old, tc.prefixes, got, ok, tc.want, tc.ok)
		}
	}
}

func TestSetupClean(t *testing.T) {
	s := Setup{From: FromCompose, Containers: []Container{{Service: "web", Image: "nginx", Env: map[string]string{"API_KEY": "abc", "MODE": "x"},
		Mounts: []Mount{{Source: "./data", Target: "/data"}}}}}
	if err := s.Clean(); err != nil || s.Containers[0].Name != "web" || s.Containers[0].Env["API_KEY"] != Masked {
		t.Errorf("clean %+v %v", s, err)
	}
	for _, bad := range []Setup{{From: "x", Containers: s.Containers}, {From: FromCompose},
		{From: FromCompose, Containers: []Container{{Image: "a", Mounts: []Mount{{Target: "rel"}}}}},
		{From: FromCompose, Containers: []Container{{Image: "a\nb"}}}} {
		if err := bad.Clean(); err == nil || strings.Contains(err.Error(), "%!") {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}
