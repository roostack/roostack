// Package vitals keeps how each server is doing (SPEC.md §6.15.1): every
// minute the controller asks each server for its latest reading
// (node.Vitals), keeps it (every reading for two days, then hourly
// averages and peaks for the lab's history), and works out what needs
// someone: memory running out, heavy swapping, a sensor too hot.
package vitals

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
)

// Server is one of the lab's servers, and how to read it.
type Server struct {
	ID, Name string
	// Services are the services placed on it: of the services it reports,
	// only these and Roostack's own parts are kept.
	Services []string
	Read     func(ctx context.Context) (node.Vitals, error)
}

// Store keeps the readings (store.Store).
type Store interface {
	AddServerReading(ctx context.Context, r store.ServerReading, services []store.ServiceReading) error
	PruneServerReadings(ctx context.Context, readingsBefore, hoursBefore time.Time) error
}

// What needs someone (the maintainer, 2026-11-12: each a problem).
const (
	// LowMemory: less than MemoryFree of memory free for Sustained.
	LowMemory = "memory"
	// Swapping: reading back from swap at SwapHeavy or more, on average,
	// for Sustained.
	Swapping = "swap"
	// Hot: a temperature at its sensor's high mark for HotReadings
	// readings in a row.
	Hot = "hot"
)

// The marks.
const (
	MemoryFree  = 0.10
	SwapHeavy   = 256 << 10 // bytes a second: 64 pages of 4 KiB
	Sustained   = 15 * time.Minute
	HotReadings = 3
	// KeepReadings is how long every reading is kept.
	KeepReadings = 48 * time.Hour
)

// Problem is something about a server that needs someone.
type Problem struct {
	Server, Name string
	Kind         string
	// Since is when it started; Reading the latest reading.
	Since   time.Time
	Reading node.Vitals
}

// Collector reads the lab's servers every minute.
type Collector struct {
	Servers func(ctx context.Context) []Server
	Store   Store
	Log     *slog.Logger
	// Enabled says whether monitoring is on (nil: on); KeepDays how long
	// the lab keeps history (nil: 90).
	Enabled  func(ctx context.Context) bool
	KeepDays func(ctx context.Context) int
	// Now is the clock (nil: time.Now).
	Now func() time.Time

	mu     sync.Mutex
	states map[string]*state
}

