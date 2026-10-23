package kept

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSuffixAndParse(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 12, 5, 0, time.Local)
	for _, tc := range []struct {
		why  Why
		note string
		want string
	}{
		{BeforeRestore, "", ".before-restore-20261022-141205"},
		{MovedAway, "to-nuc", ".moved-to-nuc-20261022-141205"},
		{BeforePutBack, "", ".before-put-back-20261022-141205"},
	} {
		s := Suffix(tc.why, tc.note, at)
		if s != tc.want {
			t.Errorf("Suffix(%s) = %q", tc.why, s)
		}
		f, ok := Parse("/srv/roostack/services/immich" + s)
		if !ok || f.Of != "/srv/roostack/services/immich" || f.Why != tc.why || f.Note != tc.note || !f.Time.Equal(at) {
			t.Errorf("Parse(%q) = %+v, %v", s, f, ok)
		}
	}
	for _, bad := range []string{"immich", "immich.incoming", "immich.before-restore-2026", "immich.later-20261022-141205"} {
		if f, ok := Parse("/srv/" + bad); ok {
			t.Errorf("Parse(%q) = %+v", bad, f)
		}
	}
}

func TestFind(t *testing.T) {
	root := t.TempDir()
	services, media := filepath.Join(root, "services"), filepath.Join(root, "media")
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Join(root, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mk("services/immich/library")
	mk("services/immich/library.before-restore-20261022-141205")
	mk("services/immich/db-dump.before-restore-20261022-141205")
	mk("services/immich/db-dump.failed-restore-20261021-093011")
	mk("services/immich/unrelated.before-restore-20261022-141205") // not a backed-up folder
	mk("services/mealie.moved-to-nuc-20261019-200412")
	mk("media/mealie.moved-to-nuc-20261019-200412")
	mk("services/paperless.incoming")
	mk("services/x.y.before-move-20261019-200412") // not a service's name
	os.WriteFile(filepath.Join(services, "notes.before-move-20261019-200412"), nil, 0o644)

	got, err := Find([]string{services, media, filepath.Join(root, "missing")},
		map[string][]string{"immich": {filepath.Join(services, "immich/library"), filepath.Join(services, "immich/db-dump")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("found %+v", got)
	}
	if c := got[0]; c.Service != "immich" || c.Why != BeforeRestore || c.Whole || len(c.Paths) != 2 || c.Size != -1 ||
		!c.Has(filepath.Join(services, "immich/library.before-restore-20261022-141205")) {
		t.Errorf("first %+v", c)
	}
	if c := got[1]; c.Why != FailedRestore || c.Why.CanGoBack() || !c.Why.Unfinished() {
		t.Errorf("second %+v", c)
	}
	if c := got[2]; c.Service != "mealie" || c.Why != MovedAway || c.Note != "to-nuc" || !c.Whole ||
		!slices.Equal(c.Paths, []string{filepath.Join(media, "mealie.moved-to-nuc-20261019-200412"),
			filepath.Join(services, "mealie.moved-to-nuc-20261019-200412")}) || c.Why.CanGoBack() {
		t.Errorf("third %+v", c)
	}
}
