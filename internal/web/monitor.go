package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"roostack.cloud/roostack/internal/caddy"
	"roostack.cloud/roostack/internal/dnsrecords"
	"roostack.cloud/roostack/internal/monitor"
	"roostack.cloud/roostack/internal/store"
)

// Monitoring (SPEC.md §6.15): the Monitoring page, a check's Details,
// people's own checks, the switch and the status page's settings, and
// what's down under "needs attention". The checks themselves run in
// internal/monitor; the services' and servers' come from the manager
// (AutoChecks), the lab's address and people's own from here.

// Runtime settings.
const (
	monitorOffKey  = "monitor.off"       // "yes": monitoring switched off
	statusShownKey = "status_page.shown" // JSON: check key → shown, where the Owner chose
)

// StatusPageSwitch turns the public status page on and off (the manager:
// lab.yaml, Caddy, its public name).
type StatusPageSwitch interface {
	StatusPageOn() bool
	SetStatusPage(on bool) error
}

// statusPageOn says whether the public status page is on.
func (s *Server) statusPageOn() bool { return s.StatusPage != nil && s.StatusPage.StatusPageOn() }

// MonitorOn says whether monitoring is switched on (the monitor asks).
func (s *Server) MonitorOn(ctx context.Context) bool {
	v, _ := s.Store.Setting(ctx, monitorOffKey)
	return v != "yes"
}

// HistoryKeepDays is how long the lab keeps history (§6.14), which
// monitoring's hourly summaries follow.
func (s *Server) HistoryKeepDays(ctx context.Context) int { return s.historyKeepDays(ctx) }

// MonitorChecks lists everything to check: the manager's (services and
// servers), the lab's address, and people's own.
func (s *Server) MonitorChecks(ctx context.Context) []monitor.Check {
	var out []monitor.Check
	if s.AutoChecks != nil {
		out = s.AutoChecks(ctx)
	}
	if s.DNS != nil {
		if st := s.DNS.Status(); st.Record != "" {
			out = append(out, monitor.Check{Key: "domain", Kind: monitor.KindDomain, Name: "The lab's address",
				Address: st.Record + " points at your home", Given: s.domainResult,
				// "Needs attention" has the address's own items (attention.go).
				Alert: false})
		}
	}
	own, err := s.Store.MonitorChecks(ctx)
	if err != nil {
		s.Log.Error("listing monitoring checks", "err", err)
	}
	for _, c := range own {
		out = append(out, ownCheck(c))
	}
	return out
}

// ownCheck is the check for someone's own (an address that no longer
// parses is still listed, failing with why).
func ownCheck(c store.MonitorCheck) monitor.Check {
	chk, err := monitor.ParseAddress(c.Address)
	chk.Key, chk.Kind, chk.Name, chk.Address = "own:"+strconv.FormatInt(c.ID, 10), monitor.KindOwn, c.Name, c.Address
	chk.SkipVerify, chk.Alert = c.SkipVerify, c.Alert
	if err != nil {
		chk.Given = func() store.MonitorResult { return store.MonitorResult{Err: err.Error()} }
	}
	return chk
}

// domainResult is how the lab's address stands, from the DNS records'
// own checks (dnsrecords, every few minutes).
func (s *Server) domainResult() store.MonitorResult {
	st := s.DNS.Status()
	if st.Checked.IsZero() {
		return store.MonitorResult{Paused: true, Err: "Roostack hasn't checked it yet"}
	}
	if st.Reachable || st.State == dnsrecords.StateOK {
		return store.MonitorResult{Up: true}
	}
	why := st.Message
	if why == "" {
		why = "devices at home don't find this server at it"
	}
	return store.MonitorResult{Err: why}
}

// checkRow is one check in a list.
type checkRow struct {
	monitor.Status
	// Down says what's wrong, while it's down.
	Down  string
	Days  []monitor.Day
	Month monitor.Uptime
	// Line is the small line under its name: where, how fast, or what's
	// wrong.
	Line string
	Href string
}

