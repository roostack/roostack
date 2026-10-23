package units

import "testing"

func TestBytes(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{
		{-1, "unknown"},
		{0, "0 B"},
		{1, "1 B"},
		{999, "999 B"},
		{1000, "1.0 kB"},
		{19_900, "19.9 kB"},
		{999_949, "999.9 kB"},
		// Rounding doesn't carry into the next unit.
		{999_999, "1000.0 kB"},
		{MB, "1.0 MB"},
		{MB + 1, "1.0 MB"},
		{GB - 1, "1000.0 MB"},
		{GB, "1.0 GB"},
		{1_536_000_000, "1.5 GB"},
		{8 * GB, "8.0 GB"},
		{TB - 1, "1000.0 GB"},
		{TB, "1.0 TB"},
		{2*TB + 500*GB, "2.5 TB"},
		{1500 * TB, "1.5 PB"},
	} {
		if got := Bytes(c.n); got != c.want {
			t.Errorf("Bytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestAsSold(t *testing.T) {
	for _, c := range []struct {
		n    int64
		want string
	}{
		{-1, "unknown"},
		{0, "nothing"},
		{1, "under 1 MB"},
		{400 * KB, "under 1 MB"},
		{MB - 1, "under 1 MB"},
		{MB, "1 MB"},
		{MB + 1, "1 MB"},
		{1_250_000, "1.25 MB"},
		{1_500_000, "1.5 MB"},
		{9_996_000, "10 MB"},
		{12_500_000, "12.5 MB"},
		{99_960_000, "100 MB"},
		{128 * MB, "128 MB"},
		{999 * MB, "999 MB"},
		{999_499_999, "999 MB"},
		// Rounding carries into the next unit: never four digits.
		{999_500_000, "1 GB"},
		{GB - 1, "1 GB"},
		{GB, "1 GB"},
		{GB + 1, "1 GB"},
		{1_536_000_000, "1.54 GB"},
		{128 * GB, "128 GB"},
		{TB - 1, "1 TB"},
		{TB, "1 TB"},
		{1500 * GB, "1.5 TB"},
		{2*TB + 500*GB, "2.5 TB"},
		{1500 * TB, "1500 TB"},
	} {
		if got := AsSold(c.n); got != c.want {
			t.Errorf("AsSold(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1284: "1,284", 1234567: "1,234,567", -1500: "-1,500"} {
		if got := Count(n); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
}
