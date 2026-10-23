// Package words writes the small pieces of sentences people read, one way
// everywhere in Roostack: a capital at the start, lists ("a, b and c"),
// counts ("1 photo", "1,284 photos"), an error's first line, lengths of
// time and when something happens. Sizes and plain numbers are units'.
package words

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"roostack.cloud/roostack/units"
)

// Capitalise starts s with a capital letter: "running" → "Running",
// "émilie" → "Émilie".
func Capitalise(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 || r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// And lists items for a sentence: "a", "a and b", "a, b and c" ("" if
// there are none).
func And(items []string) string { return list(items, "and") }

// Or lists items as choices: "a", "a or b", "a, b or c" ("" if there are
// none).
func Or(items []string) string { return list(items, "or") }

func list(items []string, word string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " " + word + " " + items[len(items)-1]
}

// Count is a count with its noun, the number with thousands separators:
// "1 photo", "0 photos", "61,204 photos".
func Count(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return units.Count(n) + " " + many
}

// Plural is the word for n of something, without the number: one when n
// is 1, otherwise many ("stops", "stop").
func Plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// FirstLine is text's first line: an error's own words, without the
// output that follows them.
func FirstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// RoughDuration says how long, roughly, rounded to the nearest minute or
// hour: "a minute", "25 minutes", "3 hours". Sentences put "about" before
// it when it's an estimate.
func RoughDuration(d time.Duration) string {
	switch {
	case d < 90*time.Second:
		return "a minute"
	case d < 90*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()+0.5))
	}
	return fmt.Sprintf("%d hours", int(d.Hours()+0.5))
}

// ExactDuration says how long, to the second under a minute and to the
// minute after: "14 s", "3 min", "1 h 5 min".
func ExactDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
}

// When is a time for people, in this server's time zone: "today 02:00",
// "yesterday 14:30", "tomorrow 02:00", or "Mon 3 Oct 02:00" further off;
// "never" for none.
func When(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	// Times from another server arrive in its time zone; show them in
	// this one's, like everything else (SPEC.md §3.2).
	t = t.Local()
	now := time.Now()
	y1, m1, d1 := now.Date()
	y2, m2, d2 := t.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return "today " + t.Format("15:04")
	case y1 == y2 && m1 == m2 && d2 == d1-1:
		return "yesterday " + t.Format("15:04")
	case y1 == y2 && m1 == m2 && d2 == d1+1:
		return "tomorrow " + t.Format("15:04")
	}
	return t.Format("Mon 2 Jan 15:04")
}

// LowerFirstWord lowers a phrase's first letter, unless its first word
// looks like a name (another capital in it, such as "PostgreSQL").
func LowerFirstWord(s string) string {
	word, _, _ := strings.Cut(s, " ")
	if len(word) < 2 || strings.ToLower(word[1:]) != word[1:] {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}
