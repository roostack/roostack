package words

import (
	"testing"
	"time"
)

func TestCapitalise(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "running": "Running", "Owner": "Owner", "a": "A", "3 files": "3 files",
		"émilie": "Émilie", "über": "Über", "ǆ": "Ǆ", "日本": "日本", "\xffbad": "\xffbad",
	} {
		if got := Capitalise(in); got != want {
			t.Errorf("Capitalise(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLists(t *testing.T) {
	for _, c := range []struct {
		in      []string
		and, or string
	}{
		{nil, "", ""},
		{[]string{}, "", ""},
		{[]string{"Films"}, "Films", "Films"},
		{[]string{"Films", "TV"}, "Films and TV", "Films or TV"},
		{[]string{"Films", "TV", "Music"}, "Films, TV and Music", "Films, TV or Music"},
		{[]string{"", "b"}, " and b", " or b"},
	} {
		if got := And(c.in); got != c.and {
			t.Errorf("And(%q) = %q, want %q", c.in, got, c.and)
		}
		if got := Or(c.in); got != c.or {
			t.Errorf("Or(%q) = %q, want %q", c.in, got, c.or)
		}
	}
}

func TestCount(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 photos", 1: "1 photo", 2: "2 photos", 999: "999 photos", 61204: "61,204 photos", -1: "-1 photos",
	} {
		if got := Count(n, "photo", "photos"); got != want {
			t.Errorf("Count(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestPlural(t *testing.T) {
	for n, want := range map[int]string{0: "stop", 1: "stops", 2: "stop", -1: "stop"} {
		if got := Plural(n, "stops", "stop"); got != want {
			t.Errorf("Plural(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "disk full": "disk full", "Fatal: no repo\nstack\ntrace": "Fatal: no repo",
		"\nsecond": "", "trailing \n": "trailing ", "a\r\nb": "a\r",
	} {
		if got := FirstLine(in); got != want {
			t.Errorf("FirstLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRoughDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "a minute", -time.Hour: "a minute", 89 * time.Second: "a minute",
		90 * time.Second: "2 minutes", 25 * time.Minute: "25 minutes", 89 * time.Minute: "89 minutes",
		90 * time.Minute: "2 hours", 3 * time.Hour: "3 hours", 3*time.Hour + 29*time.Minute: "3 hours",
		3*time.Hour + 30*time.Minute: "4 hours", 72 * time.Hour: "72 hours",
	} {
		if got := RoughDuration(d); got != want {
			t.Errorf("RoughDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestExactDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0 s", 1400 * time.Millisecond: "1 s", 14 * time.Second: "14 s", 59*time.Second + 600*time.Millisecond: "1 min",
		3*time.Minute + 59*time.Second: "3 min", time.Hour: "1 h 0 min", time.Hour + 5*time.Minute: "1 h 5 min",
		26*time.Hour + 12*time.Minute: "26 h 12 min", -5 * time.Second: "-5 s",
	} {
		if got := ExactDuration(d); got != want {
			t.Errorf("ExactDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestWhen(t *testing.T) {
	now := time.Now()
	at := func(days int) time.Time {
		y, m, d := now.Date()
		return time.Date(y, m, d+days, 2, 30, 0, 0, time.Local)
	}
	if got := When(time.Time{}); got != "never" {
		t.Errorf("zero: %q", got)
	}
	if got := When(at(0)); got != "today 02:30" {
		t.Errorf("today: %q", got)
	}
	// Yesterday and tomorrow within this month only; a date otherwise.
	for _, days := range []int{-1, 1} {
		t0 := at(days)
		want := map[int]string{-1: "yesterday 02:30", 1: "tomorrow 02:30"}[days]
		if t0.Month() != now.Month() {
			want = t0.Format("Mon 2 Jan 15:04")
		}
		if got := When(t0); got != want {
			t.Errorf("%d days: %q, want %q", days, got, want)
		}
	}
	if got, want := When(at(40)), at(40).Format("Mon 2 Jan 15:04"); got != want {
		t.Errorf("further off: %q, want %q", got, want)
	}
	// A time from another server's zone is shown in this one's.
	if got := When(at(0).In(time.FixedZone("elsewhere", 5*3600))); got != "today 02:30" {
		t.Errorf("another zone: %q", got)
	}
}

func TestLowerFirstWord(t *testing.T) {
	for in, want := range map[string]string{
		"Media disk": "media disk", "USB drive BACKUP": "USB drive BACKUP", "PostgreSQL data": "PostgreSQL data",
		"A": "A", "": "",
	} {
		if got := LowerFirstWord(in); got != want {
			t.Errorf("LowerFirstWord(%q) = %q, want %q", in, got, want)
		}
	}
}
