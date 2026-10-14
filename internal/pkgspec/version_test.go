package pkgspec

import "testing"

func TestCompareVersions(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.34.3-roostack.1", "1.34.3-roostack.1", 0},
		{"1.34.3-roostack.1", "1.34.3-roostack.2", -1},
		{"1.34.3-roostack.10", "1.34.3-roostack.9", 1},
		{"1.9.0-roostack.3", "1.10.0-roostack.1", -1},
		{"15.0.9-roostack.1", "2.1.0-roostack.1", 1},
		{"1.0-roostack.1", "1.0.1-roostack.1", -1},
		{"1.01-roostack.1", "1.1-roostack.1", 0},
		{"99999999999999999999.0", "99999999999999999998.0", 1},
		{"2.0.0-rc.1-roostack.1", "2.0.0-roostack.1", -1},
	} {
		if got := CompareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := CompareVersions(tc.b, tc.a); got != -tc.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", tc.b, tc.a, got, -tc.want)
		}
	}
}
