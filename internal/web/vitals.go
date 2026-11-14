package web

import (
	"cmp"
	"context"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"roostack.cloud/roostack/internal/attention"
	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/usererr"
	"roostack.cloud/roostack/internal/vitals"
	"roostack.cloud/roostack/internal/words"
	"roostack.cloud/roostack/units"
)

// How each server is doing (SPEC.md §6.15.1): a Servers section on
// Monitoring, and each server's page there, with charts drawn here as SVG
// (no charting library) and each service's share. Mock-ups:
// docs/mockups/server-monitoring.

// vitalsRow is a server on Monitoring.
type vitalsRow struct {
	ID, Name, Sub string
	// CPU, Memory and Swap are percentages for their meters (Swap -1 for
	// none), with what they say beside them.
	CPU, Memory, Swap      int
	MemoryText, SwapText   string
	Temp                   string
	Problem                string
	MemoryLevel, SwapLevel string
}

// vitalsRows are the servers read so far.
func (s *Server) vitalsRows() []vitalsRow {
	if s.Vitals == nil {
		return nil
	}
	var rows []vitalsRow
	for _, sn := range s.Vitals.Snapshots() {
		v := sn.Reading
		r := vitalsRow{ID: sn.ID, Name: sn.Name, Sub: serverSummary(v), CPU: int(math.Round(v.CPUBusy)),
			Memory: percent(v.MemoryUsed(), v.MemTotal), MemoryText: gbOf(v.MemoryUsed(), v.MemTotal), Swap: -1, Temp: tempText(v)}
		if v.SwapTotal > 0 {
			r.Swap, r.SwapText = percent(v.SwapTotal-v.SwapFree, v.SwapTotal), gbOf(v.SwapTotal-v.SwapFree, v.SwapTotal)
		}
		if r.Memory >= 90 {
			r.MemoryLevel = "warn"
		}
		if r.Swap >= 50 {
			r.SwapLevel = "warn"
		}
		if len(sn.Problems) > 0 {
			r.Problem = map[string]string{vitals.LowMemory: "Low on memory", vitals.Swapping: "Swapping heavily", vitals.Hot: "Too hot"}[sn.Problems[0]]
		}
		rows = append(rows, r)
	}
	return rows
}

// serverSummary says what a server is: its processors and memory.
func serverSummary(v node.Vitals) string {
	return fmt.Sprintf("%s · %s of memory", words.Count(int64(v.CPUs), "processor", "processors"), units.Bytes(v.MemTotal))
}

func percent(part, whole int64) int {
	if whole <= 0 {
		return 0
	}
	return int(math.Round(100 * float64(part) / float64(whole)))
}

// gbOf says "5.1 of 16 GB".
func gbOf(part, whole int64) string {
	return fmt.Sprintf("%.1f of %s", float64(part)/float64(units.GB), units.Bytes(whole))
}

func tempText(v node.Vitals) string {
	if v.Temp <= 0 {
		return ""
	}
	return fmt.Sprintf("%.0f °C", v.Temp)
}

// vitalsRanges are how far back a server's charts go.
var vitalsRanges = []struct{ ID, Title string }{{"day", "Day"}, {"week", "Week"}, {"month", "Month"}}

// serviceShare is a service's share of a server over the last hour.
type serviceShare struct {
	Name, Title, Href   string
	Memory, CPU         int // percentages of the server's memory, and of all its processors
	MemoryText, CPUText string
}

// serverVitalsPage is a server's page on Monitoring.
func (s *Server) serverVitalsPage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("server")
	var sn *vitals.Snapshot
	if s.Vitals != nil {
		for _, x := range s.Vitals.Snapshots() {
			if x.ID == id {
				sn = &x
			}
		}
	}
	if sn == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	rng := r.URL.Query().Get("range")
	if !slices.ContainsFunc(vitalsRanges, func(x struct{ ID, Title string }) bool { return x.ID == rng }) {
		rng = "day"
	}
	v := sn.Reading
	data := s.pageData(r)
	data["Server"], data["Reading"], data["Range"], data["Ranges"] = sn, v, rng, vitalsRanges
	data["Sub"] = fmt.Sprintf("%s · %s · up for %s", cmp.Or(v.Model, "Processors"), serverSummary(v), words.RoughDuration(v.Up))
	data["MemoryPercent"], data["SwapPercent"] = percent(v.MemoryUsed(), v.MemTotal), percent(v.SwapTotal-v.SwapFree, v.SwapTotal)
	data["Charts"] = s.vitalsCharts(ctx, id, v, rng, time.Now())
	data["Shares"] = s.serviceShares(ctx, id, v, time.Now())
	data["Problems"] = s.serverProblems(ctx, id)
	if s.Glances != nil {
		data["GlancesSwitch"] = true
		data["GlancesOn"], data["GlancesURL"] = s.Glances.GlancesOn(id), s.Glances.GlancesURL(id)
		data["HasAddress"] = s.Domain != ""
		switch g := r.URL.Query().Get("glances"); g {
		case "":
		case "on":
			data["GlancesDone"] = "Turned on. Roostack is downloading Glances and starting it, which takes a minute or two the first time."
		case "off":
			data["GlancesDone"] = "Turned off. Roostack is stopping Glances."
		default:
			data["GlancesError"] = g
		}
	}
	s.render(w, "monitoring-server", data)
}

