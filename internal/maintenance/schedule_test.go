package maintenance

import (
	"testing"
	"time"
)

func TestNextDaily(t *testing.T) {
	loc := time.FixedZone("test", 10*3600)
	at := func(d, h, m int) time.Time { return time.Date(2026, 9, d, h, m, 0, 0, loc) }
	for _, c := range []struct{ now, want time.Time }{
		{at(1, 1, 0), at(1, 2, 0)},  // later today
		{at(1, 2, 0), at(2, 2, 0)},  // exactly now: tomorrow
		{at(1, 23, 0), at(2, 2, 0)}, // tomorrow
		{at(30, 5, 0), time.Date(2026, 10, 1, 2, 0, 0, 0, loc)},
	} {
		if got := NextDaily(c.now, 2*time.Hour); !got.Equal(c.want) {
			t.Errorf("NextDaily(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

func TestNextDailyAcrossDST(t *testing.T) {
	syd, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skip("no tzdata")
	}
	// Clocks go forward at 02:00 on 2026-10-04 in Sydney.
	before := time.Date(2026, 10, 3, 12, 0, 0, 0, syd)
	got := NextDaily(before, 4*time.Hour)
	if got.Hour() != 4 || got.Day() != 4 {
		t.Errorf("got %s, want 04:00 local on the 4th", got)
	}
}

func TestNextWeekly(t *testing.T) {
	loc := time.UTC
	thu := time.Date(2026, 9, 24, 12, 0, 0, 0, loc) // a Thursday
	got := NextWeekly(thu, time.Sunday, 3*time.Hour)
	if want := time.Date(2026, 9, 27, 3, 0, 0, 0, loc); !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}
	sunLate := time.Date(2026, 9, 27, 4, 0, 0, 0, loc)
	if got := NextWeekly(sunLate, time.Sunday, 3*time.Hour); got.Day() != 4 || got.Month() != 10 {
		t.Errorf("after this week's run: got %s, want next Sunday", got)
	}
}

func TestFileJournal(t *testing.T) {
	j := FileJournal{Path: t.TempDir() + "/journal.json"}
	if names, err := j.Load(); err != nil || names != nil {
		t.Fatalf("empty: %v %v", names, err)
	}
	if err := j.Save([]string{"backup"}); err != nil {
		t.Fatal(err)
	}
	if names, _ := j.Load(); len(names) != 1 || names[0] != "backup" {
		t.Errorf("loaded %v", names)
	}
	if err := j.Save(nil); err != nil {
		t.Fatal(err)
	}
	if names, _ := j.Load(); names != nil {
		t.Errorf("after clearing: %v", names)
	}
}
