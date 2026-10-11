package web

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"roostack.cloud/roostack/cloudapi"
	"roostack.cloud/roostack/internal/units"
)

// Visitors through Roostack Cloud's relay (SPEC.md §10.5): counts Cloud's
// edges keep, never who visited. A card loaded on its own (Cloud may be
// slow), on Public access for Owners and on its own page for Admins.

// relayPeriods are the card's choices: how many days, and the label.
var relayPeriods = []struct {
	Value, Label string
	Days         int
}{{"today", "Today", 1}, {"week", "7 days", 7}, {"month", "30 days", 30}, {"year", "A year", 365}}

// relayStatsView is the card.
type relayStatsView struct {
	Period  string
	Periods []relayPeriodChoice
	// Error says why there's nothing to show (Cloud out of reach).
	Error string
	// The period's totals: different visitors (each day's, added up),
	// connections, data each way, connections turned away and why.
	Visitors, Connections int64
	BytesIn, BytesOut     int64
	Banned, Country       int64
	Bars                  []barView
	BarsTitle             string
	Countries             []rankView
}

type relayPeriodChoice struct {
	Value, Label string
	Current      bool
}

// barView is one bar of a bar chart: its label under it (may be empty),
// what it says on hover, and its height as a percent of the tallest.
type barView struct {
	Label, Title string
	Percent      int
}

// rankView is one row of a ranked list: a name, a count, and its bar's
// width as a percent of the largest.
type rankView struct {
	Name    string
	Count   int64
	Percent int
	Other   bool
}

// barChart is a bar chart's caption and bars (the bar-chart component).
type barChart struct {
	Title string
	Bars  []barView
}

// Chart is the card's bar chart.
func (v relayStatsView) Chart() barChart { return barChart{v.BarsTitle, v.Bars} }

// Refused is the period's connections turned away for the lab.
func (v relayStatsView) Refused() int64 { return v.Banned + v.Country }

// Bytes is the period's data both ways.
func (v relayStatsView) Bytes() int64 { return v.BytesIn + v.BytesOut }

