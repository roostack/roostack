// Package monitor is Roostack's monitoring (SPEC.md §6.15): every minute
// the controller checks that each service answers at its address, that
// each server answers on the lab network, that the lab's address points
// home, and the checks people add themselves; it keeps the results, says
// what's down, and summarises uptime for the pages.
//
// A check is down after three failures in a row, so one slow answer
// alerts nobody, and up again at the first success. A check that's paused
// (its service stopped on purpose, being installed or updated, its server
// not answering) counts neither way.
package monitor

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/sleep"
	"roostack.cloud/roostack/internal/store"
)

// Kinds of check.
const (
	KindService = "service" // a service's route, through the lab's Caddy
	KindServer  = "server"  // another server, over the lab network
	KindDomain  = "domain"  // the lab's address resolving home
	KindOwn     = "own"     // one people added
)

// DownAfter is how many failures in a row make a check down.
const DownAfter = 3

// Timeout is how long a check waits for an answer.
const Timeout = 10 * time.Second

// Check is one thing to check. Exactly one of URL, TCP, Ping and Given
// says how.
type Check struct {
	// Key is stable across restarts: service:<service>:<route>,
	// server:<id>, domain or own:<id>.
	Key  string
	Kind string
	// Name is what people see ("Vaultwarden", "nuc", "Router"), Address
	// what's asked, in their words (vault.smiths.roost.ac).
	Name, Address string
	// Service is the service a service check belongs to; Server names the
	// server it runs on (or the server checked).
	Service, Server string

	// URL is asked with GET; Dial, if set, is connected to instead of the
	// URL's host (the lab's Caddy on this server), the URL's host still
	// naming the site (SNI and Host).
	URL, Dial string
	// Statuses count as up; empty: any below 500.
	Statuses []int
	// SkipVerify doesn't check the certificate (a device's self-signed
	// one); the check still records its expiry.
	SkipVerify bool
	// TCP is a host:port that must accept a connection.
	TCP string
	// Ping is a host that must answer a ping.
	Ping string
	// Given is a result worked out elsewhere (the lab's DNS records, kept
	// by dnsrecords), recorded as it is.
	Given func() store.MonitorResult

	// Paused says why it isn't checked now ("" to check it).
	Paused string
	// Alert: a problem shows under "needs attention" and is emailed.
	Alert bool
	// Public: a service route open to the internet (the status page shows
	// those by default).
	Public bool
}

// States people see.
const (
	StateUp      = "up"
	StateDown    = "down"
	StatePaused  = "paused"
	StateUnknown = "unknown" // not checked yet
)

// Status is a check's state now.
type Status struct {
	Check
	State string
	// Last is the latest result.
	Last store.MonitorResult
	// Fails is how many checks in a row failed (it's down from DownAfter).
	Fails int
	// DownSince is when the first failure of the current outage was.
	DownSince time.Time
	// CertWarning: less than a fifth of the certificate's life is left, so
	// renewing it is failing (Caddy renews at a third).
	CertWarning bool
}

// CertDaysLeft is how many whole days the certificate has left (-1: no
// certificate seen).
func (s Status) CertDaysLeft(now time.Time) int {
	if s.Last.CertExpires.IsZero() {
		return -1
	}
	return int(s.Last.CertExpires.Sub(now) / (24 * time.Hour))
}

// DownText says what's wrong with a check that's down, in a sentence.
func (s Status) DownText() string {
	switch s.Kind {
	case KindService:
		return s.Name + " isn't answering at its address: " + s.Last.Err + "."
	case KindDomain:
		return "Devices at home aren't finding this server at the lab's address: " + s.Last.Err
	}
	return s.Name + " isn't answering: " + s.Last.Err + "."
}

// CertWarningText says the certificate isn't being renewed.
func (s Status) CertWarningText(now time.Time) string {
	d := s.CertDaysLeft(now)
	if d < 0 {
		return "its certificate has expired"
	}
	return "its certificate expires in " + DaysText(d) + ": renewing it is failing"
}

// DaysText is a number of whole days left: "less than a day", "1 day",
// "12 days".
func DaysText(d int) string {
	switch d {
	case 0:
		return "less than a day"
	case 1:
		return "1 day"
	}
	return strconv.Itoa(d) + " days"
}

