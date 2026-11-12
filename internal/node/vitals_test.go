package node

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// write puts files under root.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// How a server is doing (SPEC.md §6.15.1): read from /proc and /sys, with
// rates since the last reading, each service's share from its units'
// cgroups, and the sensor nearest its high mark.
func TestVitals(t *testing.T) {
	root := t.TempDir()
	user := "sys/fs/cgroup/user.slice/user-999.slice/user@999.service/app.slice/"
	tree := func(busy, idle, swap, rx, tx, immich, caddy string) map[string]string {
		return map[string]string{
			"proc/stat":                          "cpu  " + busy + " 0 0 " + idle + " 0 0 0 0 0 0\ncpu0 1 0 0 1\ncpu1 1 0 0 1\nintr 5\n",
			"proc/loadavg":                       "1.20 1.00 0.80 2/300 1234\n",
			"proc/cpuinfo":                       "processor\t: 0\nmodel name\t: Intel(R) N100\n",
			"proc/meminfo":                       "MemTotal:       16000000 kB\nMemAvailable:   11000000 kB\nSwapTotal:       2000000 kB\nSwapFree:        1900000 kB\n",
			"proc/vmstat":                        "pgfault 10\npswpin " + swap + "\npswpout 3\n",
			"proc/net/dev":                       "Inter-| Receive\n face |bytes\n    lo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\n enp1s0: " + rx + " 0 0 0 0 0 0 0 " + tx + " 0 0 0 0 0 0 0\n roostack: 5000 0 0 0 0 0 0 0 5000 0 0 0 0 0 0 0\n",
			"proc/uptime":                        "1036800.5 100.0\n",
			"sys/class/hwmon/hwmon0/name":        "coretemp\n",
			"sys/class/hwmon/hwmon0/temp1_input": "52000\n",
			"sys/class/hwmon/hwmon0/temp1_max":   "100000\n",
			"sys/class/hwmon/hwmon0/temp1_label": "Package id 0\n",
			"sys/class/hwmon/hwmon1/name":        "nvme\n",
			"sys/class/hwmon/hwmon1/temp1_input": "45000\n",
			"sys/class/hwmon/hwmon1/temp1_max":   "-273000\n", // nonsense: the default mark
			user + "roostack-immich-server.service/memory.current":  "1500000000\n",
			user + "roostack-immich-server.service/cpu.stat":        "usage_usec " + immich + "\nuser_usec 1\n",
			user + "roostack-immich-db-main.service/memory.current": "600000000\n",
			user + "roostack-immich-db-main.service/cpu.stat":       "usage_usec 0\n",
			user + "roostack-caddy.service/memory.current":          "80000000\n",
			user + "roostack-caddy.service/cpu.stat":                "usage_usec " + caddy + "\n",
		}
	}
	writeTree(t, root, tree("100", "300", "10", "1000", "500", "0", "0"))
	r := &vitalsReader{root: root, uid: 999, services: func() []string { return []string{"immich", "imm"} }}
	start := time.Unix(1_800_000_000, 0)
	first := r.read(start)
	if first.CPUs != 2 || first.CPUBusy != 0 || len(first.Services) != 0 || first.MemTotal != 16000000*1024 ||
		first.Model != "Intel(R) N100" || first.Up != 1036800*time.Second || first.Load != [3]float64{1.2, 1, 0.8} {
		t.Fatalf("first: %+v", first)
	}
	// A minute later: 30 of 100 ticks busy, 60 pages swapped in, 6 MB in.
	writeTree(t, root, tree("130", "370", "70", "6001000", "60500", "30000000", "600000"))
	v := r.read(start.Add(time.Minute))
	if v.CPUBusy != 30 || v.SwapIn != int64(60*os.Getpagesize()/60) || v.NetIn != 100000 || v.NetOut != 1000 {
		t.Errorf("rates: %+v", v)
	}
	if v.Temp != 52 || v.TempHigh != 100 || v.TempName != "coretemp Package id 0" {
		t.Errorf("temperature: %v of %v (%s)", v.Temp, v.TempHigh, v.TempName)
	}
	if len(v.Services) != 2 || v.Services[0] != (ServiceUse{Service: "immich", Memory: 2100000000, CPU: 50}) ||
		v.Services[1] != (ServiceUse{Service: "", Memory: 80000000, CPU: 1}) {
		t.Errorf("services: %+v", v.Services)
	}
	if r.latest().At != v.At || v.MemoryUsed() != 5000000*1024 {
		t.Error("latest")
	}
}