// visitorsPage is the card on its own, for Admins (who don't see
// Settings), when the lab is relayed.
func (s *Server) visitorsPage(w http.ResponseWriter, r *http.Request) {
	if !s.Manager.RelayOn() {
		redirect(w, r, "/services")
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data["Period"] = r.FormValue("period")
	s.render(w, "visitors", data)
}

// relayStatsPart is the card, for ?period= (default 7 days).
func (s *Server) relayStatsPart(w http.ResponseWriter, r *http.Request) {
	v := s.relayStats(r.Context(), r.FormValue("period"), time.Now())
	s.renderPart(w, "settings-public", "relay-stats", v)
}

func (s *Server) relayStats(ctx context.Context, period string, now time.Time) relayStatsView {
	choice := relayPeriods[1]
	for _, p := range relayPeriods {
		if p.Value == period {
			choice = p
		}
	}
	v := relayStatsView{Period: choice.Value}
	for _, p := range relayPeriods {
		v.Periods = append(v.Periods, relayPeriodChoice{p.Value, p.Label, p.Value == choice.Value})
	}
	if s.Cloud == nil {
		v.Error = "This lab isn't linked to Roostack Cloud."
		return v
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	days, err := s.Cloud.RelayStats(ctx, choice.Days, true)
	if err != nil {
		v.Error = cloudMessage(err)
		return v
	}
	countries := map[string]int64{}
	for _, p := range days.Periods {
		v.Visitors += p.Visitors
		v.Connections += p.Connections
		v.BytesIn += p.BytesIn
		v.BytesOut += p.BytesOut
		v.Banned += p.Refused["banned"]
		v.Country += p.Refused["country"]
		for c, n := range p.Countries {
			countries[c] += n
		}
	}
	v.Countries = topCountries(countries, v.Visitors, 4)
	switch choice.Value {
	case "today":
		hours, err := s.Cloud.RelayStats(ctx, 1, false)
		if err != nil {
			v.Error = cloudMessage(err)
			return v
		}
		v.BarsTitle = "Visitors each hour"
		v.Bars = hourBars(hours.Periods, now)
	case "year":
		v.BarsTitle = "Visitors each month"
		v.Bars = monthBars(days.Periods, now)
	default:
		v.BarsTitle = "Visitors each day"
		v.Bars = dayBars(days.Periods, now, choice.Days)
	}
	return v
}

// hourBars are today's hours (here), so far.
func hourBars(periods []cloudapi.RelayPeriod, now time.Time) []barView {
	now = now.Local()
	byHour := map[int]int64{}
	for _, p := range periods {
		t := p.Start.Local()
		if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
			byHour[t.Hour()] += p.Visitors
		}
	}
	var vals []int64
	var labels, titles []string
	for h := 0; h <= now.Hour(); h++ {
		vals = append(vals, byHour[h])
		label := ""
		if h%6 == 0 {
			label = strconv.Itoa(h) + ":00"
		}
		labels = append(labels, label)
		titles = append(titles, countOf(byHour[h], "visitor")+" from "+strconv.Itoa(h)+":00")
	}
	return bars(vals, labels, titles)
}

// dayBars are the last n days (the lab's days), today last.
func dayBars(periods []cloudapi.RelayPeriod, now time.Time, n int) []barView {
	byDay := map[string]int64{}
	for _, p := range periods {
		byDay[p.Day] += p.Visitors
	}
	var vals []int64
	var labels, titles []string
	now = now.Local()
	for i := n - 1; i >= 0; i-- {
		d := now.AddDate(0, 0, -i)
		vals = append(vals, byDay[d.Format(time.DateOnly)])
		label := d.Format("Mon")
		if n > 7 {
			label = ""
			if i%7 == 0 {
				label = d.Format("2 Jan")
			}
		}
		if i == 0 {
			label = "Today"
		}
		labels = append(labels, label)
		titles = append(titles, d.Format("Mon 2 Jan")+": "+countOf(byDay[d.Format(time.DateOnly)], "visitor"))
	}
	return bars(vals, labels, titles)
}

// monthBars are the last 12 months, this one last.
func monthBars(periods []cloudapi.RelayPeriod, now time.Time) []barView {
	byMonth := map[string]int64{}
	for _, p := range periods {
		if len(p.Day) >= 7 {
			byMonth[p.Day[:7]] += p.Visitors
		}
	}
	var vals []int64
	var labels, titles []string
	now = now.Local()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	for i := 11; i >= 0; i-- {
		m := first.AddDate(0, -i, 0)
		vals = append(vals, byMonth[m.Format("2006-01")])
		labels = append(labels, m.Format("Jan"))
		titles = append(titles, m.Format("January 2006")+": "+countOf(byMonth[m.Format("2006-01")], "visitor"))
	}
	return bars(vals, labels, titles)
}

func bars(vals []int64, labels, titles []string) []barView {
	top := slices.Max(append([]int64{0}, vals...))
	out := make([]barView, len(vals))
	for i, n := range vals {
		pct := 0
		if top > 0 {
			pct = int(n * 100 / top)
			if n > 0 && pct < 2 {
				pct = 2 // a visit shows
			}
		}
		out[i] = barView{Label: labels[i], Title: titles[i], Percent: pct}
	}
	return out
}

// topCountries are the n countries with the most visitors, then the rest
// (and visitors no country is known for) as "Elsewhere".
func topCountries(counts map[string]int64, total int64, n int) []rankView {
	var rows []rankView
	for code, c := range counts {
		rows = append(rows, rankView{Name: cmp.Or(countryNames[code], code), Count: c})
	}
	slices.SortFunc(rows, func(a, b rankView) int { return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Name, b.Name)) })
	if len(rows) > n {
		rows = rows[:n]
	}
	var shown int64
	for _, r := range rows {
		shown += r.Count
	}
	if rest := total - shown; rest > 0 && len(rows) > 0 {
		rows = append(rows, rankView{Name: "Elsewhere", Count: rest, Other: true})
	}
	var top int64
	for _, r := range rows {
		top = max(top, r.Count)
	}
	for i := range rows {
		if top > 0 {
			rows[i].Percent = int(rows[i].Count * 100 / top)
		}
	}
	return rows
}

// countOf says a count of things: "1 visitor", "1,284 visitors".
func countOf(n int64, thing string) string {
	if n == 1 {
		return "1 " + thing
	}
	return units.Count(n) + " " + thing + "s"
}