// checkLine says where a check is and how it's doing, in a few words.
func checkLine(st monitor.Status, now time.Time) string {
	var parts []string
	if st.Address != "" {
		parts = append(parts, st.Address)
	}
	if st.Kind == monitor.KindService && st.Server != "" {
		parts = append(parts, "on "+st.Server)
	}
	switch st.State {
	case monitor.StateDown:
		parts = append(parts, "not answering for "+durationText(now.Sub(st.DownSince)))
	case monitor.StatePaused:
		parts = append(parts, "paused: "+st.Last.Err)
	case monitor.StateUp:
		if st.Last.Millis > 0 {
			parts = append(parts, strconv.Itoa(st.Last.Millis)+" ms")
		}
	}
	if st.CertWarning && st.State != monitor.StateDown {
		parts = append(parts, certWarningText(st, now))
	}
	return strings.Join(parts, " · ")
}

// downText says what's wrong with a check that's down, in a sentence.
func downText(st monitor.Status) string {
	switch st.Kind {
	case monitor.KindService:
		return st.Name + " isn't answering at its address: " + st.Last.Err + "."
	case monitor.KindDomain:
		return "Devices at home don't find this server at the lab's address: " + st.Last.Err
	}
	return st.Name + " isn't answering: " + st.Last.Err + "."
}

// certWarningText says a certificate isn't being renewed.
func certWarningText(st monitor.Status, now time.Time) string {
	d := st.CertDaysLeft(now)
	if d < 0 {
		return "its certificate has expired"
	}
	return "its certificate expires in " + daysText(d) + ": renewing it is failing"
}

// daysText is a number of whole days left: "less than a day", "1 day",
// "12 days".
func daysText(d int) string {
	switch d {
	case 0:
		return "less than a day"
	case 1:
		return "1 day"
	}
	return strconv.Itoa(d) + " days"
}

// durationText is a length of time for people: "6 minutes", "3 hours",
// "2 days".
func durationText(d time.Duration) string {
	plural := func(n int, unit string) string {
		if n == 1 {
			return "1 " + unit
		}
		return strconv.Itoa(n) + " " + unit + "s"
	}
	switch {
	case d < 2*time.Minute:
		return "a minute"
	case d < 2*time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/(24*time.Hour)), "day")
}

func checkHref(key string) string { return "/monitoring/check/" + key }

// rows makes list rows of statuses, with their last 30 days.
func (s *Server) checkRows(ctx context.Context, statuses []monitor.Status) []checkRow {
	now := time.Now()
	out := make([]checkRow, 0, len(statuses))
	for _, st := range statuses {
		row := checkRow{Status: st, Line: checkLine(st, now), Href: checkHref(st.Key), Month: -1}
		if st.State == monitor.StateDown {
			row.Down = downText(st)
		}
		if h, err := s.Monitor.History(ctx, st.Key, 30, false); err == nil {
			row.Days, row.Month = h.Days, h.Month
		} else {
			s.Log.Error("reading a check's history", "check", st.Key, "err", err)
		}
		out = append(out, row)
	}
	return out
}

// statusChoice is a check the status page could show.
type statusChoice struct {
	Key, Name, Hint string
	Shown           bool
}

// statusShown is which checks the status page shows: what the Owner chose,
// otherwise services open to the internet (SPEC.md §6.15).
func (s *Server) statusShown(ctx context.Context, statuses []monitor.Status) map[string]bool {
	chosen := map[string]bool{}
	if v, _ := s.Store.Setting(ctx, statusShownKey); v != "" {
		json.Unmarshal([]byte(v), &chosen)
	}
	out := map[string]bool{}
	for _, st := range statuses {
		if v, ok := chosen[st.Key]; ok {
			out[st.Key] = v
		} else {
			out[st.Key] = st.Kind == monitor.KindService && st.Public
		}
	}
	return out
}

