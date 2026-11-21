package generate

import (
	"strings"
	"testing"

	"roostack.cloud/roostack/internal/instance"
	"roostack.cloud/roostack/internal/pkgspec"
	"roostack.cloud/roostack/internal/pkgtest"
)

// The services together may use the server's memory less 400 MB, never
// less than half; with the server's memory unknown, there's no cap.
func TestServicesMemory(t *testing.T) {
	for _, tc := range []struct{ total, want int64 }{
		{0, 0}, {2_000_000_000, 1_600_000_000}, {16_000_000_000, 15_600_000_000}, {600_000_000, 300_000_000},
	} {
		if got := ServicesMemory(tc.total); got != tc.want {
			t.Errorf("ServicesMemory(%d) = %d, want %d", tc.total, got, tc.want)
		}
	}
	lab := testLab()
	lab.MemoryTotal = 2_000_000_000
	lab.Email, lab.DNS.Provider = "admin@example.com", "cloudflare"
	out, err := System(lab, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Slices) != 3 || !strings.Contains(out.Slices[2].Content, "MemoryHigh=1600000000\n") {
		t.Errorf("slices on a 2 GB server: %+v", out.Slices)
	}
}

// A service's limit: the Owner's choice always; otherwise twice its
// figure (at least 256 MB above it), only while the server is tight, and
// only when every part says what it expects.
func TestMemoryLimit(t *testing.T) {
	lab := testLab()
	p, err := pkgspec.Load(pkgtest.File("vaultwarden")) // 50 MB, SQLite
	if err != nil {
		t.Fatal(err)
	}
	in := &instance.Instance{Package: "vaultwarden", Version: p.Version}
	for _, tc := range []struct {
		choice string
		tight  bool
		want   int64
	}{
		{"", false, 0},
		{"", true, 306_000_000},
		{instance.MemoryNoLimit, true, 0},
		{"1.5 GB", false, 1_500_000_000},
	} {
		in.MemoryLimit = tc.choice
		if got, err := MemoryLimit(lab, p, in, tc.tight); err != nil || got != tc.want {
			t.Errorf("%q, tight %v: %d, %v; want %d", tc.choice, tc.tight, got, err, tc.want)
		}
	}
	in.MemoryLimit = "lots"
	if _, err := MemoryLimit(lab, p, in, true); err == nil {
		t.Error("a limit that isn't a size was taken")
	}

	// A part that doesn't say: no automatic limit, as it can't be known.
	c := *p.Containers["app"]
	c.Memory = ""
	p.Containers = map[string]*pkgspec.Container{"app": &c}
	in.MemoryLimit = ""
	if got, _ := MemoryLimit(lab, p, in, true); got != 0 {
		t.Errorf("a part that doesn't say: %d", got)
	}

	// Big figures double: Immich's machine learning.
	im, err := pkgspec.Load(pkgtest.File("immich"))
	if err != nil {
		t.Fatal(err)
	}
	fig, known, _ := MemoryFigure(lab, im, &instance.Instance{Package: "immich", Version: im.Version})
	got, _ := MemoryLimit(lab, im, &instance.Instance{Package: "immich", Version: im.Version}, true)
	if known && got != 2*fig {
		t.Errorf("Immich: figure %d, limit %d", fig, got)
	}

	if ServiceSlice("actual-budget") != "roostack-services-actual_budget.slice" {
		t.Errorf("a name with a hyphen: %s", ServiceSlice("actual-budget"))
	}
	if f := ServiceSliceFile("forgejo", 0); strings.Contains(f.Content, "MemoryHigh") {
		t.Errorf("no limit:\n%s", f.Content)
	}
	if f := ServiceSliceFile("forgejo", 600_000_000); !strings.Contains(f.Content, "MemoryHigh=600000000\n") || !strings.Contains(f.Content, "past 600.0 MB") {
		t.Errorf("a limit:\n%s", f.Content)
	}
}