// state is what the collector remembers of a server between readings.
type state struct {
	name string
	// reported is when the server says it took its last reading, which
	// tells a new reading from the same one again; what's kept has the
	// controller's own time instead.
	reported time.Time
	last     node.Vitals
	recent   []node.Vitals // the last Sustained's readings, oldest first
	hot      int           // readings in a row at a sensor's high mark
	hotAt    time.Time     // when that run began
	since    map[string]time.Time
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Round reads each server once, keeps what's new, and moves its problems
// on.
func (c *Collector) Round(ctx context.Context) {
	if c.Enabled != nil && !c.Enabled(ctx) {
		c.mu.Lock()
		c.states = nil
		c.mu.Unlock()
		return
	}
	servers := c.Servers(ctx)
	seen := map[string]bool{}
	for _, srv := range servers {
		seen[srv.ID] = true
		v, err := srv.Read(ctx)
		if err != nil || v.At.IsZero() {
			continue // not answering, or no reading yet: "can't reach" says so
		}
		c.mu.Lock()
		if c.states == nil {
			c.states = map[string]*state{}
		}
		st := c.states[srv.ID]
		if st == nil {
			st = &state{since: map[string]time.Time{}}
			c.states[srv.ID] = st
		}
		st.name = srv.Name
		// What a server reports is kept only as far as the controller
		// can vouch for it: its own clock, and the services it placed
		// there (a server that's been taken over could otherwise fill
		// the database with made-up names, or with readings from the
		// future that would never be pruned).
		fresh := !v.At.Equal(st.reported)
		if fresh {
			st.reported = v.At
			v.At = c.now()
			v.Services = placed(v.Services, srv.Services)
			st.observe(v)
		}
		c.mu.Unlock()
		if fresh {
			if err := c.Store.AddServerReading(ctx, reading(srv.ID, v), services(srv.ID, v)); err != nil && c.Log != nil {
				c.Log.Error("keeping a server's reading", "server", srv.Name, "err", err)
			}
		}
	}
	c.mu.Lock()
	for id := range c.states {
		if !seen[id] {
			delete(c.states, id) // a server that left the lab
		}
	}
	c.mu.Unlock()
	days := 90
	if c.KeepDays != nil {
		days = c.KeepDays(ctx)
	}
	now := c.now()
	if err := c.Store.PruneServerReadings(ctx, now.Add(-KeepReadings), now.AddDate(0, 0, -days)); err != nil && c.Log != nil {
		c.Log.Error("pruning servers' readings", "err", err)
	}
}

// observe moves a server's state on with a new reading.
func (st *state) observe(v node.Vitals) {
	st.last = v
	st.recent = append(st.recent, v)
	for len(st.recent) > 0 && v.At.Sub(st.recent[0].At) > Sustained {
		st.recent = st.recent[1:]
	}
	if v.Temp > 0 && v.Temp >= v.TempHigh {
		if st.hot == 0 {
			st.hotAt = v.At
		}
		st.hot++
	} else {
		st.hot = 0
	}
	// Each kind's start: the first reading of a run that still holds.
	low := func(r node.Vitals) bool {
		return r.MemTotal > 0 && float64(r.MemAvailable) < MemoryFree*float64(r.MemTotal)
	}
	st.track(LowMemory, st.recent[0].At, st.lasting(low), low(v))
	var swapSum int64
	for _, r := range st.recent {
		swapSum += r.SwapIn
	}
	heavy := len(st.recent) > 0 && swapSum/int64(len(st.recent)) >= SwapHeavy
	st.track(Swapping, st.recent[0].At, heavy && st.covers(), heavy)
	st.track(Hot, st.hotAt, st.hot >= HotReadings, st.hot > 0)
}

// lasting reports whether ok held for every reading of the last
// Sustained, and the readings cover it.
func (st *state) lasting(ok func(node.Vitals) bool) bool {
	if !st.covers() {
		return false
	}
	for _, r := range st.recent {
		if !ok(r) {
			return false
		}
	}
	return true
}

// covers reports whether the recent readings span Sustained (allowing a
// minute's slack for when readings fall).
func (st *state) covers() bool {
	return len(st.recent) > 1 && st.recent[len(st.recent)-1].At.Sub(st.recent[0].At) >= Sustained-time.Minute
}

// track starts a problem when it holds, from when its condition began,
// and ends it when the condition is gone.
func (st *state) track(kind string, began time.Time, problem, still bool) {
	switch {
	case problem && st.since[kind].IsZero():
		st.since[kind] = began
	case !still:
		delete(st.since, kind)
	}
}

// Problems are what needs someone now, by server.
func (c *Collector) Problems() []Problem {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Problem
	for id, st := range c.states {
		for _, kind := range []string{LowMemory, Swapping, Hot} {
			if t := st.since[kind]; !t.IsZero() {
				out = append(out, Problem{Server: id, Name: st.name, Kind: kind, Since: t, Reading: st.last})
			}
		}
	}
	return out
}

// Latest is each server's last reading, by ID.
func (c *Collector) Latest() map[string]node.Vitals {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]node.Vitals{}
	for id, st := range c.states {
		out[id] = st.last
	}
	return out
}

// Snapshot is a server's last reading, with its problems now.
type Snapshot struct {
	ID, Name string
	Reading  node.Vitals
	Problems []string // kinds
}

// Snapshots are the servers read so far, by name.
func (c *Collector) Snapshots() []Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Snapshot
	for id, st := range c.states {
		s := Snapshot{ID: id, Name: st.name, Reading: st.last}
		for _, kind := range []string{LowMemory, Swapping, Hot} {
			if !st.since[kind].IsZero() {
				s.Problems = append(s.Problems, kind)
			}
		}
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Snapshot) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// placed are the shares of the services placed on a server, and of
// Roostack's own parts, each once.
func placed(uses []node.ServiceUse, services []string) []node.ServiceUse {
	var out []node.ServiceUse
	seen := map[string]bool{}
	for _, u := range uses {
		if seen[u.Service] || (node.OwnTitle(u.Service) == "" && !slices.Contains(services, u.Service)) {
			continue
		}
		seen[u.Service] = true
		out = append(out, u)
	}
	return out
}

func reading(server string, v node.Vitals) store.ServerReading {
	return store.ServerReading{Server: server, At: v.At, CPU: v.CPUBusy, MemUsed: v.MemoryUsed(), MemTotal: v.MemTotal,
		SwapUsed: v.SwapTotal - v.SwapFree, SwapTotal: v.SwapTotal, SwapIn: v.SwapIn, NetIn: v.NetIn, NetOut: v.NetOut, Temp: v.Temp}
}

func services(server string, v node.Vitals) []store.ServiceReading {
	var out []store.ServiceReading
	for _, u := range v.Services {
		out = append(out, store.ServiceReading{Server: server, Service: u.Service, At: v.At, Memory: u.Memory, CPU: u.CPU})
	}
	return out
}
