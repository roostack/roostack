package node

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Usage is how busy a server is, for Home (docs/DESIGN.md "Layout"):
// its memory now, and how busy its processors have been lately.
type Usage struct {
	Memory MemoryInfo
	// CPUs is how many processors (cores and threads) the server has.
	CPUs int
	// CPUBusy is the share of the processors' time they were busy, from 0
	// to 100: since the last time anyone asked if that was a few seconds
	// ago (Home asks every ten), otherwise over half a second.
	CPUBusy int
}

// cpuWindow bounds the earlier reading a CPU figure is measured from:
// one under a second old says little (the last figure is given again),
// and one over half a minute old would average too long a time.
const (
	cpuMinWindow = time.Second
	cpuMaxWindow = 30 * time.Second
	// cpuSample is how long a fresh measurement waits between readings.
	cpuSample = 500 * time.Millisecond
)

// cpuTimes is one reading of /proc/stat's first line: the time all
// processors spent busy and in all, in the kernel's ticks.
type cpuTimes struct {
	busy, total uint64
	cpus        int
}

// cpuSampler keeps the last reading of /proc/stat so that a server asked
// every few seconds can say how busy it was in between without waiting.
type cpuSampler struct {
	mu   sync.Mutex
	at   time.Time
	last cpuTimes
	busy int
	// read reads /proc/stat (a test's fake when set); now and sleep are
	// the clock's.
	read  func() (cpuTimes, error)
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// Usage reads this server's memory and how busy its processors are.
func (n *Local) Usage(ctx context.Context) (Usage, error) {
	mem, err := n.MemoryInfo(ctx)
	if err != nil {
		return Usage{}, err
	}
	busy, cpus, err := n.cpu.measure(ctx)
	if err != nil {
		return Usage{}, err
	}
	return Usage{Memory: mem, CPUs: cpus, CPUBusy: busy}, nil
}

// measure is how busy the processors were since the last reading, if it
// was a usable time ago, or else over cpuSample.
func (c *cpuSampler) measure(ctx context.Context) (busy, cpus int, err error) {
	read, now, sleep := c.read, c.now, c.sleep
	if read == nil {
		read = readProcStat
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepCtx
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := now()
	if !c.at.IsZero() && t.Sub(c.at) < cpuMinWindow {
		return c.busy, c.last.cpus, nil
	}
	cur, err := read()
	if err != nil {
		return 0, 0, err
	}
	prev := c.last
	if c.at.IsZero() || t.Sub(c.at) > cpuMaxWindow {
		if err := sleep(ctx, cpuSample); err != nil {
			return 0, 0, err
		}
		prev = cur
		if cur, err = read(); err != nil {
			return 0, 0, err
		}
		t = now()
	}
	c.at, c.last, c.busy = t, cur, busyShare(prev, cur)
	return c.busy, cur.cpus, nil
}

// busyShare is the share of the time between two readings the processors
// were busy, 0 to 100 (0 when no time passed).
func busyShare(prev, cur cpuTimes) int {
	if cur.total <= prev.total || cur.busy < prev.busy {
		return 0
	}
	return int(min((cur.busy-prev.busy)*100/(cur.total-prev.total), 100))
}

func readProcStat() (cpuTimes, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTimes{}, err
	}
	defer f.Close()
	return parseProcStat(f)
}

// parseProcStat reads the "cpu" line's times (user, nice, system, idle,
// iowait, irq, softirq, steal; guest time is already counted in user)
// and counts the "cpuN" lines. Idle and waiting for disks aren't busy.
func parseProcStat(f io.Reader) (cpuTimes, error) {
	var out cpuTimes
	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] != "cpu" {
			out.cpus++
			continue
		}
		if len(fields) < 5 {
			return cpuTimes{}, errors.New("/proc/stat: a short cpu line")
		}
		for i, s := range fields[1:min(len(fields), 9)] {
			v, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return cpuTimes{}, err
			}
			out.total += v
			if i != 3 && i != 4 { // idle, iowait
				out.busy += v
			}
		}
		found = true
	}
	if err := sc.Err(); err != nil {
		return cpuTimes{}, err
	}
	if !found {
		return cpuTimes{}, errors.New("/proc/stat: no cpu line")
	}
	return out, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
