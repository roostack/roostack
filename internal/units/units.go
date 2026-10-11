// Package units formats sizes for people, one way everywhere in Roostack:
// decimal units (1 GB is 1,000,000,000 bytes), as drive makers, macOS and
// most people count them, so an "8 GB" USB drive shows as 8.0 GB.
package units

import "fmt"

// Sizes in bytes.
const (
	KB int64 = 1000
	MB       = 1000 * KB
	GB       = 1000 * MB
	TB       = 1000 * GB
)

// Bytes formats a size, e.g. "1.2 GB" ("unknown" if negative).
func Bytes(n int64) string {
	if n < 0 {
		return "unknown"
	}
	if n < KB {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := KB, 0
	for m := n / KB; m >= KB && exp < 4; m /= KB {
		div *= KB
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTP"[exp])
}

// Count formats a count with thousands separators, e.g. "1,284".
func Count(n int64) string {
	s := fmt.Sprint(n)
	neg := ""
	if n < 0 {
		neg, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return neg + s
}