// serverProblems are the server's problems now, as Needs attention says
// them.
func (s *Server) serverProblems(ctx context.Context, id string) []string {
	var out []string
	titles := s.serviceTitles(ctx)
	for _, p := range s.Vitals.Problems() {
		if p.Server == id {
			out = append(out, attention.VitalsText(p, titles, time.Now()))
		}
	}
	slices.Sort(out)
	return out
}

// serviceShares are each service's memory and processors over the last
// hour, most memory first: a full bar is the server's whole memory, or all
// its processors busy.
func (s *Server) serviceShares(ctx context.Context, id string, v node.Vitals, now time.Time) []serviceShare {
	readings, err := s.Store.ServiceReadings(ctx, id, now.Add(-time.Hour))
	if err != nil {
		s.Log.Error("reading services' shares", "err", err)
		return nil
	}
	type sum struct {
		mem int64
		cpu float64
		n   int
	}
	sums := map[string]*sum{}
	for _, u := range readings {
		x := sums[u.Service]
		if x == nil {
			x = &sum{}
			sums[u.Service] = x
		}
		x.mem, x.cpu, x.n = x.mem+u.Memory, x.cpu+u.CPU, x.n+1
	}
	titles := s.serviceTitles(ctx)
	var out []serviceShare
	cpus := float64(max(v.CPUs, 1))
	for name, x := range sums {
		mem, cpu := x.mem/int64(x.n), x.cpu/float64(x.n)
		sh := serviceShare{Name: name, Title: cmp.Or(titles[name], name), Memory: percent(mem, v.MemTotal),
			CPU: int(math.Round(cpu / cpus)), MemoryText: units.Bytes(mem), CPUText: fmt.Sprintf("%.1f%%", cpu/cpus)}
		sh.MemoryText = fmt.Sprintf("%s · %d%%", sh.MemoryText, sh.Memory)
		if name == "" {
			sh.Title = "Roostack and Caddy"
		} else {
			sh.Href = "/services/" + name
		}
		out = append(out, sh)
	}
	slices.SortFunc(out, func(a, b serviceShare) int { return cmp.Compare(b.Memory, a.Memory) })
	return out
}

// vitalsChart is one chart on a server's page.
type vitalsChart struct {
	Title, Peak string
	SVG         template.HTML
	// Key names its two lines, when it has a second.
	Key [2]string
}

// slot is a point on a chart: an average over a stretch of time, and its
// peak, or nothing (the server didn't report).
type slot struct {
	At            time.Time
	Avg, Max      [5]float64 // cpu, memory, swap, net in, net out
	Temp, TempMax float64
	N             int
}

