package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Results are kept with their hour's summary; outages open and close;
// pruning keeps what's recent.
func TestMonitorStore(t *testing.T) {
	s, ctx := open(t), context.Background()
	hour := time.Date(2026, 10, 19, 14, 0, 0, 0, time.UTC)
	add := func(min int, up, paused bool, ms int) {
		t.Helper()
		r := MonitorResult{Key: "service:vault:web", At: hour.Add(time.Duration(min) * time.Minute), Up: up, Paused: paused, Millis: ms,
			Status: 200, CertIssued: hour.AddDate(0, -1, 0), CertExpires: hour.AddDate(0, 2, 0)}
		if !up {
			r.Err, r.Status = "no answer within 10 seconds", 0
		}
		if err := s.AddMonitorResult(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	add(0, true, false, 100)
	add(1, true, false, 300)
	add(2, false, false, 10000)
	add(3, false, true, 0)
	add(61, true, false, 50)

	hours, err := s.MonitorHours(ctx, "service:vault:web", hour)
	if err != nil || len(hours) != 2 {
		t.Fatalf("hours %+v, %v", hours, err)
	}
	if h := hours[0]; h.Checks != 4 || h.Ups != 2 || h.Paused != 1 || h.MillisSum != 400 || h.MillisMax != 300 || !h.Hour.Equal(hour) {
		t.Errorf("first hour %+v", h)
	}
	last, err := s.LastMonitorResults(ctx, "service:vault:web", 2)
	if err != nil || len(last) != 2 || last[0].Millis != 50 || !last[1].Paused || !last[0].CertExpires.Equal(hour.AddDate(0, 2, 0)) {
		t.Errorf("last two %+v, %v", last, err)
	}
	all, _ := s.MonitorResults(ctx, "service:vault:web", hour.Add(time.Minute))
	if len(all) != 4 || all[1].Err != "no answer within 10 seconds" {
		t.Errorf("since a minute in: %+v", all)
	}

	if err := s.StartMonitorOutage(ctx, "service:vault:web", hour.Add(2*time.Minute), "no answer"); err != nil {
		t.Fatal(err)
	}
	if o, _ := s.MonitorOutages(ctx, "service:vault:web", hour.AddDate(0, 1, 0)); len(o) != 1 || !o[0].Ended.IsZero() {
		t.Errorf("an open outage is listed whenever it started: %+v", o)
	}
	s.EndMonitorOutage(ctx, "service:vault:web", hour.Add(61*time.Minute))
	if o, _ := s.MonitorOutages(ctx, "service:vault:web", time.Time{}); len(o) != 1 || !o[0].Ended.Equal(hour.Add(61*time.Minute)) {
		t.Errorf("ended: %+v", o)
	}

	if err := s.PruneMonitor(ctx, hour.Add(30*time.Minute), hour.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	all, _ = s.MonitorResults(ctx, "service:vault:web", time.Time{})
	hours, _ = s.MonitorHours(ctx, "service:vault:web", time.Time{})
	if len(all) != 1 || len(hours) != 1 {
		t.Errorf("after pruning: %d results, %d hours", len(all), len(hours))
	}
	s.ForgetMonitorCheck(ctx, "service:vault:web")
	if all, _ = s.MonitorResults(ctx, "service:vault:web", time.Time{}); len(all) != 0 {
		t.Errorf("forgotten: %+v", all)
	}
}

// People's own checks are added, listed and removed.
func TestMonitorChecks(t *testing.T) {
	s, ctx := open(t), context.Background()
	id, err := s.AddMonitorCheck(ctx, MonitorCheck{Name: "NAS", Address: "https://nas.local:5001", SkipVerify: true, Alert: true})
	if err != nil {
		t.Fatal(err)
	}
	s.AddMonitorCheck(ctx, MonitorCheck{Name: "Router", Address: "192.168.1.1"})
	checks, err := s.MonitorChecks(ctx)
	if err != nil || len(checks) != 2 || checks[0].ID != id || !checks[0].SkipVerify || !checks[0].Alert || checks[1].Alert {
		t.Fatalf("checks %+v, %v", checks, err)
	}
	if err := s.RemoveMonitorCheck(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMonitorCheck(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing it again: %v", err)
	}
}
