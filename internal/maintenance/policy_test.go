package maintenance

import (
	"strings"
	"testing"
	"time"
)

func at(day int, hh, mm int) time.Time {
	return time.Date(2026, 9, day, hh, mm, 0, 0, time.Local)
}

func TestInWindow(t *testing.T) {
	p := DefaultPolicy() // 04:00-06:00
	for _, c := range []struct {
		t    time.Time
		want bool
	}{
		{at(1, 3, 59), false}, {at(1, 4, 0), true}, {at(1, 5, 59), true}, {at(1, 6, 0), false},
	} {
		if got := p.InWindow(c.t); got != c.want {
			t.Errorf("%s: got %v", c.t.Format("15:04"), got)
		}
	}

	p.WindowStart = 23 * time.Hour // 23:00-01:00, crossing midnight
	for _, c := range []struct {
		t    time.Time
		want bool
	}{
		{at(1, 22, 59), false}, {at(1, 23, 30), true}, {at(2, 0, 30), true}, {at(2, 1, 0), false},
	} {
		if got := p.InWindow(c.t); got != c.want {
			t.Errorf("midnight window %s: got %v", c.t.Format("15:04"), got)
		}
	}
}

func TestDecide(t *testing.T) {
	p := DefaultPolicy()
	window := at(2, 4, 30)
	pendingShort := window.Add(-2 * time.Hour)
	pendingLong := window.Add(-25 * time.Hour)
	upload := &Running{Name: "backup", Interruptible: true, Progress: 62}
	dumping := &Running{Name: "backup", Interruptible: false, Progress: -1}

	cases := []struct {
		name    string
		policy  Policy
		now     time.Time
		pending time.Time
		running *Running
		want    Action
		reason  string
	}{
		{"nothing pending", p, window, time.Time{}, nil, Wait, "no reboot needed"},
		{"outside window", p, at(2, 12, 0), pendingShort, nil, Wait, "maintenance window"},
		{"idle in window", p, window, pendingShort, nil, Reboot, "rebooting"},
		{"waits for job", p, window, pendingShort, upload, Wait, "waiting for backup to finish (62%)"},
		{"interrupts after deadline", p, window, pendingLong, upload, InterruptAndReboot, "pausing backup"},
		{"never interrupts prepare step", p, window, pendingLong, dumping, Wait, "waiting for backup to finish"},
		{"deadline only acts in window", p, at(2, 12, 0), pendingLong, upload, Wait, "maintenance window"},
		{"notify mode", Policy{Mode: RebootNotify, WindowStart: p.WindowStart, WindowLength: p.WindowLength},
			window, pendingLong, nil, Wait, "automatic reboots are off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Decide(c.policy, c.now, c.pending, c.running)
			if d.Action != c.want || !strings.Contains(d.Reason, c.reason) {
				t.Errorf("got %v %q, want %v containing %q", d.Action, d.Reason, c.want, c.reason)
			}
		})
	}
}
