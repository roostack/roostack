package node

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How each server is doing (SPEC.md §6.15.1): every minute, each server
// reads its processors, memory, swap, network, temperatures, and each
// service's share from /proc, /sys, and its systemd units' cgroups, with
// no other software, and reports the latest reading with its status.

// Vitals is one reading of how a server is doing.
type Vitals struct {
	At time.Time `json:"at"`
	// CPUs is how many processors (cores and threads); CPUBusy how busy
	// they all were since the last reading, from 0 to 100; Load the load
	// averages over 1, 5, and 15 minutes.
	CPUs    int        `json:"cpus"`
	CPUBusy float64    `json:"cpu_busy"`
	Load    [3]float64 `json:"load"`
	// Model is the processor's name, as /proc/cpuinfo has it.
	Model string `json:"model,omitempty"`
	// Memory and swap, in bytes: MemAvailable counts what the system
	// gives back (MemAvailable).
	MemTotal     int64 `json:"mem_total"`
	MemAvailable int64 `json:"mem_available"`
	SwapTotal    int64 `json:"swap_total"`
	SwapFree     int64 `json:"swap_free"`
	// SwapIn is how much was read back from swap, bytes a second, since
	// the last reading: what slows a server down.
	SwapIn int64 `json:"swap_in"`
	// NetIn and NetOut are bytes a second on its real network interfaces
	// (not WireGuard's, containers', or VMs'), since the last reading.
	NetIn  int64 `json:"net_in"`
	NetOut int64 `json:"net_out"`
	// Temp is the hottest sensor's temperature (°C) relative to its high
	// mark, TempHigh (the sensor's own, or DefaultTempHigh), and TempName
	// what it measures; no Temp where the server reports none.
	Temp     float64 `json:"temp,omitempty"`
	TempHigh float64 `json:"temp_high,omitempty"`
	TempName string  `json:"temp_name,omitempty"`
	// Up is how long since the server started.
	Up time.Duration `json:"up"`
	// Services are each service's memory and processor time, by the
	// service's name ("" for Roostack's own: Caddy, CrowdSec, and Roostack
	// itself).
	Services []ServiceUse `json:"services,omitempty"`
}

// ServiceUse is a service's share of a server.
type ServiceUse struct {
	Service string `json:"service"`
	// Memory is what its containers use now (memory.current), in bytes;
	// CPU their processor time since the last reading, as a share of one
	// processor (100 is one processor busy all the time).
	Memory int64   `json:"memory"`
	CPU    float64 `json:"cpu"`
}

// DefaultTempHigh is the high mark for a sensor that gives none.
const DefaultTempHigh = 90

// MemoryUsed is what's in use, not counting what the system gives back.
func (v Vitals) MemoryUsed() int64 { return v.MemTotal - v.MemAvailable }

// vitalsReader reads the counters behind Vitals, keeping the last ones to
// give rates since then. Paths are under root ("/" outside tests).
type vitalsReader struct {
	root string
	// uid is the roostack user's, whose systemd manager runs the services.
	uid int
	// services names the services on this server, for their units'
	// cgroups (roostack-<service>-…).
	services func() []string

	mu   sync.Mutex
	last vitalsCounters
	at   time.Time
	now  Vitals
}

// vitalsCounters are the running totals rates come from.
type vitalsCounters struct {
	cpuBusy, cpuTotal uint64
	swapIn            uint64 // pages
	netIn, netOut     uint64
	usage             map[string]uint64 // a unit's processor time, µs
}

func (r *vitalsReader) path(p string) string { return filepath.Join(r.root, p) }