// vitalsCharts draws a server's charts for a range.
func (s *Server) vitalsCharts(ctx context.Context, id string, v node.Vitals, rng string, now time.Time) []vitalsChart {
	var slots []slot
	var from time.Time
	var labels []string
	switch rng {
	case "week":
		from = now.AddDate(0, 0, -7)
		slots = s.hourSlots(ctx, id, from, now, time.Hour)
		for i := 0; i <= 4; i++ {
			labels = append(labels, from.Add(time.Duration(i)*7*24*time.Hour/4).Local().Format("Mon 15:04"))
		}
	case "month":
		from = now.AddDate(0, 0, -30)
		slots = s.hourSlots(ctx, id, from, now, 4*time.Hour)
		for i := 0; i <= 4; i++ {
			labels = append(labels, from.Add(time.Duration(i)*30*24*time.Hour/4).Local().Format("2 Jan"))
		}
	default:
		from = now.Add(-24 * time.Hour)
		slots = s.readingSlots(ctx, id, from, now, 5*time.Minute)
		for i := 0; i <= 4; i++ {
			labels = append(labels, from.Add(time.Duration(i)*6*time.Hour).Local().Format("15:04"))
		}
	}
	labels[4] = "Now"
	series := func(i int) []float64 {
		out := make([]float64, len(slots))
		for k, sl := range slots {
			out[k] = math.NaN()
			if sl.N > 0 {
				out[k] = sl.Avg[i]
			}
		}
		return out
	}
	temps := make([]float64, len(slots))
	for k, sl := range slots {
		temps[k] = math.NaN()
		if sl.N > 0 && sl.Temp > 0 {
			temps[k] = sl.Temp
		}
	}
	peak := func(i int) (float64, time.Time) {
		best, at := -1.0, time.Time{}
		for _, sl := range slots {
			if sl.N > 0 && sl.Max[i] > best {
				best, at = sl.Max[i], sl.At
			}
		}
		return best, at
	}
	when := func(t time.Time) string {
		if rng == "day" {
			return t.Local().Format("15:04")
		}
		return words.When(t)
	}
	var charts []vitalsChart
	if p, at := peak(0); p >= 0 {
		charts = append(charts, vitalsChart{Title: "Processors", Peak: fmt.Sprintf("busiest %.0f%% at %s", p, when(at)),
			SVG: chartSVG("Processors", 100, [2]string{"100%", "50%"}, labels, series(0))})
	}
	if p, at := peak(1); p >= 0 && v.MemTotal > 0 {
		top := float64(v.MemTotal)
		charts = append(charts, vitalsChart{Title: "Memory used", Peak: fmt.Sprintf("most %s at %s", units.Bytes(int64(p)), when(at)),
			SVG: chartSVG("Memory used", top, [2]string{units.Bytes(v.MemTotal), units.Bytes(v.MemTotal / 2)}, labels, series(1), series(2)),
			Key: [2]string{"Memory", "Swap"}})
	}
	if p, at := peak(3); p >= 0 {
		pOut, _ := peak(4)
		top := niceTop(max(p, pOut))
		charts = append(charts, vitalsChart{Title: "Network", Peak: fmt.Sprintf("most %s/s in at %s", units.Bytes(int64(p)), when(at)),
			SVG: chartSVG("Network", top, [2]string{units.Bytes(int64(top)) + "/s", units.Bytes(int64(top/2)) + "/s"}, labels, series(3), series(4)),
			Key: [2]string{"In", "Out"}})
	}
	if v.Temp > 0 {
		hot, at := -1.0, time.Time{}
		for _, sl := range slots {
			if sl.N > 0 && sl.TempMax > hot {
				hot, at = sl.TempMax, sl.At
			}
		}
		top := math.Max(100, v.TempHigh)
		c := vitalsChart{Title: "Temperature", SVG: chartSVG("Temperature", top, [2]string{fmt.Sprintf("%.0f °C", top), fmt.Sprintf("%.0f °C", top/2)}, labels, temps)}
		if hot > 0 {
			c.Peak = fmt.Sprintf("hottest %.0f °C at %s", hot, when(at))
		}
		charts = append(charts, c)
	}
	return charts
}

// readingSlots averages a server's readings into slots of a length.
func (s *Server) readingSlots(ctx context.Context, id string, from, to time.Time, length time.Duration) []slot {
	slots := emptySlots(from, to, length)
	readings, err := s.Store.ServerReadings(ctx, id, from)
	if err != nil {
		s.Log.Error("reading a server's readings", "err", err)
	}
	for _, r := range readings {
		k := int(r.At.Sub(from) / length)
		if k < 0 || k >= len(slots) {
			continue
		}
		vals := [5]float64{r.CPU, float64(r.MemUsed), float64(r.SwapUsed), float64(r.NetIn), float64(r.NetOut)}
		slots[k].add(vals, vals, r.Temp, r.Temp, 1)
	}
	return finish(slots)
}

// hourSlots averages a server's hourly summaries into slots of a length.
func (s *Server) hourSlots(ctx context.Context, id string, from, to time.Time, length time.Duration) []slot {
	slots := emptySlots(from, to, length)
	hours, err := s.Store.ServerHours(ctx, id, from)
	if err != nil {
		s.Log.Error("reading a server's hours", "err", err)
	}
	for _, h := range hours {
		k := int(h.Hour.Sub(from) / length)
		if k < 0 || k >= len(slots) {
			continue
		}
		avg := [5]float64{h.CPU, float64(h.Mem), float64(h.Swap), float64(h.NetIn), float64(h.NetOut)}
		peak := [5]float64{h.CPUMax, float64(h.MemMax), float64(h.SwapMax), float64(h.NetInMax), float64(h.NetOutMax)}
		slots[k].add(avg, peak, h.Temp, h.TempMax, h.Readings)
	}
	return finish(slots)
}

func emptySlots(from, to time.Time, length time.Duration) []slot {
	n := int(to.Sub(from) / length)
	slots := make([]slot, n)
	for i := range slots {
		slots[i].At = from.Add(time.Duration(i) * length)
	}
	return slots
}

