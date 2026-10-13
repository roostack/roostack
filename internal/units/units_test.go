package units

import "testing"

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{
		-1: "unknown", 0: "0 B", 999: "999 B", 1000: "1.0 kB", 19_900: "19.9 kB",
		1_536_000_000: "1.5 GB", 8 * GB: "8.0 GB", 2*TB + 500*GB: "2.5 TB",
	} {
		if got := Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
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
