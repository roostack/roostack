package node

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseMeminfo(t *testing.T) {
	m, err := parseMeminfo(strings.NewReader("MemTotal:        4006980 kB\nMemFree:         1796584 kB\nMemAvailable:    3587552 kB\n"))
	if err != nil || m.Total != 4006980*1024 || m.Available != 3587552*1024 {
		t.Errorf("parseMeminfo = %+v, %v", m, err)
	}
	// A kernel without MemAvailable (older than 3.14) isn't one Roostack
	// runs on; it's an error rather than a guess.
	if _, err := parseMeminfo(strings.NewReader("MemTotal: 4006980 kB\n")); err == nil {
		t.Error("no MemAvailable: want an error")
	}
}

func TestParseProcStat(t *testing.T) {
	t.Parallel()
	// Two processors; guest time (the last two) is already in user's.
	c, err := parseProcStat(strings.NewReader("cpu  100 10 50 800 40 0 0 0 7 0\ncpu0 50 5 25 400 20 0 0 0 0 0\ncpu1 50 5 25 400 20 0 0 0 0 0\nintr 1\n"))
	if err != nil || c.cpus != 2 || c.total != 1000 || c.busy != 160 {
		t.Errorf("parseProcStat = %+v, %v; want 2 cpus, total 1000, busy 160", c, err)
	}
	if _, err := parseProcStat(strings.NewReader("intr 1\n")); err == nil {
		t.Error("no cpu line: want an error")
	}
}

func TestCPUSampler(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	readings := []cpuTimes{{busy: 100, total: 1000, cpus: 4}}
	var slept time.Duration
	c := &cpuSampler{
		read: func() (cpuTimes, error) {
			r := readings[0]
			if len(readings) > 1 {
				readings = readings[1:]
			}
			return r, nil
		},
		now: func() time.Time { return now },
		sleep: func(_ context.Context, d time.Duration) error {
			slept += d
			now = now.Add(d)
			return nil
		},
	}
	// The first time, it measures over half a second: 50 of 100 ticks busy.
	readings = append(readings, cpuTimes{busy: 150, total: 1100, cpus: 4})
	if busy, cpus, err := c.measure(t.Context()); err != nil || busy != 50 || cpus != 4 || slept != cpuSample {
		t.Fatalf("first = %d%%, %d cpus, %v, slept %v; want 50%%, 4, half a second", busy, cpus, err, slept)
	}
	// Ten seconds later, it measures since then without waiting.
	now = now.Add(10 * time.Second)
	readings = []cpuTimes{{busy: 160, total: 2100, cpus: 4}}
	if busy, _, _ := c.measure(t.Context()); busy != 1 || slept != cpuSample {
		t.Errorf("ten seconds on = %d%%, slept %v; want 1%%, no more waiting", busy, slept)
	}
	// Asked again at once, it gives the same figure.
	readings = []cpuTimes{{busy: 2000, total: 2100, cpus: 4}}
	if busy, _, _ := c.measure(t.Context()); busy != 1 {
		t.Errorf("at once = %d%%, want the last figure (1%%)", busy)
	}
	// A minute later, the last reading is too old: it waits again.
	now = now.Add(time.Minute)
	readings = []cpuTimes{{busy: 2000, total: 3000, cpus: 4}, {busy: 2100, total: 3100, cpus: 4}}
	if busy, _, _ := c.measure(t.Context()); busy != 100 || slept != 2*cpuSample {
		t.Errorf("a minute on = %d%%, slept %v; want 100%%, a fresh measurement", busy, slept)
	}
}
