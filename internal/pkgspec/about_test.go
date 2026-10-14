package pkgspec

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMemory(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int64
	}{
		{"300 MB", 300_000_000},
		{"300MB", 300_000_000},
		{"1.5 GB", 1_500_000_000},
		{"2 GB", 2_000_000_000},
		{"1 MB", 1_000_000},
		{"1000 GB", 1_000_000_000_000},
	} {
		if got, err := ParseMemory(tt.in); err != nil || got != tt.want {
			t.Errorf("ParseMemory(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "300", "300M", "300 MiB", "1.5 gb", "-1 GB", "0 MB", "0.5 MB", "1001 GB", "1,5 GB", " 300 MB"} {
		if _, err := ParseMemory(in); err == nil {
			t.Errorf("ParseMemory(%q) succeeded, want an error", in)
		}
	}
}

func TestExpectedMemory(t *testing.T) {
	if got := (&Container{Memory: "1.5 GB"}).ExpectedMemory(); got != 1_500_000_000 {
		t.Errorf("container = %d", got)
	}
	if got := (&Container{}).ExpectedMemory(); got != 0 {
		t.Errorf("container without memory = %d, want 0", got)
	}
	for _, tt := range []struct {
		db   Database
		want int64
	}{
		{Database{Type: DBPostgres}, DefaultPostgresMemory},
		{Database{Type: DBPostgres, Memory: "500 MB"}, 500_000_000},
		{Database{Type: DBRedis}, DefaultRedisMemory},
		{Database{Type: DBSQLite}, 0},
	} {
		if got := tt.db.ExpectedMemory(); got != tt.want {
			t.Errorf("%+v: ExpectedMemory = %d, want %d", tt.db, got, tt.want)
		}
	}
}

// requires.roostack against the running version (SPEC.md §8.3.5).
func TestRunsOn(t *testing.T) {
	for _, tt := range []struct {
		requires, running string
		want              bool
	}{
		{"", "v0.1.0", true},
		{"0.6", "v0.6.0", true},
		{"0.6", "0.6.0", true},
		{"0.6", "v0.5.9", false},
		{"0.6", "v0.7.0", true},
		{"0.6", "v1.0.0", true},
		{"0.6.2", "v0.6.1", false},
		{"0.6.2", "v0.6.2", true},
		{"0.6.2", "v0.6", false},
		{"0.10", "v0.9.0", false}, // numbers, not text
		{"0.10", "v0.10.0", true},
		// After a tag (git describe), a pre-release, an uncommitted build:
		// the release they start with.
		{"0.6", "v0.6.0-3-gabc1234", true},
		{"0.6", "v0.5.0-30-gabc1234-dirty", false},
		{"0.6", "v0.6.0-rc.1", true},
		// Development builds run everything.
		{"9.0", "dev", true},
		{"9.0", "abc1234", true},
		{"9.0", "abc1234-dirty", true},
	} {
		p := &Package{Requires: Requires{Roostack: tt.requires}}
		if got := p.RunsOn(tt.running); got != tt.want {
			t.Errorf("requires %q, running %q: RunsOn = %v, want %v", tt.requires, tt.running, got, tt.want)
		}
	}
}

func TestCheckSPDX(t *testing.T) {
	for _, ok := range []string{
		"MIT", "AGPL-3.0-only", "GPL-3.0-or-later", "MIT OR Apache-2.0",
		"(MIT OR Apache-2.0) AND BSD-3-Clause", "GPL-2.0-or-later WITH Classpath-exception-2.0",
		"LicenseRef-Proprietary", "EUPL-1.2", "GPL-2.0+",
	} {
		if err := checkSPDX(ok); err != nil {
			t.Errorf("checkSPDX(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "MIT OR", "OR MIT", "MIT Apache-2.0", "(MIT", "MIT)", "MIT WITH", "GNU Affero General Public License version 3", "MIT/Apache"} {
		if err := checkSPDX(bad); err == nil {
			t.Errorf("checkSPDX(%q) succeeded, want an error", bad)
		}
	}
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCheckIcon(t *testing.T) {
	if err := CheckIcon(pngOf(t, 256, 256)); err != nil {
		t.Errorf("256×256: %v", err)
	}
	if err := CheckIcon(pngOf(t, 512, 512)); err != nil {
		t.Errorf("512×512: %v", err)
	}
	for name, tt := range map[string]struct {
		data []byte
		want string
	}{
		"small":      {pngOf(t, 128, 128), "too small"},
		"not square": {pngOf(t, 512, 256), "isn't square"},
		"not a png":  {[]byte("<svg/>"), "not a PNG"},
		"too large":  {make([]byte, IconMaxBytes+1), "too large"},
	} {
		if err := CheckIcon(tt.data); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: CheckIcon = %v, want it to contain %q", name, err, tt.want)
		}
	}

	// The file beside the package.
	dir := t.TempDir()
	p := &Package{About: About{Icon: "app.png"}}
	if err := p.CheckIcon(dir); err == nil || !strings.Contains(err.Error(), "about.icon") {
		t.Errorf("missing file: CheckIcon = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.png"), pngOf(t, 256, 256), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckIcon(dir); err != nil {
		t.Errorf("CheckIcon = %v", err)
	}
	if err := (&Package{}).CheckIcon(dir); err != nil {
		t.Errorf("no icon: CheckIcon = %v", err)
	}
}

// Every new field, filled in, parses.
func TestAboutParses(t *testing.T) {
	p, err := Parse([]byte(`
schema: roostack/v1alpha1
name: app
title: App
version: 2.0.0-roostack.1
about:
  maker: a volunteer community
  website: https://example.org
  source: https://example.org/src
  licence: MIT OR Apache-2.0
  support: https://example.org/donate
  icon: app.png
  categories: [documents, productivity]
  keywords: [notes, markdown, wiki]
requires: { roostack: 0.6 }
upgrade_notes:
  - version: 2.0.0-roostack.1
    note: Version 2 moves the settings page; check yours after updating.
containers:
  web: { image: x, memory: 1.5 GB }
`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Requires.Roostack != "0.6" || p.About.Categories[1] != "productivity" || p.UpgradeNotes[0].Version != "2.0.0-roostack.1" {
		t.Errorf("parsed %+v", p)
	}
	if got := CategoryTitle("productivity"); got != "Tasks and planning" {
		t.Errorf("CategoryTitle = %q", got)
	}
}
