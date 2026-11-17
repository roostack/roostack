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

// ForApps gives the name apps' own lists most likely have: "UTC" for any
// other name of it, the current name for an old one, anything else as it is.
func TestForApps(t *testing.T) {
	dir := t.TempDir()
	TzdataFile, ZoneTabFiles = filepath.Join(dir, "tzdata.zi"), []string{filepath.Join(dir, "zone1970.tab"), filepath.Join(dir, "zone.tab")}
	t.Cleanup(func() {
		TzdataFile = "/usr/share/zoneinfo/tzdata.zi"
		ZoneTabFiles = []string{"/usr/share/zoneinfo/zone1970.tab", "/usr/share/zoneinfo/zone.tab"}
	})
	// Before the files exist: only UTC's names change.
	for name, want := range map[string]string{"": "UTC", "Etc/UTC": "UTC", "GMT": "UTC", "Asia/Calcutta": "Asia/Calcutta"} {
		if got := ForApps(name); got != want {
			t.Errorf("without files, ForApps(%q) = %q, want %q", name, got, want)
		}
	}
	os.WriteFile(TzdataFile, []byte("Z Asia/Kolkata 5:53:28 - LMT 1854 Jun 28\nZ Etc/UTC 0 - UTC\nL Etc/UTC Etc/UCT\n"+
		"L Asia/Kolkata Asia/Calcutta\nL Etc/UTC Etc/Something_new\nL Europe/Kyiv Europe/Kiev\n"), 0o644)
	os.WriteFile(ZoneTabFiles[0], []byte("# zone1970.tab\nIN\t+2232+08822\tAsia/Kolkata\n"), 0o644)
	os.WriteFile(ZoneTabFiles[1], []byte("# zone.tab\nNL\t+5222+00454\tEurope/Amsterdam\n"), 0o644)
	for name, want := range map[string]string{
		"Asia/Kolkata":      "Asia/Kolkata",
		"Asia/Calcutta":     "Asia/Kolkata",
		"Europe/Amsterdam":  "Europe/Amsterdam",
		"Etc/Something_new": "UTC",         // a link to Etc/UTC
		"Europe/Kiev":       "Europe/Kiev", // its target isn't listed
		"Etc/GMT+5":         "Etc/GMT+5",   // not a link
		"America/Nowhere":   "America/Nowhere",
	} {
		if got := ForApps(name); got != want {
			t.Errorf("ForApps(%q) = %q, want %q", name, got, want)
		}
	}
}