// add sums a reading (or an hour of them, n) into a slot: totals in Avg
// until finish divides them.
func (sl *slot) add(avg, peak [5]float64, temp, tempMax float64, n int) {
	for i := range avg {
		sl.Avg[i] += avg[i] * float64(n)
		sl.Max[i] = math.Max(sl.Max[i], peak[i])
	}
	sl.Temp += temp * float64(n)
	sl.TempMax = math.Max(sl.TempMax, tempMax)
	sl.N += n
}

func finish(slots []slot) []slot {
	for i := range slots {
		if n := float64(slots[i].N); n > 0 {
			for k := range slots[i].Avg {
				slots[i].Avg[k] /= n
			}
			slots[i].Temp /= n
		}
	}
	return slots
}

// niceTop is a chart's top for values up to v: 1, 2, or 5 times a power
// of ten, at least 1 MB/s's worth so a quiet network isn't all noise.
func niceTop(v float64) float64 {
	v = math.Max(v, 1<<20)
	p := math.Pow(10, math.Floor(math.Log10(v)))
	for _, m := range []float64{1, 2, 5, 10} {
		if m*p >= v {
			return m * p
		}
	}
	return 10 * p
}

// chartSVG draws a chart of up to two lines over a scale from 0 to top,
// with the scale beside it (top and middle; the bottom is 0) and times
// below. A NaN is a gap: the server didn't report.
func chartSVG(label string, top float64, yLabels [2]string, xLabels []string, lines ...[]float64) template.HTML {
	const w, h = 600.0, 70.0
	var b strings.Builder
	esc := template.HTMLEscapeString
	fmt.Fprintf(&b, `<div class="chart-box"><div class="chart-y" aria-hidden="true"><span>%s</span><span>%s</span><span>0</span></div><div class="chart-plot">`,
		esc(yLabels[0]), esc(yLabels[1]))
	fmt.Fprintf(&b, `<svg class="chart" viewBox="0 0 %.0f %.0f" preserveAspectRatio="none" role="img" aria-label="%s">`, w, h, esc(label))
	for _, g := range []float64{0, 0.5, 1} {
		fmt.Fprintf(&b, `<line class="chart-grid" x1="0" x2="%.0f" y1="%.1f" y2="%.1f"/>`, w, h*g, h*g)
	}
	for k := 1; k < 4; k++ {
		fmt.Fprintf(&b, `<line class="chart-grid" y1="0" y2="%.0f" x1="%.1f" x2="%.1f"/>`, h, w*float64(k)/4, w*float64(k)/4)
	}
	for li, vals := range lines {
		n := len(vals)
		if n < 2 {
			continue
		}
		var run []string
		flush := func() {
			if len(run) > 1 {
				if li == 0 {
					first, last := strings.Split(run[0], ","), strings.Split(run[len(run)-1], ",")
					fmt.Fprintf(&b, `<polygon class="chart-area" points="%s,%.0f %s %s,%.0f"/>`, first[0], h, strings.Join(run, " "), last[0], h)
				}
				class := "chart-line"
				if li > 0 {
					class += " second"
				}
				fmt.Fprintf(&b, `<polyline class="%s" points="%s"/>`, class, strings.Join(run, " "))
			}
			run = nil
		}
		for i, v := range vals {
			if math.IsNaN(v) {
				flush()
				continue
			}
			y := h - math.Min(v/top, 1)*h
			run = append(run, fmt.Sprintf("%.1f,%.1f", float64(i)*w/float64(n-1), y))
		}
		flush()
	}
	b.WriteString(`</svg><div class="chart-x" aria-hidden="true">`)
	for _, l := range xLabels {
		fmt.Fprintf(&b, `<span>%s</span>`, esc(l))
	}
	b.WriteString(`</div></div></div>`)
	return template.HTML(b.String())
}

// GlancesSwitch turns servers' live views on and off (the manager, SPEC.md
// §6.15.1).
type GlancesSwitch interface {
	GlancesOn(server string) bool
	SetGlances(server string, on bool) error
	GlancesURL(server string) string
}

// glancesSubmit turns a server's live view on or off.
func (s *Server) glancesSubmit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("server")
	if s.Glances == nil {
		http.NotFound(w, r)
		return
	}
	on := r.FormValue("on") == "yes"
	if err := s.Glances.SetGlances(id, on); err != nil {
		if msg, ok := usererr.Message(err); ok {
			redirect(w, r, "/monitoring/servers/"+id+"?glances="+url.QueryEscape(msg)+"#live-view")
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "settings", map[bool]string{true: "turned on", false: "turned off"}[on]+" the live view of "+id)
	redirect(w, r, "/monitoring/servers/"+id+"?glances="+map[bool]string{true: "on", false: "off"}[on]+"#live-view")
}
