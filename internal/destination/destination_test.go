package destination

import "testing"

func TestRef(t *testing.T) {
	for _, c := range []struct {
		ref  string
		want Ref
	}{
		{"server:n-1a2b", Ref{Server, "n-1a2b"}},
		{"provider:3", Ref{Provider, "3"}},
		{"folder:/srv/backups", Ref{Folder, "/srv/backups"}},
		{"drive:uuid:1234", Ref{Drive, "uuid:1234"}}, // only the first colon
		{"cloud", Ref{Cloud, ""}},
		{"setup", Ref{Setup, ""}},
		{"", Ref{}},
	} {
		got := Parse(c.ref)
		if got != c.want {
			t.Errorf("Parse(%q) = %+v, want %+v", c.ref, got, c.want)
		}
		if got.String() != c.ref {
			t.Errorf("Parse(%q).String() = %q", c.ref, got.String())
		}
	}
	if got := OfProvider(42); got != "provider:42" {
		t.Errorf("OfProvider = %q", got)
	}
	if got := New(Server, "n-1"); got != "server:n-1" {
		t.Errorf("New = %q", got)
	}
}