// read takes a reading, with rates since the last one.
func (r *vitalsReader) read(now time.Time) Vitals {
	v := Vitals{At: now}
	c := vitalsCounters{usage: map[string]uint64{}}
	c.cpuBusy, c.cpuTotal, v.CPUs = r.cpuTimes()
	v.Load = r.loadavg()
	v.Model = r.cpuModel()
	r.meminfo(&v)
	c.swapIn = r.vmstat("pswpin")
	c.netIn, c.netOut = r.netBytes()
	v.Temp, v.TempHigh, v.TempName = r.hottest()
	v.Up = r.uptime()
	units := r.units()
	memory := map[string]int64{}
	for unit, dir := range units {
		c.usage[unit] = readUsage(filepath.Join(dir, "cpu.stat"))
		memory[unit] = readInt(filepath.Join(dir, "memory.current"))
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.at.IsZero() {
		secs := now.Sub(r.at).Seconds()
		if d := c.cpuTotal - r.last.cpuTotal; c.cpuTotal > r.last.cpuTotal {
			v.CPUBusy = 100 * float64(c.cpuBusy-r.last.cpuBusy) / float64(d)
		}
		if secs > 0 {
			v.SwapIn = int64(float64(sub(c.swapIn, r.last.swapIn)) * float64(os.Getpagesize()) / secs)
			v.NetIn = int64(float64(sub(c.netIn, r.last.netIn)) / secs)
			v.NetOut = int64(float64(sub(c.netOut, r.last.netOut)) / secs)
		}
		byService := map[string]*ServiceUse{}
		for unit := range units {
			svc := r.serviceOf(unit)
			u := byService[svc]
			if u == nil {
				u = &ServiceUse{Service: svc}
				byService[svc] = u
			}
			u.Memory += memory[unit]
			if prev, ok := r.last.usage[unit]; ok && secs > 0 {
				u.CPU += float64(sub(c.usage[unit], prev)) / 1e6 / secs * 100
			}
		}
		for _, u := range byService {
			v.Services = append(v.Services, *u)
		}
		slices.SortFunc(v.Services, func(a, b ServiceUse) int { return int(b.Memory - a.Memory) })
	}
	r.last, r.at, r.now = c, now, v
	return v
}

// latest is the last reading (zero before the first).
func (r *vitalsReader) latest() Vitals {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func sub(a, b uint64) uint64 {
	if a < b {
		return 0 // a counter that went back (a restart, an interface gone)
	}
	return a - b
}

// cpuTimes reads /proc/stat's processors.
func (r *vitalsReader) cpuTimes() (busy, total uint64, cpus int) {
	f, err := os.Open(r.path("proc/stat"))
	if err != nil {
		return 0, 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		switch {
		case len(fields) > 4 && fields[0] == "cpu":
			for i, s := range fields[1:] {
				n, _ := strconv.ParseUint(s, 10, 64)
				total += n
				if i != 3 && i != 4 { // idle, iowait
					busy += n
				}
			}
		case strings.HasPrefix(fields[0], "cpu"):
			cpus++
		}
	}
	return busy, total, cpus
}

func (r *vitalsReader) loadavg() (l [3]float64) {
	data, _ := os.ReadFile(r.path("proc/loadavg"))
	for i, f := range strings.Fields(string(data)) {
		if i < 3 {
			l[i], _ = strconv.ParseFloat(f, 64)
		}
	}
	return l
}

func (r *vitalsReader) cpuModel() string {
	data, _ := os.ReadFile(r.path("proc/cpuinfo"))
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (r *vitalsReader) meminfo(v *Vitals) {
	data, _ := os.ReadFile(r.path("proc/meminfo"))
	for _, line := range strings.Split(string(data), "\n") {
		k, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
		if err != nil {
			continue
		}
		switch k {
		case "MemTotal":
			v.MemTotal = kb * 1024
		case "MemAvailable":
			v.MemAvailable = kb * 1024
		case "SwapTotal":
			v.SwapTotal = kb * 1024
		case "SwapFree":
			v.SwapFree = kb * 1024
		}
	}
}

func (r *vitalsReader) vmstat(key string) uint64 {
	data, _ := os.ReadFile(r.path("proc/vmstat"))
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok && k == key {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

// netBytes adds up the real interfaces' bytes in and out (/proc/net/dev).
func (r *vitalsReader) netBytes() (in, out uint64) {
	data, _ := os.ReadFile(r.path("proc/net/dev"))
	for _, line := range strings.Split(string(data), "\n") {
		name, rest, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "lo" || virtualIfaceName(name) {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		in, out = in+rx, out+tx
	}
	return in, out
}

// virtualIfaceName reports interfaces that aren't a way onto the network
// (as labnet.HomeAddresses leaves out).
func virtualIfaceName(name string) bool {
	for _, p := range []string{"roostack", "wg", "podman", "veth", "docker", "br-", "cni", "virbr", "tun", "tap"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// hottest is the sensor nearest its high mark: its temperature, the
// mark, and what it measures.
func (r *vitalsReader) hottest() (temp, high float64, name string) {
	inputs, _ := filepath.Glob(r.path("sys/class/hwmon/hwmon*/temp*_input"))
	best := -1.0
	for _, in := range inputs {
		t := float64(readInt(in)) / 1000
		if t <= 0 || t > 150 {
			continue
		}
		base := strings.TrimSuffix(in, "_input")
		mark := float64(readInt(base+"_max")) / 1000
		if mark < 40 || mark > 130 {
			mark = float64(readInt(base+"_crit")) / 1000
		}
		if mark < 40 || mark > 130 {
			mark = DefaultTempHigh
		}
		if t/mark > best {
			best, temp, high = t/mark, t, mark
			chip, _ := os.ReadFile(filepath.Join(filepath.Dir(in), "name"))
			label, _ := os.ReadFile(base + "_label")
			name = strings.TrimSpace(strings.TrimSpace(string(chip)) + " " + strings.TrimSpace(string(label)))
		}
	}
	return temp, high, name
}

func (r *vitalsReader) uptime() time.Duration {
	data, _ := os.ReadFile(r.path("proc/uptime"))
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0
	}
	s, _ := strconv.ParseFloat(f[0], 64)
	return time.Duration(s) * time.Second
}

// units are Roostack's units with a cgroup, by name: the services' and
// Caddy's under the roostack user's systemd manager, and Roostack itself.
func (r *vitalsReader) units() map[string]string {
	out := map[string]string{}
	user := r.path("sys/fs/cgroup/user.slice/user-" + strconv.Itoa(r.uid) + ".slice/user@" + strconv.Itoa(r.uid) + ".service")
	dirs, _ := filepath.Glob(filepath.Join(user, "*.slice", "roostack-*.service"))
	more, _ := filepath.Glob(filepath.Join(user, "roostack-*.service"))
	for _, d := range append(dirs, more...) {
		out[strings.TrimSuffix(filepath.Base(d), ".service")] = d
	}
	if d := r.path("sys/fs/cgroup/system.slice/roostack.service"); dirExists(d) {
		out["roostack"] = d
	}
	return out
}

// serviceOf is the service a unit belongs to: the longest service name
// it starts with (names can have hyphens), or "" for Roostack's own.
func (r *vitalsReader) serviceOf(unit string) string {
	best := ""
	var names []string
	if r.services != nil {
		names = r.services()
	}
	for _, s := range names {
		if (unit == "roostack-"+s || strings.HasPrefix(unit, "roostack-"+s+"-")) && len(s) > len(best) {
			best = s
		}
	}
	return best
}

func readInt(path string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	return n
}

// readUsage reads usage_usec from a cgroup's cpu.stat.
func readUsage(path string) uint64 {
	data, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok && k == "usage_usec" {
			n, _ := strconv.ParseUint(v, 10, 64)
			return n
		}
	}
	return 0
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// VitalsEvery is how often each server reads how it's doing.
const VitalsEvery = time.Minute

// RunVitals reads how this server is doing every minute until ctx ends.
func (n *Local) RunVitals(ctx context.Context) {
	r := n.vitalsReader()
	r.read(time.Now())
	t := time.NewTicker(VitalsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			r.read(now)
		}
	}
}

// Vitals is this server's last reading of how it's doing (zero before
// the first minute).
func (n *Local) Vitals(context.Context) (Vitals, error) { return n.vitalsReader().latest(), nil }

// vitalsReader is this server's, made on first use.
func (n *Local) vitalsReader() *vitalsReader {
	n.vitalsOnce.Do(func() {
		n.vitals = &vitalsReader{root: "/", uid: os.Getuid(), services: func() []string {
			n.mu.Lock()
			a := n.assignment
			n.mu.Unlock()
			var names []string
			for _, s := range a.Services {
				names = append(names, s.Name())
			}
			return names
		}}
	})
	return n.vitals
}

// Vitals is the server's latest reading, as its status last said (zero
// if it hasn't said).
func (r *Remote) Vitals(context.Context) (Vitals, error) {
	st, err := r.wireStatus()
	if err != nil || st.Vitals == nil {
		return Vitals{}, err
	}
	return *st.Vitals, nil
}