// statusChoices are the checks the status page can show: services and
// people's own.
func (s *Server) statusChoices(ctx context.Context, statuses []monitor.Status) []statusChoice {
	shown := s.statusShown(ctx, statuses)
	var out []statusChoice
	for _, st := range statuses {
		c := statusChoice{Key: st.Key, Name: st.Name, Shown: shown[st.Key]}
		switch {
		case st.Kind == monitor.KindService && st.Public:
			c.Hint = "open to the internet"
		case st.Kind == monitor.KindService:
			c.Hint = "only on your home network: showing it tells everyone you run it"
		case st.Kind == monitor.KindOwn:
			c.Hint = "your own check"
		default:
			continue
		}
		out = append(out, c)
	}
	return out
}

// statusHost is the public status page's address (SPEC.md §6.15), or ""
// while the lab has none.
func (s *Server) statusHost() string {
	if s.Domain == "" {
		return ""
	}
	return caddy.StatusSubdomain + "." + s.Domain
}

func (s *Server) monitoringPage(w http.ResponseWriter, r *http.Request) {
	s.renderMonitoring(w, r, "", nil)
}

// renderMonitoring shows the Monitoring page, with a form's error and
// values when adding a check failed.
func (s *Server) renderMonitoring(w http.ResponseWriter, r *http.Request, formErr string, form map[string]string) {
	if s.Monitor == nil {
		http.NotFound(w, r)
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	ctx := r.Context()
	statuses := s.Monitor.Statuses()
	var services, servers, own []monitor.Status
	for _, st := range statuses {
		switch st.Kind {
		case monitor.KindService:
			services = append(services, st)
		case monitor.KindServer, monitor.KindDomain:
			servers = append(servers, st)
		case monitor.KindOwn:
			own = append(own, st)
		}
	}
	// People's own checks show from the moment they're added, before
	// their first round.
	if checks, err := s.Store.MonitorChecks(ctx); err == nil {
		for _, c := range checks {
			key := "own:" + strconv.FormatInt(c.ID, 10)
			if !slices.ContainsFunc(own, func(st monitor.Status) bool { return st.Key == key }) {
				own = append(own, monitor.Status{Check: ownCheck(c), State: monitor.StateUnknown})
			}
		}
	}
	data["On"] = s.Monitor.On(ctx)
	data["Services"], data["Servers"], data["Own"] = s.checkRows(ctx, services), s.checkRows(ctx, servers), s.checkRows(ctx, own)
	var down []checkRow
	for _, row := range slices.Concat(data["Services"].([]checkRow), data["Servers"].([]checkRow), data["Own"].([]checkRow)) {
		if row.State == monitor.StateDown {
			down = append(down, row)
		}
	}
	data["Down"] = down
	if s.Node != nil {
		data["CheckedFrom"] = s.Node.Identity().Name
	}
	if s.StatusPage != nil {
		data["StatusHost"] = s.statusHost()
	}
	data["StatusOn"] = s.statusPageOn()
	data["StatusChoices"] = s.statusChoices(ctx, append(slices.Clone(services), own...))
	data["FormError"], data["Form"] = formErr, form
	// The add form's dialog opens with a mistake to fix, or from ?add=check
	// (its button, without scripts).
	data["Adding"] = formErr != "" || r.URL.Query().Get("add") == "check"
	s.render(w, "monitoring", data)
}

// monitorCheckPage is one check's Details.
func (s *Server) monitorCheckPage(w http.ResponseWriter, r *http.Request) {
	if s.Monitor == nil {
		http.NotFound(w, r)
		return
	}
	key := r.PathValue("key")
	st, ok := s.Monitor.Status(key)
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := s.pageData(r)
	if err != nil {
		s.serverError(w, err)
		return
	}
	h, err := s.Monitor.History(r.Context(), key, 90, true)
	if err != nil {
		s.serverError(w, err)
		return
	}
	now := time.Now()
	data["Check"], data["History"], data["Line"] = st, h, checkLine(st, now)
	data["Spark"] = sparkPoints(h.Recent, 600, 80)
	// The latest few, newest first.
	var latest []store.MonitorResult
	for i := len(h.Recent) - 1; i >= 0 && len(latest) < 8; i-- {
		latest = append(latest, h.Recent[i])
	}
	data["Latest"] = latest
	type outage struct {
		store.MonitorOutage
		For string
	}
	var outages []outage
	for _, o := range h.Outages {
		end := o.Ended
		if end.IsZero() {
			end = now
		}
		outages = append(outages, outage{o, durationText(end.Sub(o.Started))})
	}
	data["Outages"] = outages
	if st.Kind == monitor.KindOwn {
		data["OwnID"] = strings.TrimPrefix(key, "own:")
	}
	if s.Node != nil {
		data["CheckedFrom"] = s.Node.Identity().Name
	}
	data["CertDays"] = st.CertDaysLeft(now)
	if st.Kind == monitor.KindService && s.Manager != nil {
		data["ServiceHref"] = "/services/" + st.Service
	}
	s.render(w, "monitoring-check", data)
}

// sparkPoints is an SVG polyline's points for the answer times of the
// results up (w by h, the slowest at the top).
func sparkPoints(results []store.MonitorResult, w, h int) string {
	var ups []store.MonitorResult
	slowest := 1
	for _, r := range results {
		if r.Up {
			ups = append(ups, r)
			slowest = max(slowest, r.Millis)
		}
	}
	if len(ups) < 2 {
		return ""
	}
	var b strings.Builder
	for i, r := range ups {
		x := float64(i) * float64(w) / float64(len(ups)-1)
		y := float64(h) - float64(r.Millis)*float64(h-6)/float64(slowest)
		fmt.Fprintf(&b, "%.1f,%.1f ", x, y)
	}
	return strings.TrimSpace(b.String())
}

// maxCheckName is the longest name a check can have.
const maxCheckName = 60

// monitorAddSubmit adds someone's own check.
func (s *Server) monitorAddSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Monitor == nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	name, addr := strings.TrimSpace(r.FormValue("name")), strings.TrimSpace(r.FormValue("address"))
	form := map[string]string{"name": name, "address": addr, "skip_verify": r.FormValue("skip_verify"), "alert": r.FormValue("alert")}
	if name == "" || utf8.RuneCountInString(name) > maxCheckName {
		s.renderMonitoring(w, r, fmt.Sprintf("Give the check a name of up to %d characters, e.g. Router.", maxCheckName), form)
		return
	}
	if _, err := monitor.ParseAddress(addr); err != nil {
		s.renderMonitoring(w, r, err.Error(), form)
		return
	}
	c := store.MonitorCheck{Name: name, Address: addr, SkipVerify: r.FormValue("skip_verify") == "on", Alert: r.FormValue("alert") == "on"}
	if _, err := s.Store.AddMonitorCheck(r.Context(), c); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "monitoring", "added the check "+name+" ("+addr+")")
	go s.Monitor.Round(context.WithoutCancel(r.Context()))
	redirect(w, r, "/monitoring#own")
}

