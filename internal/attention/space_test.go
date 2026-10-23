package attention

import (
	"testing"

	"roostack.cloud/roostack/units"
)

func TestLowSpace(t *testing.T) {
	for _, c := range []struct {
		free, total int64
		want        string
	}{
		{100 * units.GB, 200 * units.GB, ""},
		{15 * units.GB, 200 * units.GB, Warning},  // under 10%
		{4 * units.GB, 20 * units.GB, Warning},    // under 5 GB
		{8 * units.GB, 200 * units.GB, Problem},   // under 5%
		{1500 * units.MB, 25 * units.GB, Problem}, // under 2 GB
		// A disk under 20 GB: the floors are a quarter and a tenth of it.
		{1900 * units.MB, 2100 * units.MB, ""},
		{400 * units.MB, 2000 * units.MB, Warning},
		{150 * units.MB, 2000 * units.MB, Problem},
		{3 * units.GB, 10 * units.GB, ""},
		{-1, 10 * units.GB, ""},
	} {
		if got := LowSpace(c.free, c.total); got != c.want {
			t.Errorf("LowSpace(%d, %d) = %q, want %q", c.free, c.total, got, c.want)
		}
	}
}
