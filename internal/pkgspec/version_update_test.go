package pkgspec

import "testing"

func TestUpdateKind(t *testing.T) {
	for _, c := range []struct{ from, to, want string }{
		{"1.36.0-roostack.1", "1.36.0-roostack.2", UpdateRevision},
		{"1.36.0-roostack.1", "1.36.1-roostack.1", UpdatePatch},
		{"1.36.0-roostack.3", "1.37.0-roostack.1", UpdateMinor},
		{"1.9.4-roostack.1", "2.1.0-roostack.1", UpdateMajor},
		{"v1.2-roostack.1", "v1.2.1-roostack.1", UpdatePatch},
		{"15-roostack.1", "16-roostack.1", UpdateMajor},
		{"1.2.0-rc1-roostack.1", "1.2.0-roostack.1", UpdateOther},
		{"latest-roostack.1", "newer-roostack.1", UpdateOther},
		{"1.2.0-roostack.1", "1.1.9-roostack.1", UpdateOther},
		{"1.02.0-roostack.1", "1.3.0-roostack.1", UpdateOther},
	} {
		if got := UpdateKind(c.from, c.to); got != c.want {
			t.Errorf("UpdateKind(%s, %s) = %s, want %s", c.from, c.to, got, c.want)
		}
	}
}

func TestNotesSince(t *testing.T) {
	p := &Package{Version: "2.1.0-roostack.1", UpgradeNotes: []UpgradeNote{
		{Version: "2.1.0-roostack.1", Note: "c"},
		{Version: "1.9.0-roostack.1", Note: "a"},
		{Version: "2.0.0-roostack.1", Note: "b"},
	}}
	got := p.NotesSince("1.9.4-roostack.1")
	if len(got) != 2 || got[0].Note != "b" || got[1].Note != "c" {
		t.Errorf("NotesSince = %+v, want b then c", got)
	}
	if got := p.NotesSince("2.1.0-roostack.1"); len(got) != 0 {
		t.Errorf("no notes for the same version, got %+v", got)
	}
}