// monitorRemoveSubmit removes someone's own check, and what was kept of it.
func (s *Server) monitorRemoveSubmit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.Monitor == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.Store.RemoveMonitorCheck(r.Context(), id); err != nil {
		if err == store.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		s.serverError(w, err)
		return
	}
	s.Store.ForgetMonitorCheck(r.Context(), "own:"+strconv.FormatInt(id, 10))
	s.Store.Audit(r.Context(), currentUser(r), "monitoring", "removed a check of their own")
	go s.Monitor.Round(context.WithoutCancel(r.Context()))
	redirect(w, r, "/monitoring#own")
}

// monitorSwitchSubmit turns monitoring on or off.
func (s *Server) monitorSwitchSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Monitor == nil {
		http.NotFound(w, r)
		return
	}
	on := r.FormValue("on") == "yes"
	v := "yes"
	if on {
		v = ""
	}
	if err := s.Store.SetSetting(r.Context(), monitorOffKey, v); err != nil {
		s.serverError(w, err)
		return
	}
	s.Store.Audit(r.Context(), currentUser(r), "monitoring", map[bool]string{true: "turned monitoring on", false: "turned monitoring off"}[on])
	go s.Monitor.Round(context.WithoutCancel(r.Context()))
	redirect(w, r, "/monitoring")
}

