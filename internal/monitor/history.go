package monitor

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"roostack.cloud/roostack/internal/store"
)

// How a day looks on an uptime bar.
const (
	DayOK   = "ok"   // up whenever it was checked
	DayWarn = "warn" // down for less than an hour in all
	DayBad  = "bad"  // down for an hour or more
	DayNone = "none" // not checked (not there yet, paused, or monitoring off)
)

// Day is one day on an uptime bar.
type Day struct {
	Date time.Time // midnight, local time
	// Checks counted (not paused), and how many were up.
	Checks, Ups int
	State       string
}

// DownMinutes is roughly how long it was down that day (checks are a
// minute apart).
func (d Day) DownMinutes() int { return d.Checks - d.Ups }

// Uptime is the share of counted checks that were up, from 0 to 1, or -1
// when nothing was counted.
type Uptime float64

// Text is the uptime as people read it: "100%", "99.97%" (rounded down,
// so a little down time never shows as 100%), or "" with nothing counted.
func (u Uptime) Text() string {
	switch {
	case u < 0:
		return ""
	case u >= 1:
		return "100%"
	}
	return strconv.FormatFloat(math.Floor(float64(u)*10000)/100, 'f', 2, 64) + "%"
}

// Title says how the day went, for its bar's tooltip.
func (d Day) Title(today time.Time) string {
	when := d.Date.Format("Mon 2 Jan")
	switch daysBetween(d.Date, midnight(today)) {
	case 0:
		when = "Today"
	case 1:
		when = "Yesterday"
	}
	switch d.State {
	case DayOK:
		return when + ": up whenever checked"
	case DayWarn, DayBad:
		if n := d.DownMinutes(); n >= 120 {
			return fmt.Sprintf("%s: down for about %d hours", when, (n+30)/60)
		} else if n != 1 {
			return fmt.Sprintf("%s: down for about %d minutes", when, n)
		}
		return when + ": down for about a minute"
	}
	return when + ": not checked"
}

// History is a check's past, for the pages.
type History struct {
	// Days are the last n days, oldest first, today last.
	Days []Day
	// Day1, Week, Month and Quarter are its uptime over the last 24 hours,
	// 7, 30 and 90 days.
	Day1, Week, Month, Quarter Uptime
	// AvgMillis is the average answer time over the last 24 hours (0:
	// none), MaxMillis the slowest.
	AvgMillis, MaxMillis int
	// Recent are the results of the last 24 hours, oldest first.
	Recent []store.MonitorResult
	// Outages are the times it was down, newest first.
	Outages []store.MonitorOutage
}

// History reads a check's past: n days of bars (30 or 90), with its recent
// results and outages when detail is set.
func (m *Monitor) History(ctx context.Context, key string, n int, detail bool) (History, error) {
	now := m.now()
	today := midnight(now)
	from := today.AddDate(0, 0, -(n - 1))
	quarter := now.AddDate(0, 0, -90)
	since := from
	if quarter.Before(since) {
		since = quarter
	}
	hours, err := m.Store.MonitorHours(ctx, key, since)
	if err != nil {
		return History{}, err
	}
	h := History{Days: make([]Day, n)}
	for i := range h.Days {
		h.Days[i] = Day{Date: from.AddDate(0, 0, i), State: DayNone}
	}
	var day1, week, month, qtr struct{ checks, ups int }
	var msSum, msN int
	for _, hr := range hours {
		counted := hr.Checks - hr.Paused
		if i := daysBetween(from, midnight(hr.Hour)); i >= 0 && i < n {
			h.Days[i].Checks += counted
			h.Days[i].Ups += hr.Ups
		}
		for _, p := range []struct {
			acc   *struct{ checks, ups int }
			start time.Time
		}{{&day1, now.Add(-24 * time.Hour)}, {&week, now.AddDate(0, 0, -7)}, {&month, now.AddDate(0, 0, -30)}, {&qtr, quarter}} {
			if !hr.Hour.Before(p.start.Truncate(time.Hour)) {
				p.acc.checks += counted
				p.acc.ups += hr.Ups
			}
		}
		if !hr.Hour.Before(now.Add(-24 * time.Hour).Truncate(time.Hour)) {
			msSum += hr.MillisSum
			msN += hr.Ups
			h.MaxMillis = max(h.MaxMillis, hr.MillisMax)
		}
	}
	for i := range h.Days {
		d := &h.Days[i]
		switch {
		case d.Checks == 0:
			d.State = DayNone
		case d.Ups >= d.Checks:
			d.State = DayOK
		case d.DownMinutes() < 60:
			d.State = DayWarn
		default:
			d.State = DayBad
		}
	}
	share := func(a struct{ checks, ups int }) Uptime {
		if a.checks == 0 {
			return -1
		}
		return Uptime(float64(min(a.ups, a.checks)) / float64(a.checks))
	}
	h.Day1, h.Week, h.Month, h.Quarter = share(day1), share(week), share(month), share(qtr)
	if msN > 0 {
		h.AvgMillis = msSum / msN
	}
	if detail {
		if h.Recent, err = m.Store.MonitorResults(ctx, key, now.Add(-24*time.Hour)); err != nil {
			return h, err
		}
		if h.Outages, err = m.Store.MonitorOutages(ctx, key, now.AddDate(0, 0, -90)); err != nil {
			return h, err
		}
	}
	return h, nil
}

// midnight is the start of t's day, in t's location.
func midnight(t time.Time) time.Time {
	y, mo, d := t.Date()
	return time.Date(y, mo, d, 0, 0, 0, 0, t.Location())
}

// daysBetween counts calendar days from a to b (both midnights), safe
// across daylight saving changes.
func daysBetween(a, b time.Time) int {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return int(time.Date(by, bm, bd, 12, 0, 0, 0, time.UTC).Sub(time.Date(ay, am, ad, 12, 0, 0, 0, time.UTC)) / (24 * time.Hour))
}
