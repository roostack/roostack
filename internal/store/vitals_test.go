package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// How each server is doing (SPEC.md §6.15.1): every reading kept, summed
// into its hour's averages and peaks, with its services' shares; old ones
// pruned.
func TestServerReadings(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "roostack.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hour := time.Unix(1_800_000_000, 0).Truncate(time.Hour)
	for i, cpu := range []float64{10, 30, 80} {
		r := ServerReading{Server: "n-1", At: hour.Add(time.Duration(i) * time.Minute), CPU: cpu, MemUsed: int64(i+1) * 1000, MemTotal: 16000,
			NetIn: int64(cpu) * 10, Temp: 40 + cpu/10}
		if err := s.AddServerReading(ctx, r, []ServiceReading{{Service: "immich", Memory: 500, CPU: cpu / 2}}); err != nil {
			t.Fatal(err)
		}
	}
	s.AddServerReading(ctx, ServerReading{Server: "n-2", At: hour, CPU: 99}, nil)
	rs, _ := s.ServerReadings(ctx, "n-1", hour)
	if len(rs) != 3 || rs[2].CPU != 80 || rs[0].At != hour {
		t.Errorf("readings: %+v", rs)
	}
	hs, _ := s.ServerHours(ctx, "n-1", hour)
	if len(hs) != 1 || hs[0].Readings != 3 || hs[0].CPU != 40 || hs[0].CPUMax != 80 || hs[0].Mem != 2000 || hs[0].MemMax != 3000 ||
		hs[0].MemTotal != 16000 || hs[0].NetInMax != 800 || hs[0].TempMax != 48 {
		t.Errorf("hours: %+v", hs)
	}
	us, _ := s.ServiceReadings(ctx, "n-1", hour)
	if len(us) != 3 || us[2].CPU != 40 || us[0].Service != "immich" {
		t.Errorf("services: %+v", us)
	}
	if err := s.PruneServerReadings(ctx, hour.Add(2*time.Minute), hour); err != nil {
		t.Fatal(err)
	}
	if rs, _ := s.ServerReadings(ctx, "n-1", time.Time{}); len(rs) != 1 {
		t.Errorf("after pruning: %+v", rs)
	}
	if hs, _ := s.ServerHours(ctx, "n-1", time.Time{}); len(hs) != 1 {
		t.Errorf("the hour went: %+v", hs)
	}
}