// statusPageSubmit saves the status page's settings: on or off, and which
// checks it shows.
func (s *Server) statusPageSubmit(w http.ResponseWriter, r *http.Request) {
	if s.Monitor == nil {
		http.NotFound(w, r)
		return
	}
	r.ParseForm()
	ctx := r.Context()
	on := r.FormValue("on") == "yes"
	if r.FormValue("on") == "" { // the list's Save keeps it as it is
		on = s.statusPageOn()
	}
	shown := map[string]bool{}
	if r.FormValue("choices") == "yes" {
		for _, c := range s.statusChoices(ctx, s.Monitor.Statuses()) {
			shown[c.Key] = slices.Contains(r.Form["show"], c.Key)
		}
		b, _ := json.Marshal(shown)
		if err := s.Store.SetSetting(ctx, statusShownKey, string(b)); err != nil {
			s.serverError(w, err)
			return
		}
	}
	if s.StatusPage != nil && on != s.StatusPage.StatusPageOn() {
		err := s.StatusPage.SetStatusPage(on)
		if msg, ok := userMessage(err); ok {
			s.renderMonitoring(w, r, msg, nil)
			return
		}
		if err != nil {
			s.serverError(w, err)
			return
		}
	}
	s.Store.Audit(ctx, currentUser(r), "monitoring", map[bool]string{true: "saved the public status page (on)", false: "saved the public status page (off)"}[on])
	s.WriteStatusPage(ctx)
	redirect(w, r, "/monitoring#status-page")
}

// monitorAttention adds what monitoring found down to "needs attention"
// (SPEC.md §6.15): a check down after three failures, a certificate not
// being renewed. Servers aren't repeated: "can't reach" says so already.
func (s *Server) monitorAttention(add func(attentionItem)) {
	if s.Monitor == nil {
		return
	}
	now := time.Now()
	for _, st := range s.Monitor.Statuses() {
		if !st.Alert {
			continue
		}
		switch {
		case st.State == monitor.StateDown:
			add(attentionItem{Level: levelProblem, Key: "monitor:" + st.Key,
				Text: downText(st), Link: checkHref(st.Key), LinkText: "See what's wrong"})
		case st.CertWarning && st.State == monitor.StateUp:
			add(attentionItem{Level: levelWarning, Key: "monitor-cert:" + st.Key,
				Text: capitalise(st.Name + "'s address: " + certWarningText(st, now) + "."), Link: checkHref(st.Key), LinkText: "Details"})
		}
	}
}

// serviceUptime is a service's Uptime card, or nil without a check.
type serviceUptime struct {
	Status   monitor.Status
	History  monitor.History
	CertDays int
	Href     string
}

func (s *Server) serviceUptime(ctx context.Context, service string) *serviceUptime {
	if s.Monitor == nil || !s.Monitor.On(ctx) {
		return nil
	}
	st, ok := s.Monitor.ServiceStatus(service)
	if !ok {
		return nil
	}
	h, err := s.Monitor.History(ctx, st.Key, 30, false)
	if err != nil {
		s.Log.Error("reading a service's uptime", "service", service, "err", err)
		return nil
	}
	return &serviceUptime{Status: st, History: h, CertDays: st.CertDaysLeft(time.Now()), Href: checkHref(st.Key)}
}
