package hosttime

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestZone(t *testing.T) {
	dir := t.TempDir()
	LocaltimeFile = filepath.Join(dir, "localtime")
	t.Cleanup(func() { LocaltimeFile = "/etc/localtime" })

	if got := Zone(); got != "UTC" {
		t.Errorf("missing link: got %q", got)
	}
	os.Symlink("../usr/share/zoneinfo/Australia/Melbourne", LocaltimeFile)
	if got := Zone(); got != "Australia/Melbourne" {
		t.Errorf("got %q", got)
	}
}

func TestZones(t *testing.T) {
	TzdataFile = filepath.Join(t.TempDir(), "tzdata.zi")
	t.Cleanup(func() { TzdataFile = "/usr/share/zoneinfo/tzdata.zi" })
	os.WriteFile(TzdataFile, []byte("# version 2026a\nR Aus 1917 o - Ja 1 2:1 1 D\n"+
		"Z Australia/Melbourne 9:39:52 - LMT 1895 F\n10 AV AE%sT\nZ Etc/UTC 0 - UTC\nL Etc/UTC UTC\n"), 0o644)
	got, err := Zones()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Australia/Melbourne", "Etc/UTC", "UTC"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
