// Package units formats sizes for people, one way everywhere in Roostack:
// decimal units (1 GB is 1,000,000,000 bytes), as drive makers, macOS and
// most people count them, so an "8 GB" USB drive shows as 8.0 GB.
//
// It's public, not under internal, so Roostack Cloud's repository formats
// sizes with the same code (quality review X1).
package units

import (
	"fmt"
	"strings"
)

// Sizes in bytes.
const (
	KB int64 = 1000
	MB       = 1000 * KB
	GB       = 1000 * MB
	TB       = 1000 * GB
)

// Bytes formats a size, e.g. "1.2 GB" ("unknown" if negative). It's how
// Roostack shows every size: always one decimal, so a drive's free space
// or a backup's growth is exact enough to compare.
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

// AsSold formats a size as storage is sold, e.g. "128 GB", "1.5 TB": three
// significant digits at most, without a trailing ".0", in MB, GB or TB.
// Anything below a megabyte is "under 1 MB", since rounding 400 kB to
// "0 MB" would say there's nothing (quality review P8), and zero is
// "nothing" ("unknown" if negative).
//
// It's Roostack Cloud's style, for sizes set beside an allowance or a price,
// where "128.0 GB" or "0.4 MB" would be detail nobody buys by. Roostack
// itself uses Bytes.
func AsSold(n int64) string {
	switch {
	case n < 0:
		return "unknown"
	case n == 0:
		return "nothing"
	case n < MB:
		return "under 1 MB"
	}
	sold := []struct {
		name string
		size int64
	}{{"MB", MB}, {"GB", GB}, {"TB", TB}}
	i := 0
	for i+1 < len(sold) && n >= sold[i+1].size {
		i++
	}
	s := figure(float64(n) / float64(sold[i].size))
	if s == "1000" && i+1 < len(sold) {
		// 999.6 MB rounds to 1000 MB, which is four digits: it's 1 GB.
		i, s = i+1, "1"
	}
	return s + " " + sold[i].name
}

// figure is f to three significant digits at most, without a trailing ".0"
// (1.25, 12.5, 128).
func figure(f float64) string {
	switch {
	case f >= 100:
		return fmt.Sprintf("%.0f", f)
	case f >= 10:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", f), ".0")
	}
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
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
