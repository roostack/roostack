package maintenance

import (
	"context"
	"fmt"
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

// Every fires at the wall clock's time even if its timers ran slow (as
// after a suspend): it looks at the clock often, not once.
func TestEveryChecksTheClock(t *testing.T) {
	checkEvery = 10 * time.Millisecond
	defer func() { checkEvery = time.Minute }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := make(chan time.Time, 3)
	start := time.Now()
	// The first time is already past (the machine slept through it): it
	// fires at once, then each time after.
	n := 0
	go Every(ctx, func(now time.Time) time.Time {
		n++
		if n == 1 {
			return start.Add(-time.Hour)
		}
		return now.Add(30 * time.Millisecond)
	}, func() { fired <- time.Now() })
	for i := range 3 {
		select {
		case <-fired:
		case <-time.After(2 * time.Second):
			t.Fatalf("fire %d didn't come", i+1)
		}
	}
}

// TestOffset: each server gets the same whole minute under Spread every
// time, and servers spread over the half hour rather than all taking a
// few minutes.
func TestOffset(t *testing.T) {
	if Offset("n-0123456789") != Offset("n-0123456789") {
		t.Error("a server's offset changed")
	}
	minutes := map[time.Duration]bool{}
	for i := range 300 {
		d := Offset(fmt.Sprintf("n-%010d", i))
		if d < 0 || d >= Spread || d%time.Minute != 0 {
			t.Fatalf("offset %v", d)
		}
		minutes[d] = true
	}
	if len(minutes) != int(Spread/time.Minute) {
		t.Errorf("300 servers took %d of the %d minutes", len(minutes), int(Spread/time.Minute))
	}
	// With the offset, 02:00 becomes this server's own minute.
	loc := time.FixedZone("AEST", 10*3600)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, loc)
	if got, want := NextDaily(now, DefaultBackupAt+17*time.Minute), time.Date(2026, 10, 25, 2, 17, 0, 0, loc); !got.Equal(want) {
		t.Errorf("NextDaily = %v, want %v", got, want)
	}
}
