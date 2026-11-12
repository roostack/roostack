package vitals

import (
	"context"
	"testing"
	"time"

	"roostack.cloud/roostack/internal/node"
	"roostack.cloud/roostack/internal/store"
)

type fakeStore struct {
	readings []store.ServerReading
	services []store.ServiceReading
	pruned   int
}

func (f *fakeStore) AddServerReading(_ context.Context, r store.ServerReading, s []store.ServiceReading) error {
	f.readings = append(f.readings, r)
	f.services = append(f.services, s...)
	return nil
}

func (f *fakeStore) PruneServerReadings(context.Context, time.Time, time.Time) error {
	f.pruned++
	return nil
}

// Each minute's new reading is kept (once), and the problems follow the
// marks (SPEC.md §6.15.1): memory under 10% free for 15 minutes, heavy
// swapping for 15 minutes, a sensor at its high mark three times running.
func TestCollector(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := start
	at := start // when the server took its latest reading
	v := node.Vitals{MemTotal: 1000, MemAvailable: 500, Services: []node.ServiceUse{{Service: "immich", Memory: 300, CPU: 12}}}
	st := &fakeStore{}
	c := &Collector{Store: st, Now: func() time.Time { return now },
		Servers: func(context.Context) []Server {
			return []Server{{ID: "n-1", Name: "nuc", Read: func(context.Context) (node.Vitals, error) { v.At = at; return v, nil }},
				{ID: "n-2", Name: "quiet", Read: func(context.Context) (node.Vitals, error) { return node.Vitals{}, nil }}}
		}}
	tick := func(minutes int) {
		for range minutes {
			at = now
			c.Round(context.Background())
			now = now.Add(time.Minute)
		}
	}
	tick(1)
	if len(st.readings) != 1 || st.readings[0].MemUsed != 500 || len(st.services) != 1 || st.pruned != 1 {
		t.Fatalf("kept %+v %+v", st.readings, st.services)
	}
	// The same reading again isn't kept twice.
	c.Round(context.Background())
	if len(st.readings) != 1 {
		t.Errorf("kept twice: %d", len(st.readings))
	}

	// Low on memory: a problem only once it's lasted 15 minutes.
	v.MemAvailable = 50
	lowFrom := now
	tick(15)
	if len(c.Problems()) != 0 {
		t.Fatalf("too soon: %+v", c.Problems())
	}
	tick(1)
	p := c.Problems()
	if len(p) != 1 || p[0].Kind != LowMemory || p[0].Name != "nuc" || !p[0].Since.Equal(lowFrom) {
		t.Fatalf("low memory: %+v (from %v)", p, lowFrom)
	}
	v.MemAvailable = 500
	tick(1)
	if len(c.Problems()) != 0 {
		t.Errorf("recovered: %+v", c.Problems())
	}

	// Hot: three readings in a row at the mark.
	v.Temp, v.TempHigh = 95, 90
	tick(2)
	if len(c.Problems()) != 0 {
		t.Error("hot too soon")
	}
	tick(1)
	if p := c.Problems(); len(p) != 1 || p[0].Kind != Hot {
		t.Errorf("hot: %+v", p)
	}
	v.Temp = 60
	tick(1)

	// Swapping heavily on average for 15 minutes.
	v.SwapIn = 2 * SwapHeavy
	tick(17)
	if p := c.Problems(); len(p) != 1 || p[0].Kind != Swapping {
		t.Errorf("swapping: %+v", p)
	}
	if l := c.Latest(); l["n-1"].SwapIn != 2*SwapHeavy || len(l) != 1 {
		t.Errorf("latest: %+v", l)
	}

	// Monitoring off: nothing kept, no problems.
	c.Enabled = func(context.Context) bool { return false }
	n := len(st.readings)
	tick(1)
	if len(st.readings) != n || len(c.Problems()) != 0 {
		t.Error("kept while monitoring is off")
	}
}
