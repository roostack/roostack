package importer

import (
	"bufio"
	"bytes"
	"cmp"
	"errors"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The check before anything is copied (SPEC.md §8.4, docs/MIGRATION-PLAN.md
// §3.6): a list of what was found, each with ✓ or what to do. The server
// works it out (internal/node, import.go); these are its rules.

// Item is one line of the check: its first sentence, more after it, and
// sometimes a command to copy or a list.
type Item struct {
	Level string   `json:"level"`
	Text  string   `json:"text"`
	More  string   `json:"more,omitempty"`
	Code  string   `json:"code,omitempty"`
	List  []string `json:"list,omitempty"`
}

// Levels of a check's items: fine; worth a look; in the way (nothing is
// copied until it's fixed); just so you know.
const (
	LevelOK   = "ok"
	LevelWarn = "warn"
	LevelBad  = "bad"
	LevelInfo = "info"
)

// Report is a check's result.
type Report struct {
	At    time.Time `json:"at"`
	Items []Item    `json:"items"`
	// Files and Bytes are what the copy brings.
	Files int64 `json:"files"`
	Bytes int64 `json:"bytes"`
}

// Ready says whether nothing in the report is in the way.
func (r *Report) Ready() bool {
	return r != nil && !r.At.IsZero() && !slices.ContainsFunc(r.Items, func(i Item) bool { return i.Level == LevelBad })
}

// Warnings counts the report's warnings.
func (r *Report) Warnings() int {
	n := 0
	for _, i := range r.Items {
		if i.Level == LevelWarn {
			n++
		}
	}
	return n
}

// PgDump is what `pg_restore --list` says of a custom-format dump.
type PgDump struct {
	// Version is the Postgres it was dumped from ("14.19"), Major its
	// major version (14; 9.6 is 9.6's).
	Version string
	Major   float64
	// Extensions are those the dump creates.
	Extensions []string
}

var (
	pgFrom      = regexp.MustCompile(`(?m)^;\s+Dumped from database version: (\d+)\.(\d+)`)
	pgExtension = regexp.MustCompile(`(?m)^\d+; \d+ \d+ EXTENSION - (\S+)`)
	pgFormat    = regexp.MustCompile(`(?m)^;\s+Format: (\S+)`)
)

// ParsePgList reads `pg_restore --list` of a dump.
func ParsePgList(out string) (PgDump, error) {
	var d PgDump
	if f := pgFormat.FindStringSubmatch(out); f == nil || f[1] != "CUSTOM" {
		return d, errors.New("it isn't a custom-format dump (pg_dump --format=custom)")
	}
	m := pgFrom.FindStringSubmatch(out)
	if m == nil {
		return d, errors.New("it doesn't say which Postgres it came from")
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	d.Version, d.Major = m[1]+"."+m[2], float64(major)
	if major < 10 {
		d.Major = float64(major) + float64(minor)/10
	}
	for _, e := range pgExtension.FindAllStringSubmatch(out, -1) {
		if !slices.Contains(d.Extensions, e[1]) {
			d.Extensions = append(d.Extensions, e[1])
		}
	}
	slices.Sort(d.Extensions)
	return d, nil
}

// PgMajor reads `postgres --version` ("postgres (PostgreSQL) 14.19 …") or
// a version ("14.19") as a major version (14, or 9.6).
func PgMajor(s string) float64 {
	m := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < 10 {
		return float64(major) + float64(minor)/10
	}
	return float64(major)
}

// SQLDump says what a MySQL or MariaDB dump's first lines say it is
// ("MariaDB", "MySQL"), or "" for what isn't one.
func SQLDump(head []byte) string {
	s := string(head)
	switch {
	case strings.Contains(s, "MariaDB dump"):
		return "MariaDB"
	case strings.Contains(s, "MySQL dump"):
		return "MySQL"
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return "" // gzipped: not yet
	}
	return ""
}

// Images compared (CompareImages).
const (
	SameImage  = "same"
	OlderImage = "older" // the old one is older: the app upgrades its data
	NewerImage = "newer" // the old one is newer: likely to fail
	OtherImage = "other" // another repository: folders and users may differ
	SomeImage  = "some"  // the same repository, versions that can't be compared
)

// CompareImages compares an old container's image with the package's.
func CompareImages(old, pkg string) string {
	if Repository(old) != Repository(pkg) {
		return OtherImage
	}
	a, b := versionOf(Tag(old)), versionOf(Tag(pkg))
	if a == nil || b == nil {
		if Tag(old) == Tag(pkg) {
			return SameImage
		}
		return SomeImage
	}
	switch slices.Compare(a, b) {
	case 0:
		return SameImage
	case -1:
		return OlderImage
	}
	return NewerImage
}

// versionOf reads a version from an image's tag: 1.43.4.10903 from
// 1.43.4.10903-e5521bd8c, 3.3.3 from v3.3.3.
func versionOf(tag string) []int {
	m := regexp.MustCompile(`^v?(\d+(?:\.\d+){0,3})(?:[-_.+].*)?$`).FindStringSubmatch(tag)
	if m == nil {
		return nil
	}
	var v []int
	for _, p := range strings.Split(m[1], ".") {
		n, _ := strconv.Atoi(p)
		v = append(v, n)
	}
	return v
}

// TagVersion is the version an image's tag names ("3.3.3"), or the tag.
func TagVersion(image string) string {
	v := versionOf(Tag(image))
	if v == nil {
		return cmp.Or(Tag(image), "latest")
	}
	var parts []string
	for _, n := range v {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ".")
}

// EnvDiff lists an old container's settings the package doesn't set
// itself (set: its variables), leaving out those its image sets the same
// way by default (the image's own PATH, versions…), sorted, as K=V.
func EnvDiff(old map[string]string, set []string, defaults map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(old)) {
		v := old[k]
		if slices.Contains(set, k) || (defaults != nil && defaults[k] == v) || k == "PATH" || k == "HOSTNAME" || k == "HOME" {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}

// CountPaths counts how often each of needles appears in what r reads
// (a dump as text, an SQLite file), reading it once.
func CountPaths(r io.Reader, needles []string) (map[string]int, error) {
	counts := map[string]int{}
	longest := 0
	for _, n := range needles {
		longest = max(longest, len(n))
		counts[n] = 0
	}
	if longest == 0 {
		return counts, nil
	}
	br := bufio.NewReaderSize(r, 1<<20)
	buf := make([]byte, 0, 2<<20)
	chunk := make([]byte, 1<<20)
	for {
		n, err := br.Read(chunk)
		buf = append(buf, chunk[:n]...)
		// Count what can't be the start of a match cut by the chunk's end.
		safe := len(buf) - (longest - 1)
		if errors.Is(err, io.EOF) {
			safe = len(buf)
		}
		if safe > 0 {
			for _, nd := range needles {
				counts[nd] += countStarts(buf, []byte(nd), safe)
			}
			buf = append(buf[:0], buf[safe:]...)
		}
		if errors.Is(err, io.EOF) {
			return counts, nil
		}
		if err != nil {
			return counts, err
		}
	}
}

// countStarts counts matches of needle in buf starting before limit.
func countStarts(buf, needle []byte, limit int) int {
	n, at := 0, 0
	for {
		i := bytes.Index(buf[at:], needle)
		if i < 0 || at+i >= limit {
			return n
		}
		n++
		at += i + 1
	}
}

// SamplePaths finds up to limit distinct paths under prefix ("/movies/")
// in what r reads, as they're written there (up to a quote, a NUL or a
// line's end): the files an old app's data names.
func SamplePaths(r io.Reader, prefix string, limit int) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	br := bufio.NewReaderSize(r, 1<<20)
	p := []byte(prefix)
	var tail []byte
	chunk := make([]byte, 1<<20)
	for len(out) < limit {
		n, err := br.Read(chunk)
		buf := append(tail, chunk[:n]...)
		at := 0
		for len(out) < limit {
			i := bytes.Index(buf[at:], p)
			if i < 0 {
				break
			}
			start := at + i + len(p)
			end := start
			for end < len(buf) && end-start < 1024 && !bytes.ContainsRune([]byte("\x00\n\r\"'\t"), rune(buf[end])) && buf[end] >= 0x20 {
				end++
			}
			if end == len(buf) && !errors.Is(err, io.EOF) {
				break // cut by the chunk's end: next time
			}
			if s := string(buf[start:end]); s != "" && !seen[s] && !strings.Contains(s, "..") {
				seen[s] = true
				out = append(out, s)
			}
			at = end
		}
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		keep := min(len(buf), 1024+len(p))
		tail = append([]byte(nil), buf[len(buf)-keep:]...)
	}
	return out, nil
}