// Store keeps results (store.Store).
type Store interface {
	AddMonitorResult(ctx context.Context, r store.MonitorResult) error
	MonitorResults(ctx context.Context, key string, since time.Time) ([]store.MonitorResult, error)
	LastMonitorResults(ctx context.Context, key string, n int) ([]store.MonitorResult, error)
	MonitorHours(ctx context.Context, key string, since time.Time) ([]store.MonitorHour, error)
	StartMonitorOutage(ctx context.Context, key string, started time.Time, why string) error
	EndMonitorOutage(ctx context.Context, key string, ended time.Time) error
	MonitorOutages(ctx context.Context, key string, since time.Time) ([]store.MonitorOutage, error)
	PruneMonitor(ctx context.Context, resultsBefore, hoursBefore time.Time) error
}

// KeepResults is how long every result is kept; hourly summaries are
// kept as long as the lab's history (KeepDays).
const KeepResults = 48 * time.Hour

// Monitor runs the checks.
type Monitor struct {
	// Checks lists what to check now.
	Checks func(ctx context.Context) []Check
	Store  Store
	Log    *slog.Logger
	// Enabled says whether monitoring is on (nil: on).
	Enabled func(ctx context.Context) bool
	// KeepDays is how long the lab keeps history (§6.14; nil: 90).
	KeepDays func(ctx context.Context) int
	// Interval between rounds (zero: a minute).
	Interval time.Duration
	// Prober runs one check (nil: the real one, Probe).
	Prober func(ctx context.Context, c Check) store.MonitorResult
	// Now is the clock (nil: time.Now).
	Now func() time.Time
	// Changed, if set, is told after each round (the status page).
	Changed func(ctx context.Context)

	mu      sync.Mutex
	checks  []Check
	states  map[string]*state
	running bool
}

// state is what the monitor remembers of a check between rounds.
type state struct {
	last      store.MonitorResult
	fails     int
	downSince time.Time
	seen      bool // a result has been recorded
	outage    bool // an outage is open in the store
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Monitor) log() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// On reports whether monitoring is switched on.
func (m *Monitor) On(ctx context.Context) bool { return m.Enabled == nil || m.Enabled(ctx) }

// Run checks every Interval until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	every := m.Interval
	if every == 0 {
		every = time.Minute
	}
	m.Round(ctx)
	sleep.Every(ctx, every, func() { m.Round(ctx) })
}

// parallel is how many checks run at once.
const parallel = 16

// Round runs every check once (a round still running is left to finish).
func (m *Monitor) Round(ctx context.Context) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()
	if !m.On(ctx) {
		m.mu.Lock()
		m.checks = nil
		m.mu.Unlock()
		return
	}
	checks := m.Checks(ctx)
	results := make([]store.MonitorResult, len(checks))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, c := range checks {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = m.probe(ctx, c)
		})
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	for i, c := range checks {
		m.record(ctx, c, results[i])
	}
	m.mu.Lock()
	m.checks = checks
	m.mu.Unlock()
	m.prune(ctx)
	if m.Changed != nil {
		m.Changed(ctx)
	}
}

// probe runs one check, or records why it's paused.
func (m *Monitor) probe(ctx context.Context, c Check) store.MonitorResult {
	var r store.MonitorResult
	switch {
	case c.Paused != "":
		r = store.MonitorResult{Paused: true, Err: c.Paused}
	case c.Given != nil:
		r = c.Given()
	case m.Prober != nil:
		r = m.Prober(ctx, c)
	default:
		r = Probe(ctx, c)
	}
	r.Key = c.Key
	if r.At.IsZero() {
		r.At = m.now()
	}
	return r
}

// record keeps a result and moves the check's state on.
func (m *Monitor) record(ctx context.Context, c Check, r store.MonitorResult) {
	// The state is rebuilt from earlier results before this one is kept.
	m.mu.Lock()
	m.stateLocked(ctx, c.Key)
	m.mu.Unlock()
	if err := m.Store.AddMonitorResult(ctx, r); err != nil {
		m.log().Error("recording a monitoring result", "check", c.Key, "err", err)
	}
	m.mu.Lock()
	st := m.stateLocked(ctx, c.Key)
	wasDown := st.fails >= DownAfter
	st.last, st.seen = r, true
	switch {
	case r.Paused:
	case r.Up:
		st.fails, st.downSince = 0, time.Time{}
	default:
		if st.fails == 0 {
			st.downSince = r.At
		}
		st.fails++
	}
	down := st.fails >= DownAfter
	startOutage, endOutage := down && !st.outage, !down && st.outage && !r.Paused
	if startOutage {
		st.outage = true
	}
	if endOutage {
		st.outage = false
	}
	since := st.downSince
	m.mu.Unlock()
	if startOutage {
		if err := m.Store.StartMonitorOutage(ctx, c.Key, since, r.Err); err != nil {
			m.log().Error("recording an outage", "check", c.Key, "err", err)
		}
	}
	if endOutage {
		if err := m.Store.EndMonitorOutage(ctx, c.Key, r.At); err != nil {
			m.log().Error("recording an outage's end", "check", c.Key, "err", err)
		}
	}
	if down && !wasDown {
		m.log().Warn("a monitored check is down", "check", c.Key, "why", r.Err)
	} else if wasDown && !down && r.Up {
		m.log().Info("a monitored check is up again", "check", c.Key)
	}
}

// stateLocked is a check's state, rebuilt from its latest results the
// first time it's needed, so a restart doesn't forget an outage. Callers
// hold mu.
func (m *Monitor) stateLocked(ctx context.Context, key string) *state {
	if m.states == nil {
		m.states = map[string]*state{}
	}
	if st := m.states[key]; st != nil {
		return st
	}
	st := &state{}
	m.states[key] = st
	last, err := m.Store.LastMonitorResults(ctx, key, DownAfter+5)
	if err != nil || len(last) == 0 {
		return st
	}
	// Newest first: count the failures since the last success.
	st.last, st.seen = last[0], true
	for _, r := range last {
		if r.Paused {
			continue
		}
		if r.Up {
			break
		}
		st.fails++
		st.downSince = r.At
	}
	if open, err := m.Store.MonitorOutages(ctx, key, time.Time{}); err == nil && len(open) > 0 && open[0].Ended.IsZero() {
		st.outage = true
		if st.fails < DownAfter {
			// It came back while Roostack wasn't running.
			st.fails = DownAfter
		}
	}
	return st
}

// prune deletes results older than KeepResults and summaries older than
// the lab's history.
func (m *Monitor) prune(ctx context.Context) {
	days := 90
	if m.KeepDays != nil {
		days = m.KeepDays(ctx)
	}
	now := m.now()
	if err := m.Store.PruneMonitor(ctx, now.Add(-KeepResults), now.AddDate(0, 0, -days)); err != nil {
		m.log().Error("pruning monitoring results", "err", err)
	}
}

// Statuses are every check's state, in the order the checks were listed.
func (m *Monitor) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Status, 0, len(m.checks))
	for _, c := range m.checks {
		out = append(out, m.statusLocked(c))
	}
	return out
}

// Status is one check's state.
func (m *Monitor) Status(key string) (Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i := slices.IndexFunc(m.checks, func(c Check) bool { return c.Key == key })
	if i < 0 {
		return Status{}, false
	}
	return m.statusLocked(m.checks[i]), true
}

// ServiceStatus is the state of a service's main check: its first route's.
func (m *Monitor) ServiceStatus(service string) (Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.checks {
		if c.Kind == KindService && c.Service == service {
			return m.statusLocked(c), true
		}
	}
	return Status{}, false
}

func (m *Monitor) statusLocked(c Check) Status {
	s := Status{Check: c, State: StateUnknown}
	st := m.states[c.Key]
	if st == nil || !st.seen {
		return s
	}
	s.Last, s.Fails, s.DownSince = st.last, st.fails, st.downSince
	switch {
	case st.last.Paused:
		s.State = StatePaused
	case st.fails >= DownAfter:
		s.State = StateDown
	default:
		s.State = StateUp
	}
	if !st.last.CertExpires.IsZero() && !st.last.CertIssued.IsZero() {
		life := st.last.CertExpires.Sub(st.last.CertIssued)
		s.CertWarning = st.last.CertExpires.Sub(m.now()) < life/5
	}
	return s
}
